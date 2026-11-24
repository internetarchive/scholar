package etd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"git.archive.org/webgroup/scholar/trawler/fatcat2"
	"git.archive.org/webgroup/scholar/trawler/indexing"
	"git.archive.org/webgroup/scholar/trawler/pdf"
	"github.com/google/uuid"
	"github.com/spf13/viper"
)

// Indexer finishes a line: search docs for its release and files, and a
// fulltext doc built from GROBID output for its best PDF.
type Indexer interface {
	// Index returns a short note of what it did. An error means try again
	// later; a fulltext that cannot be had is a note, not an error.
	Index(ctx context.Context, job IndexJob) (string, error)
}

// pdfProcessor is the part of pdf.Processor the indexer uses.
type pdfProcessor interface {
	Process(ctx context.Context, pdfBs []byte, sha1 string) (pdf.Content, error)
}

type esIndexer struct {
	http      *http.Client
	fetch     Fetcher
	processor pdfProcessor
	// busyBackoff is the wait before each retry of a GROBID 503; its length
	// is the number of retries. See processWhenFree.
	busyBackoff []time.Duration
}

// defaultBusyBackoff spans about four minutes, a few long theses' worth of
// GROBID time, before a busy GROBID counts against the job.
var defaultBusyBackoff = []time.Duration{15 * time.Second, 30 * time.Second, 60 * time.Second, 120 * time.Second}

// processWhenFree runs the PDF through GROBID, waiting out 503s. GROBID
// answers 503 when every one of its processing slots is taken, which with the
// index pool sized to GROBID's capacity is ordinary contention rather than a
// failure, so it is retried here instead of spending one of the job's attempts.
func (x esIndexer) processWhenFree(ctx context.Context, bs []byte) (pdf.Content, error) {
	for i := 0; ; i++ {
		content, err := x.processor.Process(ctx, bs, "")
		var busy *pdf.GrobidUnexpectedError
		if !errors.As(err, &busy) || busy.StatusCode != http.StatusServiceUnavailable || i >= len(x.busyBackoff) {
			return content, err
		}
		if err := sleepCtx(ctx, x.busyBackoff[i]); err != nil {
			return content, err
		}
	}
}

func (x esIndexer) Index(ctx context.Context, job IndexJob) (string, error) {
	rid, err := uuid.Parse(job.ReleaseID)
	if err != nil {
		return "", fmt.Errorf("bad release id %q: %w", job.ReleaseID, err)
	}
	var notes []string

	// Release and file docs come first and from fc2 rather than from what the
	// metadata worker had in hand, so they reflect every write the line made.
	if err := indexing.IndexRelease(rid); err != nil {
		return "", fmt.Errorf("index release %s: %w", rid, err)
	}
	for _, s := range job.FileIDs {
		fid, err := uuid.Parse(s)
		if err != nil {
			return "", fmt.Errorf("bad file id %q: %w", s, err)
		}
		if err := indexing.IndexFile(fid); err != nil {
			return "", fmt.Errorf("index file %s: %w", fid, err)
		}
	}
	notes = append(notes, fmt.Sprintf("indexed release + %d files", len(job.FileIDs)))

	note, err := x.indexFulltext(ctx, rid, job)
	if err != nil {
		return "", err
	}
	return strings.Join(append(notes, note), "; "), nil
}

func (x esIndexer) indexFulltext(ctx context.Context, rid uuid.UUID, job IndexJob) (string, error) {
	fid, err := uuid.Parse(job.BestFileID)
	if err != nil {
		return "", fmt.Errorf("bad file id %q: %w", job.BestFileID, err)
	}

	bs, err := x.fetch.Bytes(ctx, job.BestFetchURL)
	if isPermanent(err) {
		return "no fulltext doc: pdf unavailable: " + err.Error(), nil
	}
	if err != nil {
		return "", fmt.Errorf("refetch %s: %w", job.BestFetchURL, err)
	}

	start := time.Now()
	content, err := x.processWhenFree(ctx, bs)
	took := fmt.Sprintf("pdf %d bytes, processing %s", len(bs), time.Since(start).Round(time.Second))
	if err != nil {
		return "", fmt.Errorf("pdf processing for file %s (%s): %w", fid, took, err)
	}
	if len(content.GrobidXML) == 0 {
		if content.GrobidOutcome == pdf.GrobidTimeout {
			// About GROBID's afternoon, not the PDF: worth another go.
			return "", fmt.Errorf("grobid timed out on file %s (%s)", fid, took)
		}
		if len(content.PdfText) == 0 {
			return fmt.Sprintf("no fulltext doc: no tei (%s) and no text", content.GrobidOutcome), nil
		}
	}

	release, err := fatcat2.GetRelease(x.http, rid)
	if err != nil {
		return "", fmt.Errorf("get release %s: %w", rid, err)
	}
	file, err := fatcat2.GetFile(x.http, fid)
	if err != nil {
		return "", fmt.Errorf("get file %s: %w", fid, err)
	}
	var container *fatcat2.Container
	if release.ContainerID != nil {
		c, err := fatcat2.GetContainer(x.http, *release.ContainerID)
		if err != nil {
			return "", fmt.Errorf("get container %s: %w", release.ContainerID, err)
		}
		container = &c
	}

	doc := indexing.PrepareFulltextDoc(indexing.FulltextTransformCtx{
		HttpClient: x.http,
		Release:    release,
		Container:  container,
		File:       &file,
		GrobidXML:  content.GrobidXML,
		PdfText:    content.PdfText,
	})
	out, err := json.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("fulltext doc marshal: %w", err)
	}
	if err := indexing.DoElasticIndex(x.http, viper.GetString("indexing.fulltext_ix"), doc.Key, out); err != nil {
		return "", fmt.Errorf("index fulltext for %s: %w", rid, err)
	}
	if len(content.GrobidXML) == 0 {
		return fmt.Sprintf("indexed fulltext from text only (grobid: %s; %s)", content.GrobidOutcome, took), nil
	}
	return "indexed fulltext (" + took + ")", nil
}
