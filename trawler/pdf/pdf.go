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

// Process submits pdfBs to blobproc, polls until processing completes, then
// fetches and returns the grobid XML and pdftotext output from S3. sha1 is the
// SHA-1 hex digest of pdfBs, used to verify the spool URL and construct S3
// keys. Reads blobproc.endpoint, blobproc.s3bucket, and blobproc.poll_interval
// from viper config.
func Process(ctx context.Context, client *http.Client, pdfBs []byte, sha1 string) (Content, error) {
	endpoint := viper.GetString("blobproc.endpoint")

	req, err := http.NewRequest("POST", endpoint+"/spool", bytes.NewBuffer(pdfBs))
	if err != nil {
		return Content{}, fmt.Errorf("could not form blobproc request: %w", err)
	}
	req.Header.Set("Content-Type", "application/pdf")

	resp, err := client.Do(req)
	if err != nil {
		return Content{}, fmt.Errorf("blobproc request error: %w", err)
	}
	if resp.StatusCode != 202 {
		return Content{}, fmt.Errorf("unexpected status from blobproc '%d'", resp.StatusCode)
	}

	loc := resp.Header.Get("Location")
	if loc == "" {
		return Content{}, fmt.Errorf("got blank spool url from blobproc")
	}

	pu, err := url.Parse(loc)
	if err != nil {
		return Content{}, fmt.Errorf("could not parse blobproc spool url %q: %w", loc, err)
	}

	pollURL := endpoint + pu.Path
	if !strings.Contains(pollURL, sha1) {
		return Content{}, fmt.Errorf("expected sha1 %q in spool url %q", sha1, pollURL)
	}

	pollReq, err := http.NewRequest("GET", pollURL, nil)
	if err != nil {
		return Content{}, fmt.Errorf("could not form blobproc poll request: %w", err)
	}

	for {
		time.Sleep(viper.GetDuration("blobproc.poll_interval"))
		resp, err = client.Do(pollReq)
		if err != nil {
			return Content{}, fmt.Errorf("error polling blobproc: %w", err)
		}
		if resp.StatusCode == 404 {
			break
		}
	}

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

	return Content{
		GrobidXML: grobidXML,
		PdfText:   pdfText,
	}, nil
}
