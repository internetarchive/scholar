// pdf runs PDF post-processing (GROBID TEI, pdftotext, page-0 thumbnail)
// in-process via the blobproc library. Derivatives are written through to S3
// (the rederivable cache that scholar's web UI and the reindex path read) and
// also returned in memory so callers can index without an S3 round-trip.
//
// This replaces the old HTTP round-trip to blobprocd (POST /spool, poll for
// completion, fetch results from S3), which imposed a ~10-minute latency floor
// from blobproc's systemd-timer spool cycle. See trawler/daily/BLOBPROC-INLINE.md.
package pdf

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/internetarchive/scholar/blobproc"
	"github.com/miku/grobidclient"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/spf13/viper"
)

// defaultGrobidHost is used when [grobid].host is unset in config.
const defaultGrobidHost = "https://scholar.archive.org/_grobid"

// defaultGrobidMaxFileSize bounds the input we hand to GROBID; larger PDFs
// still get text + thumbnail but skip the GROBID step.
const defaultGrobidMaxFileSize = 256 << 20 // 256 MiB

// Content holds the outputs produced for a single PDF.
type Content struct {
	GrobidXML []byte
	PdfText   []byte
}

// Processor runs blobproc.ProcessPDF in-process. Construct once per activity
// with NewProcessor and reuse it across PDFs: the GROBID client and S3
// BlobStore it holds are safe for concurrent use, so Process may be called
// from multiple goroutines.
type Processor struct {
	grobid            *grobidclient.Grobid
	s3                *blobproc.BlobStore
	grobidMaxFileSize int64
	// Heartbeater, if set, is invoked periodically while a PDF is being
	// processed so a long GROBID call doesn't trip an activity HeartbeatTimeout.
	Heartbeater func(string)
}

// NewProcessor builds a Processor from viper config: [grobid] host/max_filesize
// and the [s3] endpoint/credentials for the write-through derivative store
// (same store the rest of trawler reads derivatives from). The optional
// heartbeat callback is wired to activity.RecordHeartbeat by callers.
func NewProcessor(heartbeat func(string)) (*Processor, error) {
	mc, err := minio.New(viper.GetString("s3.endpoint"), &minio.Options{
		Creds: credentials.NewStaticV4(
			viper.GetString("s3.access_id"),
			viper.GetString("s3.secret_key"),
			"",
		),
		Secure: false,
	})
	if err != nil {
		return nil, fmt.Errorf("s3 client init failed: %w", err)
	}

	host := viper.GetString("grobid.host")
	if host == "" {
		host = defaultGrobidHost
	}
	maxSize := viper.GetInt64("grobid.max_filesize")
	if maxSize <= 0 {
		maxSize = defaultGrobidMaxFileSize
	}

	return &Processor{
		grobid:            grobidclient.New(host),
		s3:                &blobproc.BlobStore{Client: mc},
		grobidMaxFileSize: maxSize,
		Heartbeater:       heartbeat,
	}, nil
}

func (p *Processor) beatHeart(msg string) {
	if p.Heartbeater != nil {
		p.Heartbeater(msg)
	}
}

var knownGrobidErrors = []string{
	"[BAD_INPUT_DATA]",
	"[NO_BLOCKS]",
	"[TOO_MANY_TOKENS]",
}

// Process runs the full per-PDF pipeline (pdfextract for text + thumbnail, then
// GROBID for TEI) on pdfBs. Derivatives are written through to S3 and the
// GROBID XML + extracted text are returned in memory. sha1 is used only for
// logging; blobproc derives the canonical sha1 from the bytes for S3 keys.
//
// An error is returned when we failed in calling out to grobid or if grobid
// returned a surprising error. Always check to see if the XML is sane; a bad,
// unparseable PDF will result in no XML.
func (p *Processor) Process(ctx context.Context, pdfBs []byte, sha1 string) (Content, error) {
	out := Content{}
	f, err := os.CreateTemp("", "trawler-pdf-*.pdf")
	if err != nil {
		return out, fmt.Errorf("could not create temp file: %w", err)
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(pdfBs); err != nil {
		f.Close()
		return out, fmt.Errorf("could not write temp pdf: %w", err)
	}
	if err := f.Close(); err != nil {
		return out, fmt.Errorf("could not close temp pdf: %w", err)
	}

	// Keep the activity alive across the synchronous GROBID call.
	stop := p.heartbeatLoop(ctx)
	defer stop()

	result, errs := blobproc.ProcessPDF(ctx, blobproc.ProcessPDFParams{
		Path:              f.Name(),
		Size:              int64(len(pdfBs)),
		Grobid:            p.grobid,
		S3:                p.s3,
		GrobidMaxFileSize: p.grobidMaxFileSize,
		Logger:            slog.Default(),
	})

	out.GrobidXML = result.TEI
	out.PdfText = []byte(result.Text)

	for _, e := range errs {
		slog.Warn("pdf processing error", "sha1", sha1, "err", e.Error())

		if strings.Contains(e.Error(), "grobid client failure") {
			if strings.Contains(e.Error(), "Client.Timeout exceeded while awaiting headers") {
				// grobid timing out means just ignore this pdf
				out.GrobidXML = []byte{}
			} else {
				return out, e
			}
		} else if strings.Contains(e.Error(), "non-200 response from grobid") {
			var knownErrCode bool
			for _, errcode := range knownGrobidErrors {
				if strings.HasPrefix(string(result.TEI), errcode) {
					knownErrCode = true
					break
				}
			}
			if !knownErrCode {
				// We do this dance because it's cause for stopping everything if
				// Grobid is having an outage. It will return 500 for a bad pdf and
				// also for general errors; relying only on status code can't
				// differentiate between grobid outage vs. bad pdf.
				//
				// there are almost certainly error codes that should be in
				// knownGrobidErrors but we haven't seen them yet; it will be annoying
				// to whack a mole them but that's better then treating PDFs as bad
				// when grobid is just quietly having an outage.
				return out, fmt.Errorf("unknown grobid error: %q", result.TEI)
			} else {
				out.GrobidXML = []byte{}
			}
		}
	}

	return out, nil
}

// heartbeatLoop fires Heartbeater every 30s until the returned stop func is
// called (or ctx is cancelled). It is a no-op when no Heartbeater is set.
func (p *Processor) heartbeatLoop(ctx context.Context) (stop func()) {
	if p.Heartbeater == nil {
		return func() {}
	}
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				p.beatHeart("pdf-processing")
			}
		}
	}()
	return func() { close(done) }
}
