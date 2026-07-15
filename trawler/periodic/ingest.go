package periodic

/*
This package is like `daily` but for the ingestion of PDFs from warcs on
petabox instead of the live web. We complement the daily crawling with periodic
heritrix (or whatever) based wide crawls in the hope of capturing PDFs.

The entry workflow is PeriodicIngestWorkflow. It pages through a crawl collection on petabox, pulls CDX files, then reads and processes any PDFs within.
*/

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base32"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"git.archive.org/webgroup/scholar/trawler/cdx/cdxfile"
	"git.archive.org/webgroup/scholar/trawler/fatcat2"
	"git.archive.org/webgroup/scholar/trawler/ia"
	"git.archive.org/webgroup/scholar/trawler/pdf"
	warc "github.com/internetarchive/gowarc"
	"github.com/miku/grobidclient/tei"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/workflow"
)

const (
	// TODO config?
	itemsPerPage = 100
	taskQueue    = "periodic_ingest"
)

// NEW BESPOKE HERE

type PeriodicCounts struct {
	PdfLines     int
	PdfsWanted   int
	PdfsAcquired int
}

type PeriodicIngestInput struct {
	// petabox collection of warcs/cdx
	CollectionName string
	// limit how many items are looked at; for debugging
	Limit int
}

func PeriodicIngestWorkflow(ctx workflow.Context, in PeriodicIngestInput) (PeriodicCounts, error) {
	out := PeriodicCounts{}
	// l := workflow.GetLogger(ctx)

	ao := workflow.ActivityOptions{
		StartToCloseTimeout: 20 * time.Minute,
		TaskQueue:           taskQueue,
	}
	var listOut ListCollectionOutput
	err := workflow.ExecuteActivity(
		workflow.WithActivityOptions(ctx, ao),
		ListCollectionActivity,
		ListCollectionInput{CollectionName: in.CollectionName}).Get(ctx, &listOut)
	if err != nil {
		return out, err
	}

	ao = workflow.ActivityOptions{
		StartToCloseTimeout: 4 * time.Hour, // TODO may want to tweak later
		TaskQueue:           taskQueue,
	}
	ctx = workflow.WithActivityOptions(ctx, ao)
	var processOut PeriodicCounts
	for _, itemId := range listOut.ItemIds {

		err := workflow.ExecuteActivity(
			ctx, ProcessCrawlItemActivity,
			ProcessCrawlItemInput{ItemId: itemId}).Get(ctx, &processOut)
		if err != nil {
			return out, err
		}
		out.PdfLines += processOut.PdfLines
		out.PdfsWanted += processOut.PdfsWanted
		out.PdfsAcquired += processOut.PdfsAcquired
	}

	return out, nil
}

type ListCollectionInput struct {
	CollectionName string
}

type ListCollectionOutput struct {
	ItemIds []string
}

func ListCollectionActivity(ctx context.Context, in ListCollectionInput) (ListCollectionOutput, error) {
	out := ListCollectionOutput{
		ItemIds: []string{},
	}
	client := &http.Client{Timeout: 120 * time.Second}
	page := 1

	for true {
		ids, hasMore, err := ia.SearchCollection(
			ctx, client, in.CollectionName, page, itemsPerPage)
		if err != nil {
			return out, fmt.Errorf("failed to search '%s': %w", in.CollectionName, err)
		}

		for _, id := range ids {
			if !strings.HasSuffix(id, "-CRL") {
				out.ItemIds = append(out.ItemIds, id)
			}
		}

		if !hasMore {
			break
		}

		page += 1
	}

	return out, nil
}

type ProcessCrawlItemInput struct {
	ItemId string
}

func ProcessCrawlItemActivity(ctx context.Context, in ProcessCrawlItemInput) (PeriodicCounts, error) {
	out := PeriodicCounts{}
	client := &http.Client{Timeout: 10 * time.Minute}
	l := activity.GetLogger(ctx)

	activity.RecordHeartbeat(ctx, "metadata")
	files, err := ia.ItemFiles(ctx, client, in.ItemId)
	if err != nil {
		return out, fmt.Errorf("metadata for %s: %w", in.ItemId, err)
	}
	rollup, err := ia.FindRollupCDX(files)
	if err != nil {
		return out, fmt.Errorf("find rollup CDX for %s: %w", in.ItemId, err)
	}

	activity.RecordHeartbeat(ctx, "download-cdx")
	rdr, err := ia.OpenFile(ctx, client, in.ItemId, rollup)
	if err != nil {
		return out, fmt.Errorf("open rollup CDX %s/%s: %w", in.ItemId, rollup, err)
	}
	defer rdr.Close()

	activity.RecordHeartbeat(ctx, "parse-cdx")
	pdfLines, err := cdxfile.Parse(rdr, cdxfile.PDFFilter)
	if err != nil {
		return out, fmt.Errorf("parse rollup CDX %s/%s: %w", in.ItemId, rollup, err)
	}

	out.PdfLines = len(pdfLines)

	processor, err := pdf.NewProcessor(func(msg string) {
		activity.RecordHeartbeat(ctx, msg)
	})

	for _, pdfLine := range pdfLines {
		warcItem, warcFile := pdfLine.WARCItemAndFile()
		if warcItem == "" {
			warcItem = in.ItemId
		}
		sha1, err := decodeSha1Base32(pdfLine.Sha1Base32)
		if err != nil {
			l.Warn("skipping row with bad sha1", "sha1_b32", pdfLine.Sha1Base32, "err", err.Error())
			continue
		}

		activity.RecordHeartbeat(ctx, "sha1-lookup")
		fid, err := fatcat2.LookupSha1(client, sha1)
		if err != nil {
			return out, fmt.Errorf("could not look up sha1 in fc2: %w", err)
		}

		if fid != nil {
			activity.RecordHeartbeat(ctx, "file-lookup")
			file, err := fatcat2.GetFile(client, *fid)
			if err != nil {
				return out, fmt.Errorf("could not get file '%s' from fc2: '%w'", fid, err)
			}
			if len(file.URLs) > 0 {
				l.Info("skipping known file", "sha1", sha1, "fid", fid)
			}
		}

		out.PdfsWanted++

		activity.RecordHeartbeat(ctx, "range-read")
		raw, err := ia.ReadRange(ctx, client,
			warcItem, warcFile, pdfLine.WarcOffset, pdfLine.WarcSize)
		if err != nil {
			return out, fmt.Errorf("range read failed: %w", err)
		}

		activity.RecordHeartbeat(ctx, "pdf-extract")
		pdfBs, err := extractWARCPayload(raw)
		if err != nil {
			return out, fmt.Errorf("could not extract pdf bytes from warc: %w", err)
		}

		activity.RecordHeartbeat(ctx, "pdf-process")
		pdfContent, err := processor.Process(ctx, pdfBs, sha1)
		if err != nil {
			return out, fmt.Errorf("pdf processing failed: %w", err)
		}

		// TODO create fc2 entities
		// TODO ES ingest
	}

	return out, nil
}

type GrobidParseError struct {
	Sha1 string
	Err  error
}

func (e *GrobidParseError) Error() string {
	return fmt.Sprintf("grobid parsing failure for '%s': %v", e.Sha1, e.Err)
}

func (e *GrobidParseError) Unwrap() error { return e.Err }

// processResult is a stub for downstream handling of blobproc results.
// TODO: implement real downstream handling (indexing into ES, attaching the
// file to fatcat, etc).
func processResult(ctx context.Context, sha1hex string, content pdf.Content) error {
	gdoc, err := tei.ParseDocument(bytes.NewReader(content.GrobidXML))
	if err != nil {
		return &GrobidParseError{Sha1: sha1hex, Err: err}
	}

	// TODO support non-DOI ext ids
	if gdoc.Header.DOI == "" {
		return nil
	}

	fmt.Printf("DBG %#v\n", gdoc)
	slog.Info("processed blobproc result",
		"sha1", sha1hex,
		"grobid_xml_len", len(content.GrobidXML),
		"pdf_text_len", len(content.PdfText))
	return nil
}

// extractWARCPayload reads one WARC record (gzipped) out of raw bytes and
// returns the HTTP response body bytes. Expects exactly one record per
// input slice (matches the Range-GET shape we use against petabox).
func extractWARCPayload(raw []byte) ([]byte, error) {
	rdr, err := warc.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("warc reader init: %w", err)
	}
	defer rdr.Close()

	rec, err := rdr.ReadRecord()
	if err != nil {
		return nil, fmt.Errorf("warc ReadRecord: %w", err)
	}
	defer rec.Content.Close()

	httpResp, err := http.ReadResponse(bufio.NewReader(rec.Content), nil)
	if err != nil {
		return nil, fmt.Errorf("parse HTTP response inside WARC: %w", err)
	}
	defer httpResp.Body.Close()

	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("read HTTP body: %w", err)
	}
	return body, nil
}

// decodeSha1Base32 converts the CDX-formatted 32-char base32 sha1 digest to
// fatcat's 40-char lowercase hex form.
func decodeSha1Base32(s string) (string, error) {
	bs, err := base32.StdEncoding.DecodeString(strings.ToUpper(s))
	if err != nil {
		return "", err
	}
	if len(bs) != 20 {
		return "", fmt.Errorf("expected 20 sha1 bytes, got %d", len(bs))
	}
	return hex.EncodeToString(bs), nil
}
