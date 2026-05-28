package periodic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"git.archive.org/webgroup/scholar/trawler/cdx/cdxfile"
	"git.archive.org/webgroup/scholar/trawler/counts"
	"git.archive.org/webgroup/scholar/trawler/fatcat2"
	"git.archive.org/webgroup/scholar/trawler/ia"
	"git.archive.org/webgroup/scholar/trawler/pdf"
	warc "github.com/internetarchive/gowarc"
	"github.com/miku/grobidclient/tei"
	"github.com/spf13/viper"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// stageConcurrency caps in-flight HTTP work per stage inside
// ProcessItemActivity. Tuned for "fast enough that blobproc sees the whole
// batch in one 10-minute cycle" without overwhelming fatcat / petabox.
const stageConcurrency = 8

type IngestCollectionInput struct {
	CollectionID string
	// Limit caps the number of items selected from the collection in this
	// run. 0 means no limit. Useful for smoke tests against a real
	// collection without committing to processing the whole thing.
	Limit int
}

type IngestItemBatchInput struct {
	Identifiers []string
}

type ProcessItemInput struct {
	Identifier string
	Rows       []cdxfile.Row
}

type ListItemsInput struct {
	CollectionID string
	Page         int
	PageSize     int
}

type ListItemsOutput struct {
	Identifiers []string
	HasMore     bool
}

// IngestCollectionWorkflow is the parent: pages the IA advancedsearch API
// for items in the named collection, batches identifiers into chunks of
// periodic_ingest.items_per_child, and dispatches one IngestItemBatchWorkflow
// child per chunk.
func IngestCollectionWorkflow(ctx workflow.Context, in IngestCollectionInput) (counts.Counts, error) {
	out := counts.Counts{}
	l := workflow.GetLogger(ctx)

	itemsPerChild := viper.GetInt("periodic_ingest.items_per_child")
	if itemsPerChild <= 0 {
		itemsPerChild = 4
	}
	pageSize := viper.GetInt("periodic_ingest.collection_page_size")
	if pageSize <= 0 {
		pageSize = 100
	}

	ao := workflow.ActivityOptions{
		StartToCloseTimeout: 10 * time.Minute,
		TaskQueue:           viper.GetString("periodic_ingest.task_queue"),
	}
	ctx = workflow.WithActivityOptions(ctx, ao)

	childWfOpts := workflow.ChildWorkflowOptions{
		ParentClosePolicy: enums.PARENT_CLOSE_POLICY_ABANDON,
	}

	childSelector := workflow.NewSelector(ctx)
	var childCount int
	var childErr error
	var childCounts counts.Counts

	dispatch := func(ids []string) error {
		cctx := workflow.WithChildOptions(ctx, childWfOpts)
		fut := workflow.ExecuteChildWorkflow(cctx, IngestItemBatchWorkflow, IngestItemBatchInput{Identifiers: ids})
		var cwe workflow.Execution
		if err := fut.GetChildWorkflowExecution().Get(ctx, &cwe); err != nil {
			return err
		}
		childSelector.AddFuture(fut, func(f workflow.Future) {
			childErr = f.Get(ctx, &childCounts)
		})
		childCount++
		return nil
	}

	page := 1
	collected := 0
	var pending []string
	for {
		var listOut ListItemsOutput
		err := workflow.ExecuteActivity(ctx, ListCollectionItemsActivity, ListItemsInput{
			CollectionID: in.CollectionID,
			Page:         page,
			PageSize:     pageSize,
		}).Get(ctx, &listOut)
		if err != nil {
			return out, err
		}
		for _, id := range listOut.Identifiers {
			if isSkippableItem(id) {
				l.Info("skipping item", "identifier", id)
				continue
			}
			if in.Limit > 0 && collected >= in.Limit {
				break
			}
			pending = append(pending, id)
			collected++
		}
		for len(pending) >= itemsPerChild {
			chunk := pending[:itemsPerChild]
			pending = pending[itemsPerChild:]
			if err := dispatch(chunk); err != nil {
				return out, err
			}
		}
		if in.Limit > 0 && collected >= in.Limit {
			break
		}
		if !listOut.HasMore {
			break
		}
		page++
	}
	if len(pending) > 0 {
		if err := dispatch(pending); err != nil {
			return out, err
		}
	}

	for range childCount {
		childSelector.Select(ctx)
		if childErr != nil {
			return out, childErr
		}
		out = out.Add(childCounts)
	}

	l.Info(fmt.Sprintf("collection ingest done for %s: %#v", in.CollectionID, out))
	return out, nil
}

// IngestItemBatchWorkflow handles a small group of items, one at a time:
// fetch the rollup CDX, then run a single per-item activity that processes
// the whole item internally (aligning with blobproc's 10-minute spool cycle).
func IngestItemBatchWorkflow(ctx workflow.Context, in IngestItemBatchInput) (counts.Counts, error) {
	out := counts.Counts{}

	ao := workflow.ActivityOptions{
		StartToCloseTimeout:    2 * time.Hour,
		ScheduleToCloseTimeout: 6 * time.Hour,
		HeartbeatTimeout:       3 * time.Minute,
		TaskQueue:              viper.GetString("periodic_ingest.task_queue"),
		RetryPolicy: &temporal.RetryPolicy{
			BackoffCoefficient: 1.5,
			InitialInterval:    30 * time.Second,
			MaximumInterval:    90 * time.Second,
		},
	}
	ctx = workflow.WithActivityOptions(ctx, ao)

	for _, identifier := range in.Identifiers {
		var rows []cdxfile.Row
		if err := workflow.ExecuteActivity(ctx, FetchItemCDXActivity, identifier).Get(ctx, &rows); err != nil {
			return out, err
		}
		var itemCounts counts.Counts
		if err := workflow.ExecuteActivity(ctx, ProcessItemActivity, ProcessItemInput{
			Identifier: identifier,
			Rows:       rows,
		}).Get(ctx, &itemCounts); err != nil {
			return out, err
		}
		out = out.Add(itemCounts)
	}
	return out, nil
}

// ListCollectionItemsActivity wraps ia.SearchCollection so the parent
// workflow can page through a collection.
func ListCollectionItemsActivity(ctx context.Context, in ListItemsInput) (ListItemsOutput, error) {
	client := &http.Client{Timeout: 60 * time.Second}
	ids, hasMore, err := ia.SearchCollection(ctx, client, in.CollectionID, in.Page, in.PageSize)
	if err != nil {
		return ListItemsOutput{}, err
	}
	return ListItemsOutput{Identifiers: ids, HasMore: hasMore}, nil
}

// FetchItemCDXActivity locates an item's rollup CDX, downloads it, and
// returns parsed PDF rows. Filtering inside the activity keeps the returned
// payload proportional to the PDF count rather than the total record count.
func FetchItemCDXActivity(ctx context.Context, identifier string) ([]cdxfile.Row, error) {
	// Timeout is per-request; the rollup CDX can be ~100MB so allow plenty.
	client := &http.Client{Timeout: 10 * time.Minute}

	activity.RecordHeartbeat(ctx, "metadata")
	files, err := ia.ItemFiles(ctx, client, identifier)
	if err != nil {
		return nil, fmt.Errorf("metadata for %s: %w", identifier, err)
	}
	rollup, err := ia.FindRollupCDX(files)
	if err != nil {
		return nil, fmt.Errorf("find rollup CDX for %s: %w", identifier, err)
	}

	activity.RecordHeartbeat(ctx, "download-cdx")
	rdr, err := ia.OpenFile(ctx, client, identifier, rollup)
	if err != nil {
		return nil, fmt.Errorf("open rollup CDX %s/%s: %w", identifier, rollup, err)
	}
	defer rdr.Close()

	activity.RecordHeartbeat(ctx, "parse-cdx")
	stopHB := heartbeat(ctx, "parse-cdx-progress")
	rows, err := cdxfile.Parse(rdr, cdxfile.PDFFilter)
	stopHB()
	if err != nil {
		return nil, fmt.Errorf("parse rollup CDX %s/%s: %w", identifier, rollup, err)
	}
	return rows, nil
}

// taskState carries one row through the per-item pipeline. Fields populate
// as the row progresses through stages.
type taskState struct {
	row      cdxfile.Row
	sha1hex  string
	warcItem string
	warcFile string
	pdfBytes []byte // populated after stage 2; freed after stage 3
	pollURL  string // populated after stage 3
}

// ProcessItemActivity is the per-item leaf. It runs five internal stages
// over a single item's PDF rows:
//
//  1. parallel fatcat probes (skip what's already known + indexed)
//  2. parallel petabox Range-GET + WARC payload extract
//  3. parallel POSTs to blobproc /spool (drops PDF bytes after each)
//  4. parallel polls until each blob is processed
//  5. fetch GROBID + pdftotext from S3 and run processResult
//
// Aligning all stage 3 POSTs inside one blobproc spool cycle is the point of
// this restructure — blobproc processes its spool in ~10-minute batches, so
// one item's PDFs share a single cycle instead of paying N×10min serially.
func ProcessItemActivity(ctx context.Context, in ProcessItemInput) (counts.Counts, error) {
	l := activity.GetLogger(ctx)
	out := counts.Counts{}
	// Per-request timeout bounds hangs from stuck TCP reads (DNS blips,
	// blobproc/fatcat hiccups, slow petabox reads). Has to be larger than
	// any single legitimate request: blobproc POSTs for big PDFs are the
	// slowest, but should still finish well under 5 minutes.
	client := &http.Client{Timeout: 5 * time.Minute}
	processor := pdf.Processor{
		Client: client,
		Heartbeater: func(msg string) {
			activity.RecordHeartbeat(ctx, msg)
		},
	}

	// Stage 0: decode + validate rows into task states.
	tasks := make([]*taskState, 0, len(in.Rows))
	for _, row := range in.Rows {
		warcItem, warcFile := row.WARCItemAndFile()
		if warcItem == "" {
			warcItem = in.Identifier
		}
		sha1hex, err := decodeSha1Base32(row.Sha1Base32)
		if err != nil {
			l.Warn("skipping row with bad sha1", "sha1_b32", row.Sha1Base32, "err", err.Error())
			out.Pdfs.Failed++
			continue
		}
		tasks = append(tasks, &taskState{row: row, sha1hex: sha1hex, warcItem: warcItem, warcFile: warcFile})
	}
	if len(tasks) == 0 {
		return out, nil
	}

	// Stage 1: parallel fatcat probes.
	activity.RecordHeartbeat(ctx, "stage-1-fatcat-probes")
	stopHB := heartbeat(ctx, "stage-1-progress")
	tasks, skipped, err := filterKnown(ctx, client, tasks)
	stopHB()
	if err != nil {
		return out, fmt.Errorf("fatcat probe: %w", err)
	}
	out.Pdfs.Skipped += skipped
	l.Info("fatcat probe complete", "item", in.Identifier, "skipped", skipped, "remaining", len(tasks))
	if len(tasks) == 0 {
		return out, nil
	}

	// Stage 2: parallel petabox fetch + WARC extract.
	activity.RecordHeartbeat(ctx, "stage-2-fetch-warc")
	stopHB = heartbeat(ctx, "stage-2-progress")
	tasks, failed := fetchAndExtractWARC(ctx, client, tasks)
	stopHB()
	out.Pdfs.Failed += failed
	l.Info("warc fetch complete", "item", in.Identifier, "ready_to_submit", len(tasks), "fetch_failures", failed)
	if len(tasks) == 0 {
		return out, nil
	}

	// Stage 3: parallel submits. Each task drops its PDF bytes after a
	// successful POST so memory stays bounded to ~stageConcurrency PDFs.
	activity.RecordHeartbeat(ctx, "stage-3-blobproc-submit")
	stopHB = heartbeat(ctx, "stage-3-progress")
	tasks, failed = submitToBlobproc(ctx, &processor, tasks)
	stopHB()
	out.Pdfs.Failed += failed
	l.Info("blobproc submission complete", "item", in.Identifier, "submitted", len(tasks), "submit_failures", failed)
	if len(tasks) == 0 {
		return out, nil
	}

	// Stage 4: parallel polls.
	activity.RecordHeartbeat(ctx, "stage-4-blobproc-poll")
	stopHB = heartbeat(ctx, "stage-4-progress")
	tasks, failed = pollBlobproc(ctx, &processor, tasks)
	stopHB()
	out.Pdfs.Failed += failed
	l.Info("blobproc poll complete", "item", in.Identifier, "completed", len(tasks), "poll_failures", failed)

	// Stage 5: fetch results + processResult per blob.
	activity.RecordHeartbeat(ctx, "stage-5-fetch-and-process")
	stopHB = heartbeat(ctx, "stage-5-progress")
	processed, failedFinal := fetchAndRunProcessResult(ctx, &processor, tasks)
	stopHB()
	out.Pdfs.Processed += processed
	out.Pdfs.Failed += failedFinal
	l.Info("processed item",
		"item", in.Identifier,
		"processed", processed,
		"failed", failedFinal,
		"skipped", out.Pdfs.Skipped)

	return out, nil
}

// filterKnown probes fatcat in parallel and returns the tasks whose sha1
// isn't yet attached to a file with URLs (the keep set), plus the count of
// already-indexed tasks.
func filterKnown(ctx context.Context, client *http.Client, tasks []*taskState) (kept []*taskState, skipped int, err error) {
	knownMap := make(map[string]bool, len(tasks))
	var (
		mu       sync.Mutex
		probeErr error
		wg       sync.WaitGroup
		sem      = make(chan struct{}, stageConcurrency)
	)
	for _, t := range tasks {
		select {
		case <-ctx.Done():
			return nil, 0, ctx.Err()
		case sem <- struct{}{}:
		}
		wg.Add(1)
		go func(t *taskState) {
			defer wg.Done()
			defer func() { <-sem }()
			fid, err := fatcat2.LookupSha1(client, t.sha1hex)
			if err != nil {
				mu.Lock()
				if probeErr == nil {
					probeErr = fmt.Errorf("LookupSha1 %s: %w", t.sha1hex, err)
				}
				mu.Unlock()
				return
			}
			if fid == nil {
				return
			}
			file, err := fatcat2.GetFile(client, *fid)
			if err != nil {
				mu.Lock()
				if probeErr == nil {
					probeErr = fmt.Errorf("GetFile %s: %w", t.sha1hex, err)
				}
				mu.Unlock()
				return
			}
			if len(file.URLs) > 0 {
				mu.Lock()
				knownMap[t.sha1hex] = true
				mu.Unlock()
			}
		}(t)
	}
	wg.Wait()
	if probeErr != nil {
		return nil, 0, probeErr
	}
	for _, t := range tasks {
		if knownMap[t.sha1hex] {
			skipped++
			continue
		}
		kept = append(kept, t)
	}
	return kept, skipped, nil
}

// fetchAndExtractWARC reads each task's WARC record from petabox in parallel
// and extracts the HTTP response payload into task.pdfBytes. Per-task
// failures are skipped (returned as count) rather than failing the activity.
func fetchAndExtractWARC(ctx context.Context, client *http.Client, tasks []*taskState) (kept []*taskState, failed int) {
	type outcome struct {
		t  *taskState
		ok bool
	}
	results := make([]outcome, len(tasks))
	var wg sync.WaitGroup
	sem := make(chan struct{}, stageConcurrency)
	for i, t := range tasks {
		select {
		case <-ctx.Done():
			return nil, failed
		case sem <- struct{}{}:
		}
		wg.Add(1)
		go func(i int, t *taskState) {
			defer wg.Done()
			defer func() { <-sem }()
			raw, err := ia.ReadRange(ctx, client, t.warcItem, t.warcFile, t.row.WarcOffset, t.row.WarcSize)
			if err != nil {
				slog.Warn("petabox range read failed", "sha1", t.sha1hex, "err", err.Error())
				results[i] = outcome{t: t, ok: false}
				return
			}
			pdfBs, err := extractWARCPayload(raw)
			if err != nil {
				slog.Warn("warc extract failed", "sha1", t.sha1hex, "err", err.Error())
				results[i] = outcome{t: t, ok: false}
				return
			}
			t.pdfBytes = pdfBs
			results[i] = outcome{t: t, ok: true}
		}(i, t)
	}
	wg.Wait()
	for _, r := range results {
		if r.t == nil {
			continue
		}
		if !r.ok {
			failed++
			continue
		}
		kept = append(kept, r.t)
	}
	return kept, failed
}

// submitToBlobproc POSTs each task's bytes to blobproc /spool in parallel.
// On success task.pollURL is set and task.pdfBytes is freed. Per-task
// submit failures are skipped (returned as count).
func submitToBlobproc(ctx context.Context, processor *pdf.Processor, tasks []*taskState) (kept []*taskState, failed int) {
	type outcome struct {
		t  *taskState
		ok bool
	}
	results := make([]outcome, len(tasks))
	var wg sync.WaitGroup
	sem := make(chan struct{}, stageConcurrency)
	for i, t := range tasks {
		select {
		case <-ctx.Done():
			return nil, failed
		case sem <- struct{}{}:
		}
		wg.Add(1)
		go func(i int, t *taskState) {
			defer wg.Done()
			defer func() { <-sem }()
			pollURL, err := processor.Submit(ctx, t.pdfBytes, t.sha1hex)
			t.pdfBytes = nil
			if err != nil {
				slog.Warn("blobproc submit failed", "sha1", t.sha1hex, "err", err.Error())
				results[i] = outcome{t: t, ok: false}
				return
			}
			t.pollURL = pollURL
			results[i] = outcome{t: t, ok: true}
		}(i, t)
	}
	wg.Wait()
	for _, r := range results {
		if r.t == nil {
			continue
		}
		if !r.ok {
			failed++
			continue
		}
		kept = append(kept, r.t)
	}
	return kept, failed
}

// pollBlobproc polls every task's blobproc spool URL in parallel until each
// returns 404. Per-task poll failures are skipped (returned as count).
func pollBlobproc(ctx context.Context, processor *pdf.Processor, tasks []*taskState) (kept []*taskState, failed int) {
	type outcome struct {
		t  *taskState
		ok bool
	}
	results := make([]outcome, len(tasks))
	var wg sync.WaitGroup
	sem := make(chan struct{}, stageConcurrency)
	for i, t := range tasks {
		select {
		case <-ctx.Done():
			return nil, failed
		case sem <- struct{}{}:
		}
		wg.Add(1)
		go func(i int, t *taskState) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := processor.Poll(ctx, t.pollURL); err != nil {
				slog.Warn("blobproc poll failed", "sha1", t.sha1hex, "err", err.Error())
				results[i] = outcome{t: t, ok: false}
				return
			}
			results[i] = outcome{t: t, ok: true}
		}(i, t)
	}
	wg.Wait()
	for _, r := range results {
		if r.t == nil {
			continue
		}
		if !r.ok {
			failed++
			continue
		}
		kept = append(kept, r.t)
	}
	return kept, failed
}

// fetchAndRunProcessResult pulls each task's GROBID + pdftotext output from
// S3 and runs processResult. GROBID parse failures are counted as failures
// (not propagated) so one broken paper doesn't abort the activity.
func fetchAndRunProcessResult(ctx context.Context, processor *pdf.Processor, tasks []*taskState) (processed, failed int) {
	var (
		wg  sync.WaitGroup
		mu  sync.Mutex
		sem = make(chan struct{}, stageConcurrency)
	)
	for _, t := range tasks {
		select {
		case <-ctx.Done():
			return processed, failed
		case sem <- struct{}{}:
		}
		wg.Add(1)
		go func(t *taskState) {
			defer wg.Done()
			defer func() { <-sem }()
			content, err := processor.Fetch(ctx, t.sha1hex)
			if err != nil {
				slog.Warn("blobproc result fetch failed", "sha1", t.sha1hex, "err", err.Error())
				mu.Lock()
				failed++
				mu.Unlock()
				return
			}
			if err := processResult(ctx, t.sha1hex, content); err != nil {
				var gpe *GrobidParseError
				if errors.As(err, &gpe) {
					slog.Warn("grobid parse failed", "sha1", t.sha1hex)
				} else {
					slog.Warn("processResult failed", "sha1", t.sha1hex, "err", err.Error())
				}
				mu.Lock()
				failed++
				mu.Unlock()
				return
			}
			mu.Lock()
			processed++
			mu.Unlock()
		}(t)
	}
	wg.Wait()
	return processed, failed
}

// heartbeat fires an activity heartbeat every 60s until the returned stop
// function is called. Use to keep a long parallel stage alive against the
// HeartbeatTimeout while individual goroutines may be blocked on I/O.
func heartbeat(ctx context.Context, msg string) (stop func()) {
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				activity.RecordHeartbeat(ctx, msg)
			}
		}
	}()
	return func() {
		select {
		case <-done:
		default:
			close(done)
		}
	}
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

// isSkippableItem reports whether an IA item identifier should be skipped
// by the periodic-ingest pipeline. Today this catches -CRL meta containers
// (crawl logs, configs, reports — they have no WARCs or rollup CDX). Add
// further rules here as new meta-item conventions surface.
func isSkippableItem(id string) bool {
	return strings.HasSuffix(id, "-CRL")
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
