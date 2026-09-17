package periodic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"git.archive.org/webgroup/scholar/trawler/cdx/cdxfile"
	"git.archive.org/webgroup/scholar/trawler/harvesting"
	"git.archive.org/webgroup/scholar/trawler/httpclient"
	"git.archive.org/webgroup/scholar/trawler/ia"
	"git.archive.org/webgroup/scholar/trawler/s3"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/log"
)

// Planning is the half of a periodic ingest that decides what a unit of work
// is. It walks the collection's items, stages each one's PDF CDX rows in S3,
// and cuts those rows into byte ranges; a range is what an ingest activity is
// handed.
//
// The rows themselves never reach the workflow, and neither does the item list.
// That is the whole point of the split: a run's Temporal history is a function
// of how many batches it dispatched rather than how many PDFs it ingested or
// how many items the collection holds, and its entire resume state is an item
// index plus a byte offset.
//
// Two levels of list is what makes this messier than the flat scrape. A
// collection holds items, an item holds a WARC-derived CDX, and only the PDF
// rows of that CDX are work. Planning flattens all of that: it may cross item
// boundaries within a single call, and a BatchRef says which item it came from
// so the batch can find the rows again.

// itemsPerPage is the advancedsearch page size used to enumerate a collection.
const itemsPerPage = 100

var unsafeIDChars = regexp.MustCompile(`[^A-Za-z0-9_.-]+`)

// ItemCursor is a run's position in the collection: which item, and how far
// into that item's staged PDF CDX.
//
// It only ever sits on a batch boundary, so resuming from it neither re-ingests
// nor skips a row.
type ItemCursor struct {
	// Index is the position in the item list object, which is why that object
	// is written once and then pinned for the life of the run.
	Index int
	// Offset is the byte position in that item's staged CDX. Zero means the
	// item has not been started.
	Offset int64
}

// BatchRef names a contiguous slice of one item's PDF CDX rows: one ingest
// activity's worth of work.
//
// Offset and End are byte positions in S3Key, End being just past the final
// row's trailing newline. So [Offset, End) re-reads exactly this batch's rows
// however many times it is retried.
type BatchRef struct {
	ItemIndex int
	ItemID    string
	// S3Key is the staged PDF-CDX ndjson for this item.
	S3Key     string
	Offset    int64
	End       int64
	LineCount int
	// NextCursor is where the run resumes once this batch has been dispatched.
	// The planner fills it in because only the planner knows whether this batch
	// was the last of its item, and having the workflow guess would cost it a
	// wasted plan call per item boundary.
	NextCursor ItemCursor
}

// LineRef is one PDF CDX row from a staged item plus the byte offset just past
// its trailing newline, which is where a resume picks up.
type LineRef struct {
	JSON string
	End  int64
}

// ItemListKey is where a run pins the list of items it is going to walk.
//
// The list is written once, at the start of the first run in the chain, and
// then referred to by key for the rest of the chain. It has to be pinned:
// ItemCursor.Index is an index into it, and a periodic crawl collection grows
// while we are ingesting it, so re-enumerating would shift every index under a
// running cursor. Pinning also gives the run a definite scope -- a re-run of
// the same collection later is what picks up items added since.
func ItemListKey(workflowID, runID string) string {
	slug := strings.Trim(unsafeIDChars.ReplaceAllString(workflowID, "_"), "_")
	if slug == "" {
		slug = "periodic"
	}
	if len(runID) > 8 {
		runID = runID[:8]
	}
	return fmt.Sprintf("sandcrawler/pdfitems/%s-%s.txt", slug, runID)
}

// itemCDXKey is where an item's filtered PDF CDX rows are staged.
func itemCDXKey(collection, itemID string) string {
	return fmt.Sprintf("sandcrawler/pdfcdx/%s/%s", collection, itemID)
}

type ListCollectionInput struct {
	CollectionName string
	// ItemListKey is where to write the list. The workflow picks it (see
	// ItemListKey) so that the key is decided once and carried, rather than
	// re-derived by an activity that might be retried on another host.
	ItemListKey string
	// StartAtItem, when set, is an item identifier to resolve to an index in
	// the list this call writes. See PeriodicIngestInput.StartAtItem.
	StartAtItem string
}

type ListCollectionOutput struct {
	ItemCount int
	// StartIndex is where StartAtItem landed in the list, or zero when no
	// StartAtItem was asked for.
	StartIndex int
}

// ListCollectionActivity enumerates an IA collection and writes its item
// identifiers, sorted and one per line, to ItemListKey.
//
// Sorting is what makes ItemCursor.Index mean something stable: advancedsearch
// paging is not ordered strongly enough to index into directly.
func ListCollectionActivity(ctx context.Context, in ListCollectionInput) (ListCollectionOutput, error) {
	var out ListCollectionOutput

	if in.CollectionName == "" || in.ItemListKey == "" {
		return out, errors.New("periodic: CollectionName and ItemListKey are required")
	}

	client := metadataClient()
	var ids []string

	for page := 1; ; page++ {
		activity.RecordHeartbeat(ctx, fmt.Sprintf("page %d", page))

		found, hasMore, err := ia.SearchCollection(ctx, client, in.CollectionName, page, itemsPerPage)
		if err != nil {
			return out, fmt.Errorf("failed to search %q: %w", in.CollectionName, err)
		}

		for _, id := range found {
			// -CRL items hold the crawl log rather than crawled content.
			if !strings.HasSuffix(id, "-CRL") {
				ids = append(ids, id)
			}
		}

		if !hasMore {
			break
		}
	}

	slices.Sort(ids)
	ids = slices.Compact(ids)

	body := []byte(strings.Join(ids, "\n"))
	if len(ids) > 0 {
		body = append(body, '\n')
	}
	if err := s3.PutObject(ctx, in.ItemListKey, body, "text/plain"); err != nil {
		return out, fmt.Errorf("failed to write item list to %q: %w", in.ItemListKey, err)
	}

	out.ItemCount = len(ids)

	if in.StartAtItem != "" {
		idx, err := resolveStartIndex(ids, in.StartAtItem)
		if err != nil {
			return out, err
		}
		out.StartIndex = idx
	}

	return out, nil
}

// resolveStartIndex finds where an item sits in the pinned list.
//
// An item that is not there is an error rather than a silent start from the
// top. Getting this wrong is expensive in a way that is easy to miss: a run
// that quietly restarts at index zero re-checks every row of the collection
// against fatcat and both ES indices, which takes hours and looks exactly like
// a run that is working.
func resolveStartIndex(items []string, startAt string) (int, error) {
	idx := slices.Index(items, startAt)
	if idx < 0 {
		return 0, fmt.Errorf(
			"periodic: start-at item %q is not in this collection's %d items", startAt, len(items))
	}
	return idx, nil
}

type PlanBatchesInput struct {
	CollectionName string
	ItemListKey    string
	Cursor         ItemCursor
	// BatchSize is how many PDF CDX rows go in one batch.
	BatchSize int
	// MaxBatches bounds how many batches one call plans.
	MaxBatches int
	// MaxItems bounds how many items one call may stage. Staging an item means
	// downloading and filtering its rollup CDX, so without this a collection
	// with a long run of PDF-less items would turn one planning call into an
	// unbounded download.
	MaxItems int
	// ChunkSize is how many bytes one range read pulls.
	ChunkSize int
}

type PlanBatchesOutput struct {
	Batches []BatchRef
	// NextCursor is where the next planning call resumes. It never lands
	// mid-batch.
	NextCursor ItemCursor
	// EOF reports that the scan reached the end of the item list, so these are
	// the last batches in the collection.
	EOF bool
}

// PlanBatchesActivity stages the next stretch of the collection and cuts it
// into batches.
//
// It may cross item boundaries: a plan call keeps going until it has enough
// batches, has touched enough items, or has run out of collection. Items with
// no usable rollup CDX are logged to the outcome file and stepped over, because
// a periodic crawl collection reliably contains some.
func PlanBatchesActivity(ctx context.Context, in PlanBatchesInput) (PlanBatchesOutput, error) {
	l := activity.GetLogger(ctx)
	out := PlanBatchesOutput{NextCursor: in.Cursor}

	if in.BatchSize <= 0 || in.MaxBatches <= 0 || in.MaxItems <= 0 || in.ChunkSize <= 0 {
		return out, fmt.Errorf("periodic: bad plan config (batch=%d max=%d items=%d chunk=%d)",
			in.BatchSize, in.MaxBatches, in.MaxItems, in.ChunkSize)
	}

	items, err := readItemList(ctx, in.ItemListKey, in.ChunkSize)
	if err != nil {
		return out, err
	}

	client := metadataClient()
	logDir := outcomeLogDir()
	workflowID := activity.GetInfo(ctx).WorkflowExecution.ID

	return planBatches(ctx, planDeps{
		stage: func(ctx context.Context, itemID string) (string, error) {
			return stageItemCDX(ctx, client, in.CollectionName, itemID)
		},
		cut: planItemBatches,
		skipped: func(itemID string, cause error) error {
			return appendOutcome(logDir, workflowID, Outcome{
				ItemID: itemID,
				Status: StatusSkip,
				Detail: cause.Error(),
				At:     time.Now(),
			})
		},
		beat: func(msg string) { activity.RecordHeartbeat(ctx, msg) },
		log:  l,
	}, items, in)
}

// planDeps is what the planning walk needs from the outside world, injected so
// the walk itself -- which is where the item-boundary bookkeeping lives, and so
// where a resume can go wrong -- can be tested without petabox or S3.
type planDeps struct {
	// stage makes sure an item's PDF CDX rows are in S3 and returns their key.
	stage func(ctx context.Context, itemID string) (string, error)
	// cut plans one staged item's batches from an offset.
	cut func(ctx context.Context, s3key string, args planItemArgs) ([]BatchRef, int64, bool, error)
	// skipped records an item stepped over for want of a usable rollup CDX.
	skipped func(itemID string, cause error) error
	beat    func(string)
	log     log.Logger
}

// planBatches walks the collection from in.Cursor, staging and cutting items
// until it has enough batches, has touched enough items, or has run out of
// collection.
func planBatches(ctx context.Context, d planDeps, items []string, in PlanBatchesInput) (PlanBatchesOutput, error) {
	out := PlanBatchesOutput{NextCursor: in.Cursor}

	cursor := in.Cursor
	cursor.Index = max(cursor.Index, 0)
	itemsTouched := 0

	for cursor.Index < len(items) && len(out.Batches) < in.MaxBatches && itemsTouched < in.MaxItems {
		itemID := items[cursor.Index]
		d.beat(fmt.Sprintf("item %d/%d %s", cursor.Index+1, len(items), itemID))

		s3key, err := d.stage(ctx, itemID)
		if err != nil {
			// An item with no rollup CDX, or with several and no way to choose,
			// is a fact about the collection rather than a failure of this run.
			// Step over it; anything else is ours and is worth a retry.
			if errors.Is(err, ia.ErrNoCDX) || errors.Is(err, ia.ErrMultipleCDX) {
				d.log.Warn("skipping item with no usable rollup CDX",
					"item", itemID, "err", err.Error())
				if logErr := d.skipped(itemID, err); logErr != nil {
					return PlanBatchesOutput{NextCursor: in.Cursor}, logErr
				}
				cursor = ItemCursor{Index: cursor.Index + 1}
				itemsTouched++
				continue
			}
			// Discard the partial plan: the activity is about to be retried, and
			// staging is idempotent, so redoing it is a handful of S3 stats.
			return PlanBatchesOutput{NextCursor: in.Cursor},
				fmt.Errorf("staging %s/%s: %w", in.CollectionName, itemID, err)
		}

		batches, nextOffset, itemEOF, err := d.cut(ctx, s3key, planItemArgs{
			ItemIndex:  cursor.Index,
			ItemID:     itemID,
			From:       cursor.Offset,
			BatchSize:  in.BatchSize,
			MaxBatches: in.MaxBatches - len(out.Batches),
			ChunkSize:  in.ChunkSize,
		})
		if err != nil {
			return PlanBatchesOutput{NextCursor: in.Cursor}, err
		}

		if itemEOF {
			cursor = ItemCursor{Index: cursor.Index + 1}
			itemsTouched++
		} else {
			// The only other way the scan stops is a full batch budget, so the
			// loop is about to end and this item picks up here next time.
			cursor.Offset = nextOffset
		}

		// Every batch resumes at the row after it except the last, which resumes
		// wherever the item scan left the cursor -- the next item, if this batch
		// finished one off.
		for i := range batches {
			batches[i].NextCursor = ItemCursor{Index: batches[i].ItemIndex, Offset: batches[i].End}
		}
		if n := len(batches); n > 0 {
			batches[n-1].NextCursor = cursor
		}
		out.Batches = append(out.Batches, batches...)
	}

	out.NextCursor = cursor
	out.EOF = cursor.Index >= len(items)
	return out, nil
}

type planItemArgs struct {
	ItemIndex  int
	ItemID     string
	From       int64
	BatchSize  int
	MaxBatches int
	ChunkSize  int
}

// planItemBatches cuts one staged item's rows into batches, starting at
// args.From. It reports where in the item the next call resumes and whether the
// item is exhausted.
func planItemBatches(ctx context.Context, s3key string, args planItemArgs) ([]BatchRef, int64, bool, error) {
	obj, err := s3.GetObject(ctx, s3key)
	if err != nil {
		return nil, args.From, false, err
	}
	defer obj.Close()

	batches, nextOffset, itemEOF, err := cutBatches(obj, s3key, args)
	if err != nil {
		return nil, args.From, false, fmt.Errorf("planning %s: %w", s3key, err)
	}
	return batches, nextOffset, itemEOF, nil
}

// cutBatches is the cutting logic, split out from planItemBatches so it can be
// tested without S3.
func cutBatches(r io.ReaderAt, s3key string, args planItemArgs) ([]BatchRef, int64, bool, error) {
	var batches []BatchRef

	// batchStart trails one batch behind the scan: it is the end of the last
	// batch emitted, which is both where the next batch begins and the offset a
	// resume picks up from. Cutting only on a batch boundary is what keeps a
	// resume from landing in the middle of a batch somebody is already ingesting.
	batchStart := args.From
	count := 0
	lastEnd := args.From

	emit := func(end int64) {
		batches = append(batches, BatchRef{
			ItemIndex: args.ItemIndex,
			ItemID:    args.ItemID,
			S3Key:     s3key,
			Offset:    batchStart,
			End:       end,
			LineCount: count,
		})
		batchStart = end
		count = 0
	}

	consumed, err := harvesting.ScanLines(r, args.From, -1, args.ChunkSize, func(_ string, end int64) bool {
		count++
		lastEnd = end
		if count < args.BatchSize {
			return true
		}
		emit(end)
		return len(batches) < args.MaxBatches
	})
	if err != nil {
		return nil, args.From, false, err
	}

	if consumed {
		// The item ran out mid-batch. A short final batch is still a batch --
		// dropping it would silently abandon the tail of every item whose row
		// count is not a multiple of BatchSize.
		if count > 0 {
			emit(lastEnd)
		}
		return batches, batchStart, true, nil
	}

	return batches, batchStart, false, nil
}

// stageItemCDX makes sure an item's PDF CDX rows are in S3 and returns their
// key.
//
// Staging is the expensive, flaky part of this pipeline -- a metadata call, a
// whole rollup CDX download, a parse -- so it is done once and then reused by
// every plan call, every retry and every run in the ContinueAsNew chain that
// touches the item. An already-staged item costs one HEAD.
//
// An empty object is a real result: plenty of items hold no PDFs at all, and
// re-deriving those on every pass would be the bulk of the work for some
// collections.
func stageItemCDX(ctx context.Context, client *http.Client, collection, itemID string) (string, error) {
	s3key := itemCDXKey(collection, itemID)

	cached, err := s3.ObjectExists(ctx, s3key)
	if err != nil {
		return "", err
	}
	if cached {
		return s3key, nil
	}

	files, err := ia.ItemFiles(ctx, client, itemID)
	if err != nil {
		return "", fmt.Errorf("metadata: %w", err)
	}
	rollup, err := ia.FindRollupCDX(files)
	if err != nil {
		return "", err
	}

	activity.RecordHeartbeat(ctx, "download-cdx "+itemID)
	rdr, err := ia.OpenFile(ctx, client, itemID, rollup)
	if err != nil {
		return "", fmt.Errorf("open rollup CDX %s: %w", rollup, err)
	}
	defer rdr.Close()

	activity.RecordHeartbeat(ctx, "parse-cdx "+itemID)
	rows, err := cdxfile.Parse(rdr, cdxfile.PDFFilter)
	if err != nil {
		return "", fmt.Errorf("parse rollup CDX %s: %w", rollup, err)
	}

	var jsonl []byte
	for _, row := range rows {
		jl, err := json.Marshal(row)
		if err != nil {
			return "", fmt.Errorf("failed to encode cdx row %#v: %w", row, err)
		}
		jsonl = append(jsonl, jl...)
		jsonl = append(jsonl, '\n')
	}

	if err := s3.PutObject(ctx, s3key, jsonl, "application/x-ndjson"); err != nil {
		return "", fmt.Errorf("failed to write pdf cdx to %q: %w", s3key, err)
	}

	return s3key, nil
}

// readItemList pulls the pinned item list. It is read whole -- even a large
// collection is a few megabytes of identifiers -- because the planner needs to
// index into it and to know where the end is.
func readItemList(ctx context.Context, s3key string, chunkSize int) ([]string, error) {
	obj, err := s3.GetObject(ctx, s3key)
	if err != nil {
		return nil, err
	}
	defer obj.Close()

	var items []string
	if _, err := harvesting.ScanLines(obj, 0, -1, chunkSize, func(line string, _ int64) bool {
		items = append(items, line)
		return true
	}); err != nil {
		return nil, fmt.Errorf("reading item list %s: %w", s3key, err)
	}
	return items, nil
}

// readBatchLines pulls one batch's CDX rows out of a staged item. This is the
// read the old per-line design paid once per line and then put in workflow
// history; here it stays inside the activity that is about to ingest them.
func readBatchLines(ctx context.Context, ref BatchRef, chunkSize int) ([]LineRef, error) {
	obj, err := s3.GetObject(ctx, ref.S3Key)
	if err != nil {
		return nil, err
	}
	defer obj.Close()

	lines, err := sliceLines(ref, chunkSize, obj)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", ref.S3Key, err)
	}
	return lines, nil
}

// sliceLines is the reading logic, split out from readBatchLines so it can be
// tested without S3.
func sliceLines(ref BatchRef, chunkSize int, r io.ReaderAt) ([]LineRef, error) {
	out := make([]LineRef, 0, ref.LineCount)
	if _, err := harvesting.ScanLines(r, ref.Offset, ref.End, chunkSize, func(line string, end int64) bool {
		out = append(out, LineRef{JSON: line, End: end})
		return true
	}); err != nil {
		return nil, err
	}
	return out, nil
}

// metadataClient is the HTTP client for archive.org calls: the shared
// connection pool, with a timeout generous enough for a large rollup CDX
// download.
func metadataClient() *http.Client {
	return &http.Client{
		Transport: httpclient.Transport(),
		Timeout:   10 * time.Minute,
	}
}
