// pdf handles submission of PDF bytes to blobproc, polling for completion,
// and retrieval of the resulting grobid XML and pdftotext output from S3.
package pdf

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"git.archive.org/webgroup/scholar/trawler/s3"
	"github.com/spf13/viper"
)

// Content holds the outputs produced by blobproc for a single PDF.
type Content struct {
	GrobidXML []byte
	PdfText   []byte
}

type Processor struct {
	Client      *http.Client
	Heartbeater func(string)
}

func (p Processor) beatHeart(msg string) {
	if p.Heartbeater != nil {
		p.Heartbeater(msg)
	}
}

// Submit POSTs pdfBs to blobproc's spool endpoint and returns the poll URL
// blobproc will report completion on. sha1 is the SHA-1 hex digest of pdfBs,
// used to sanity-check the returned spool URL.
func (p *Processor) Submit(ctx context.Context, pdfBs []byte, sha1 string) (string, error) {
	endpoint := viper.GetString("blobproc.endpoint")

	req, err := http.NewRequestWithContext(ctx, "POST", endpoint+"/spool", bytes.NewBuffer(pdfBs))
	if err != nil {
		return "", fmt.Errorf("could not form blobproc request: %w", err)
	}
	req.Header.Set("Content-Type", "application/pdf")

	resp, err := p.Client.Do(req)
	if err != nil {
		return "", fmt.Errorf("blobproc request error: %w", err)
	}
	if resp.StatusCode != 202 {
		return "", fmt.Errorf("unexpected status from blobproc '%d'", resp.StatusCode)
	}
	p.beatHeart("blobproc-submitted")

	loc := resp.Header.Get("Location")
	if loc == "" {
		return "", fmt.Errorf("got blank spool url from blobproc")
	}

	pu, err := url.Parse(loc)
	if err != nil {
		return "", fmt.Errorf("could not parse blobproc spool url %q: %w", loc, err)
	}

	pollURL := endpoint + pu.Path
	if !strings.Contains(pollURL, sha1) {
		return "", fmt.Errorf("expected sha1 %q in spool url %q", sha1, pollURL)
	}
	return pollURL, nil
}

// Poll waits for blobproc to finish processing the blob at pollURL (signalled
// by a 404 response).
func (p *Processor) Poll(ctx context.Context, pollURL string) error {
	pollReq, err := http.NewRequestWithContext(ctx, "GET", pollURL, nil)
	if err != nil {
		return fmt.Errorf("could not form blobproc poll request: %w", err)
	}
	interval := viper.GetDuration("blobproc.poll_interval")
	for {
		p.beatHeart("blobproc-poll")
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
		resp, err := p.Client.Do(pollReq)
		if err != nil {
			return fmt.Errorf("error polling blobproc: %w", err)
		}
		if resp.StatusCode == 404 {
			return nil
		}
	}
}

// Fetch retrieves the grobid XML and pdftotext output that blobproc wrote to
// S3 for the given sha1.
func (p *Processor) Fetch(ctx context.Context, sha1 string) (Content, error) {
	s3bucket := viper.GetString("blobproc.s3bucket")

	grobidKey := fmt.Sprintf("%s/grobid/%s/%s/%s.tei.xml", s3bucket, sha1[0:2], sha1[2:4], sha1)
	obj, err := s3.GetObject(ctx, grobidKey)
	if err != nil {
		return Content{}, fmt.Errorf("blobproc grobid s3 read failed: %w", err)
	}
	grobidXML, err := io.ReadAll(obj)
	if err != nil {
		return Content{}, fmt.Errorf("could not read grobid output: %w", err)
	}

	pdftotextKey := fmt.Sprintf("%s/text/%s/%s/%s.txt", s3bucket, sha1[0:2], sha1[2:4], sha1)
	obj, err = s3.GetObject(ctx, pdftotextKey)
	if err != nil {
		return Content{}, fmt.Errorf("blobproc pdftotext s3 read failed: %w", err)
	}
	pdfText, err := io.ReadAll(obj)
	if err != nil {
		return Content{}, fmt.Errorf("could not read pdftotext output: %w", err)
	}

	return Content{GrobidXML: grobidXML, PdfText: pdfText}, nil
}

// Process is a convenience: Submit → Poll → Fetch.
func (p *Processor) Process(ctx context.Context, pdfBs []byte, sha1 string) (Content, error) {
	pollURL, err := p.Submit(ctx, pdfBs, sha1)
	if err != nil {
		return Content{}, err
	}
	if err := p.Poll(ctx, pollURL); err != nil {
		return Content{}, err
	}
	return p.Fetch(ctx, sha1)
}
