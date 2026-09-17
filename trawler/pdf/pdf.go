// Package pdf runs PDF post-processing -- GROBID TEI, MuPDF text, page-0
// thumbnail -- in-process. Derivatives are written through to S3 (the
// rederivable cache that scholar's web UI and the reindex path read) and also
// returned in memory so callers can index without an S3 round-trip.
//
// This was blobproc, first as a daemon and then as a library. The daemon's
// spool-and-cron transport imposed a ~10-minute latency floor and was dropped;
// what remained was an orchestration function whose failure modes reached
// callers as a []error to be matched on substrings, wrapping an extraction
// package whose metadata and weblink passes nothing here consumed. So it is
// inlined: this package now owns the whole pipeline, reports what went wrong
// with typed errors, and has exactly two calls into MuPDF instead of three (see
// extract.go), which is what makes isolating them in a child process tractable.
package pdf

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

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
//
// The contract callers depend on, unchanged from when this was blobproc:
//
//   - a non-nil error means we could not reach a conclusion about this PDF and
//     it is worth trying again;
//   - a nil error with empty GrobidXML is a conclusion -- nothing usable came
//     out of this PDF -- and GrobidOutcome says which kind;
//   - a nil error with non-empty GrobidXML is a success.
type Content struct {
	GrobidXML []byte
	PdfText   []byte
	// GrobidOutcome says why GrobidXML is empty.
	GrobidOutcome GrobidOutcome
	// GrobidRejection is the error code GROBID returned when it rejected the
	// PDF outright, e.g. "[NO_BLOCKS]". Set only with GrobidRejected.
	GrobidRejection string
}

// GrobidOutcome distinguishes the ways a PDF can come back without TEI.
//
// They look identical to a caller -- empty GrobidXML in every case -- and they
// mean very different things, which matters because callers remember these
// verdicts to avoid re-processing the PDF later. Only some of them are facts
// about the PDF; see Content.CacheableVerdict.
type GrobidOutcome string

const (
	// GrobidOK means TEI was produced.
	GrobidOK GrobidOutcome = ""
	// GrobidTimeout means GROBID never answered. A fact about the last few
	// seconds, not about the PDF.
	GrobidTimeout GrobidOutcome = "timeout"
	// GrobidRejected means GROBID read the PDF and says the PDF is at fault.
	GrobidRejected GrobidOutcome = "rejected"
	// GrobidTooLarge means we chose not to ask, because the PDF is over
	// grobid.max_filesize. A fact about our configuration, which can change.
	GrobidTooLarge GrobidOutcome = "too-large"
)

// CacheableVerdict reports whether the absence of TEI is a durable fact about
// this PDF -- one a caller may remember so it never pays to process these bytes
// again -- along with the reason to record.
//
// This judgement lives here rather than in the callers because it depends on
// what each outcome means, which is this package's knowledge. Getting it wrong
// in the permissive direction is the expensive mistake: remembering a timeout
// would blacklist good PDFs during a GROBID wobble, and remembering a size skip
// would strand them the day somebody raised the limit.
func (c Content) CacheableVerdict() (reason string, ok bool) {
	if len(c.GrobidXML) > 0 {
		return "", false
	}
	switch c.GrobidOutcome {
	case GrobidRejected:
		return "grobid rejected the pdf: " + c.GrobidRejection, true
	case GrobidOK:
		// A 200 with nothing in it. Odd, but it is GROBID's answer about these
		// bytes and it will be the same answer next time.
		return "grobid produced no xml", true
	default:
		return "", false
	}
}

// GrobidUnavailableError means GROBID could not be reached or did not complete.
// It is about the service, not the PDF, so it is worth retrying.
type GrobidUnavailableError struct{ Err error }

func (e *GrobidUnavailableError) Error() string {
	return fmt.Sprintf("grobid client failure: %v", e.Err)
}
func (e *GrobidUnavailableError) Unwrap() error { return e.Err }

// GrobidUnexpectedError means GROBID answered with something we do not
// recognise.
//
// This is the case that must not be mistaken for a bad PDF. GROBID returns 500
// both for a PDF it cannot parse and for its own internal trouble, so a status
// code alone cannot tell them apart; only the handful of codes in
// knownGrobidRejections are known to mean "this PDF". Anything else is treated
// as "GROBID may be having an outage", because guessing wrong in that direction
// writes off good PDFs in bulk, while guessing wrong in this direction just
// stops a run.
type GrobidUnexpectedError struct {
	StatusCode int
	Body       []byte
}

func (e *GrobidUnexpectedError) Error() string {
	return fmt.Sprintf("unknown grobid error (status %d): %q", e.StatusCode, e.Body)
}

// knownGrobidRejections are the bodies GROBID returns to say the PDF itself is
// the problem. Inherited from blobproc's caller-side list, whose comment noted:
// "there are almost certainly error codes that should be in here but we haven't
// seen them yet; it will be annoying to whack a mole them but that's better
// than treating PDFs as bad when grobid is just quietly having an outage."
var knownGrobidRejections = []string{
	"[BAD_INPUT_DATA]",
	"[NO_BLOCKS]",
	"[TOO_MANY_TOKENS]",
}

// Processor runs the PDF pipeline. Construct once with NewProcessor and reuse
// it across PDFs: the GROBID client and the S3 client it holds are safe for
// concurrent use, so Process may be called from multiple goroutines.
type Processor struct {
	grobid            *grobidclient.Grobid
	s3                *minio.Client
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
	// V4 signing, which is what trawler has always used against this store --
	// blobproc's own constructor used V2 with a note that seaweedfs might need
	// it, but trawler never went through that constructor.
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

	return newProcessor(mc, heartbeat), nil
}

// NewProcessorWithoutStore builds a Processor that computes derivatives without
// storing them, for one-off inspection such as `trawler pdf dump`.
func NewProcessorWithoutStore() *Processor {
	return newProcessor(nil, nil)
}

func newProcessor(mc *minio.Client, heartbeat func(string)) *Processor {
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
		s3:                mc,
		grobidMaxFileSize: maxSize,
		Heartbeater:       heartbeat,
	}
}

func (p *Processor) beatHeart(msg string) {
	if p.Heartbeater != nil {
		p.Heartbeater(msg)
	}
}

// Process runs the full per-PDF pipeline on pdfBs: MuPDF for text and a page-0
// thumbnail, then GROBID for TEI. Derivatives are written through to S3 and the
// TEI + text are returned in memory. sha1 is used only for logging; the
// canonical sha1 for derivative keys is derived from the bytes.
//
// Extraction failures are not fatal. GROBID is the output that matters, and a
// PDF MuPDF cannot read is often one GROBID can, so a failed text or thumbnail
// pass is logged and the run continues. See Content for what the error and the
// emptiness of GrobidXML mean together.
func (p *Processor) Process(ctx context.Context, pdfBs []byte, sha1 string) (Content, error) {
	var out Content

	// Keep the caller alive across the synchronous GROBID call.
	stop := p.heartbeatLoop(ctx)
	defer stop()

	digest := sha1Hex(pdfBs)
	log := slog.With("sha1", digest)

	switch {
	case !looksLikePDF(pdfBs):
		// Not fatal, and we still ask GROBID: it has its own opinion, and this
		// check is a smell test on the first kilobyte rather than validation.
		log.Warn("bytes do not look like a pdf; skipping extraction")
	case knownBadSHA1[digest]:
		// The whole point of the list is to keep these away from MuPDF.
		log.Warn("pdf is on the known-bad list; skipping extraction")
	default:
		p.extract(ctx, pdfBs, digest, log, &out)
	}

	return out, p.runGrobid(ctx, pdfBs, digest, log, &out)
}

// extract runs the MuPDF passes and stores what they produce. Every failure in
// here is logged rather than returned; see Process.
func (p *Processor) extract(ctx context.Context, pdfBs []byte, digest string, log *slog.Logger, out *Content) {
	p.beatHeart("pdf-text")
	text, err := extractText(ctx, pdfBs)
	switch {
	case err != nil:
		log.Warn("text extraction failed", "err", err)
	case len(text) == 0:
		// blobproc treated a zero-length extraction as a failed run ("empty-pdf")
		// and threw the thumbnail away with it. Here it is just an empty text
		// derivative that is not worth storing.
		log.Warn("pdf produced no text")
	default:
		out.PdfText = text
		if err := putDerivative(ctx, p.s3, textDerivative, digest, text); err != nil {
			log.Error("could not store text derivative", "err", err)
		}
	}

	p.beatHeart("pdf-thumbnail")
	thumb, err := extractThumbnail(ctx, pdfBs)
	switch {
	case err != nil:
		log.Warn("thumbnail extraction failed", "err", err)
	case len(thumb) < minThumbnailSize:
		log.Warn("thumbnail suspiciously small; discarding", "bytes", len(thumb))
	default:
		if err := putDerivative(ctx, p.s3, thumbnailDerivative, digest, thumb); err != nil {
			log.Error("could not store thumbnail derivative", "err", err)
		}
	}
}

// runGrobid asks GROBID for TEI and classifies what comes back. Its return is
// Process's return, so only a reason to try this PDF again is an error.
func (p *Processor) runGrobid(ctx context.Context, pdfBs []byte, digest string, log *slog.Logger, out *Content) error {
	if int64(len(pdfBs)) > p.grobidMaxFileSize {
		// Not an error and not a verdict on the PDF: we chose not to ask. The
		// caller sees empty TEI, which it treats as "nothing usable here".
		log.Warn("pdf too large for grobid; skipping", "bytes", len(pdfBs))
		out.GrobidOutcome = GrobidTooLarge
		return nil
	}

	// GROBID's client takes a path, so this is the one step that still needs the
	// bytes on disk -- the MuPDF passes above read them straight from memory.
	path, cleanup, err := tempPDF(pdfBs)
	if err != nil {
		return err
	}
	defer cleanup()

	p.beatHeart("grobid")
	res, err := p.grobid.ProcessPDFContext(ctx, path, "processFulltextDocument", &grobidclient.Options{
		GenerateIDs:            true,
		ConsolidateHeader:      true,
		ConsolidateCitations:   false, // "too expensive for now"
		IncludeRawCitations:    true,
		IncludeRawAffiliations: true,
		TEICoordinates:         []string{"ref", "figure", "persName", "formula", "biblStruct"},
		SegmentSentences:       true,
	})
	if err != nil {
		// A timeout is the one client failure that is not worth failing over:
		// GROBID is busy, not broken, and the PDF gets another pass later.
		if isTimeout(err) {
			log.Warn("grobid timed out", "err", err)
			out.GrobidOutcome = GrobidTimeout
			return nil
		}
		return &GrobidUnavailableError{Err: err}
	}
	if res.Err != nil {
		return &GrobidUnavailableError{Err: res.Err}
	}

	if res.StatusCode != 200 {
		if code, ok := grobidRejection(res.Body); ok {
			// GROBID read the PDF and says the PDF is the problem. A verdict,
			// not a failure.
			log.Warn("grobid rejected the pdf", "code", code)
			out.GrobidOutcome = GrobidRejected
			out.GrobidRejection = code
			return nil
		}
		return &GrobidUnexpectedError{StatusCode: res.StatusCode, Body: res.Body}
	}

	out.GrobidXML = res.Body
	if err := putDerivative(ctx, p.s3, teiDerivative, digest, res.Body); err != nil {
		// The TEI is already in hand and about to be indexed; losing the
		// rederivable copy is not worth discarding that.
		log.Error("could not store tei derivative", "err", err)
	}
	return nil
}

// grobidRejection reports whether a non-200 body is one of the codes that means
// the PDF is at fault rather than GROBID.
func grobidRejection(body []byte) (string, bool) {
	for _, code := range knownGrobidRejections {
		if strings.HasPrefix(string(body), code) {
			return code, true
		}
	}
	return "", false
}

// isTimeout reports whether an error is a request that ran out of time. It
// checks both the typed form and the string, because net/http's client timeout
// surfaces as an *url.Error wrapping a bare errors.New rather than anything
// implementing net.Error in every path.
func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var timeout interface{ Timeout() bool }
	if errors.As(err, &timeout) && timeout.Timeout() {
		return true
	}
	return strings.Contains(err.Error(), "Client.Timeout exceeded")
}

// tempPDF writes the bytes somewhere GROBID's client can open them.
func tempPDF(pdfBs []byte) (path string, cleanup func(), err error) {
	f, err := os.CreateTemp("", "trawler-pdf-*.pdf")
	if err != nil {
		return "", nil, fmt.Errorf("could not create temp file: %w", err)
	}
	cleanup = func() { os.Remove(f.Name()) }

	if _, err := f.Write(pdfBs); err != nil {
		f.Close()
		cleanup()
		return "", nil, fmt.Errorf("could not write temp pdf: %w", err)
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("could not close temp pdf: %w", err)
	}
	return f.Name(), cleanup, nil
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
