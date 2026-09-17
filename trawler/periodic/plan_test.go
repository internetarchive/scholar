package periodic

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"git.archive.org/webgroup/scholar/trawler/ia"
	"github.com/stretchr/testify/require"
)

// --- cutBatches -----------------------------------------------------------

// rows builds n ndjson-ish lines of a fixed ten bytes each, so the byte
// arithmetic in the assertions stays readable.
func rows(n int) string {
	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b, "row%06d\n", i)
	}
	return b.String()
}

func cut(t *testing.T, body string, from int64, batchSize, maxBatches int) ([]BatchRef, int64, bool) {
	t.Helper()
	batches, next, eof, err := cutBatches(bytes.NewReader([]byte(body)), "bucket/item", planItemArgs{
		ItemIndex:  3,
		ItemID:     "ITEM-3",
		From:       from,
		BatchSize:  batchSize,
		MaxBatches: maxBatches,
		ChunkSize:  4096,
	})
	require.NoError(t, err)
	return batches, next, eof
}

func Test_cutBatches_wholeItem(t *testing.T) {
	batches, next, eof := cut(t, rows(25), 0, 10, 8)

	require.True(t, eof, "the whole item was scanned")
	require.Len(t, batches, 3)
	// A short final batch is still a batch: dropping it would abandon the tail
	// of every item whose row count is not a multiple of BatchSize.
	require.Equal(t, 5, batches[2].LineCount)
	require.Equal(t, int64(250), next)

	// Ranges tile without gaps or overlap, which is what makes re-reading a
	// batch reproduce exactly its own rows.
	require.Equal(t, int64(0), batches[0].Offset)
	require.Equal(t, int64(100), batches[0].End)
	require.Equal(t, int64(100), batches[1].Offset)
	require.Equal(t, int64(200), batches[1].End)
	require.Equal(t, int64(200), batches[2].Offset)
	require.Equal(t, int64(250), batches[2].End)

	// The item is carried on every ref so the batch can find its rows again.
	for _, b := range batches {
		require.Equal(t, 3, b.ItemIndex)
		require.Equal(t, "ITEM-3", b.ItemID)
		require.Equal(t, "bucket/item", b.S3Key)
	}
}

// Filling the batch budget mid-item leaves the rest of the item for the next
// call, and must not claim the item is finished.
func Test_cutBatches_budgetFullMidItem(t *testing.T) {
	batches, next, eof := cut(t, rows(100), 0, 10, 3)

	require.False(t, eof)
	require.Len(t, batches, 3)
	require.Equal(t, int64(300), next, "resume just past the last batch cut")
}

func Test_cutBatches_resumesFromOffset(t *testing.T) {
	batches, next, eof := cut(t, rows(25), 100, 10, 8)

	require.True(t, eof)
	require.Len(t, batches, 2)
	require.Equal(t, int64(100), batches[0].Offset)
	require.Equal(t, int64(250), next)
}

// An item with no PDF rows at all is normal -- most of a crawl collection is
// not PDFs -- and must come back as "finished, nothing to do" rather than as a
// stall.
func Test_cutBatches_emptyItem(t *testing.T) {
	batches, next, eof := cut(t, "", 0, 10, 8)

	require.True(t, eof)
	require.Empty(t, batches)
	require.Equal(t, int64(0), next)
}

// --- sliceLines -----------------------------------------------------------

// The read has to reproduce exactly the rows the plan counted; a disagreement
// of one line means PDFs quietly skipped or ingested twice.
func Test_sliceLines_readsExactlyItsBatch(t *testing.T) {
	body := rows(25)
	batches, _, _ := cut(t, body, 0, 10, 8)

	for _, ref := range batches {
		got, err := sliceLines(ref, 4096, bytes.NewReader([]byte(body)))
		require.NoError(t, err)
		require.Len(t, got, ref.LineCount)
		require.Equal(t, ref.End, got[len(got)-1].End,
			"the last row's end must be the batch's end")
	}

	first, err := sliceLines(batches[0], 4096, bytes.NewReader([]byte(body)))
	require.NoError(t, err)
	require.Equal(t, "row000000", first[0].JSON)
	require.Equal(t, "row000009", first[9].JSON)
}

// --- planBatches ----------------------------------------------------------

// fakePlan stands in for petabox and S3: every item has rowsPer rows except
// those named in noCDX, which behave like an item with no rollup CDX.
type fakePlan struct {
	rowsPer map[string]int
	noCDX   map[string]bool
	staged  []string
	skipped []string
}

func (f *fakePlan) deps() planDeps {
	return planDeps{
		stage: func(_ context.Context, itemID string) (string, error) {
			f.staged = append(f.staged, itemID)
			if f.noCDX[itemID] {
				return "", ia.ErrNoCDX
			}
			return "bucket/cdx/" + itemID, nil
		},
		cut: func(_ context.Context, s3key string, args planItemArgs) ([]BatchRef, int64, bool, error) {
			return cutBatches(bytes.NewReader([]byte(rows(f.rowsPer[args.ItemID]))), s3key, args)
		},
		skipped: func(itemID string, _ error) error {
			f.skipped = append(f.skipped, itemID)
			return nil
		},
		beat: func(string) {},
		log:  slog.Default(),
	}
}

func planInput(cursor ItemCursor, maxBatches, maxItems int) PlanBatchesInput {
	return PlanBatchesInput{
		CollectionName: "COLL",
		ItemListKey:    "bucket/items.txt",
		Cursor:         cursor,
		BatchSize:      10,
		MaxBatches:     maxBatches,
		MaxItems:       maxItems,
		ChunkSize:      4096,
	}
}

// The planner flattens two levels of list, so one call routinely spans several
// items. Each batch has to carry the cursor that resumes after it, including
// the one that steps over an item boundary.
func Test_planBatches_crossesItemBoundaries(t *testing.T) {
	f := &fakePlan{rowsPer: map[string]int{"a": 15, "b": 10, "c": 5}}

	out, err := planBatches(context.Background(), f.deps(), []string{"a", "b", "c"}, planInput(ItemCursor{}, 8, 8))
	require.NoError(t, err)
	require.True(t, out.EOF)
	require.Equal(t, ItemCursor{Index: 3}, out.NextCursor)

	// a: 10 + 5, b: 10, c: 5
	require.Len(t, out.Batches, 4)
	require.Equal(t, []int{10, 5, 10, 5}, lineCounts(out.Batches))

	// The batch that finishes item a resumes at item b, not mid-a.
	require.Equal(t, ItemCursor{Index: 0, Offset: 100}, out.Batches[0].NextCursor)
	require.Equal(t, ItemCursor{Index: 1}, out.Batches[1].NextCursor)
	require.Equal(t, ItemCursor{Index: 2}, out.Batches[2].NextCursor)
	require.Equal(t, ItemCursor{Index: 3}, out.Batches[3].NextCursor)
}

// Resuming mid-item must pick up inside that item rather than restarting it,
// which is the whole reason the cursor carries an offset as well as an index.
func Test_planBatches_resumesMidItem(t *testing.T) {
	f := &fakePlan{rowsPer: map[string]int{"a": 30, "b": 10}}

	out, err := planBatches(context.Background(), f.deps(), []string{"a", "b"},
		planInput(ItemCursor{Index: 0, Offset: 200}, 8, 8))
	require.NoError(t, err)
	require.True(t, out.EOF)
	require.Equal(t, []string{"a", "b"}, f.staged)
	require.Len(t, out.Batches, 2)
	require.Equal(t, int64(200), out.Batches[0].Offset)
	require.Equal(t, 1, out.Batches[1].ItemIndex)
}

// A full batch budget stops the walk without touching the next item, and the
// cursor it hands back points inside the item it stopped in.
func Test_planBatches_stopsAtBatchBudget(t *testing.T) {
	f := &fakePlan{rowsPer: map[string]int{"a": 100, "b": 100}}

	out, err := planBatches(context.Background(), f.deps(), []string{"a", "b"}, planInput(ItemCursor{}, 3, 8))
	require.NoError(t, err)
	require.False(t, out.EOF)
	require.Len(t, out.Batches, 3)
	require.Equal(t, []string{"a"}, f.staged, "item b must not have been staged")
	require.Equal(t, ItemCursor{Index: 0, Offset: 300}, out.NextCursor)
	require.Equal(t, out.NextCursor, out.Batches[2].NextCursor)
}

// Staging is the expensive part, so a long run of PDF-less items must not turn
// one planning call into an unbounded download.
func Test_planBatches_stopsAtItemBudget(t *testing.T) {
	f := &fakePlan{rowsPer: map[string]int{}} // every item is empty

	out, err := planBatches(context.Background(), f.deps(),
		[]string{"a", "b", "c", "d", "e"}, planInput(ItemCursor{}, 8, 2))
	require.NoError(t, err)
	require.False(t, out.EOF)
	require.Empty(t, out.Batches)
	require.Len(t, f.staged, 2)
	// Progress was still made, which is what keeps the workflow's
	// no-progress guard from firing on a stretch of empty items.
	require.Equal(t, ItemCursor{Index: 2}, out.NextCursor)
}

// A crawl collection reliably contains items with no usable rollup CDX. They
// are stepped over and written to the outcome log, not treated as failures.
func Test_planBatches_skipsItemsWithNoCDX(t *testing.T) {
	f := &fakePlan{
		rowsPer: map[string]int{"a": 10, "c": 10},
		noCDX:   map[string]bool{"b": true},
	}

	out, err := planBatches(context.Background(), f.deps(), []string{"a", "b", "c"}, planInput(ItemCursor{}, 8, 8))
	require.NoError(t, err)
	require.True(t, out.EOF)
	require.Equal(t, []string{"b"}, f.skipped)
	require.Len(t, out.Batches, 2)
	require.Equal(t, "a", out.Batches[0].ItemID)
	require.Equal(t, "c", out.Batches[1].ItemID)
	// The batch that ends item a resumes at index 1, so the skip is re-decided
	// rather than assumed -- a rollup CDX that shows up later is picked up.
	require.Equal(t, ItemCursor{Index: 1}, out.Batches[0].NextCursor)
}

// Anything other than a missing CDX is ours, and the partial plan is discarded
// so the retry redoes the whole call from the cursor it was given.
func Test_planBatches_realStagingErrorDiscardsThePlan(t *testing.T) {
	f := &fakePlan{rowsPer: map[string]int{"a": 10}}
	d := f.deps()
	stage := d.stage
	d.stage = func(ctx context.Context, itemID string) (string, error) {
		if itemID == "b" {
			return "", fmt.Errorf("metadata: 503 from archive.org")
		}
		return stage(ctx, itemID)
	}

	out, err := planBatches(context.Background(), d, []string{"a", "b"},
		planInput(ItemCursor{Index: 0}, 8, 8))
	require.ErrorContains(t, err, "503 from archive.org")
	require.Empty(t, out.Batches)
	require.Equal(t, ItemCursor{Index: 0}, out.NextCursor)
}

func Test_planBatches_endOfCollection(t *testing.T) {
	f := &fakePlan{rowsPer: map[string]int{}}

	out, err := planBatches(context.Background(), f.deps(), []string{"a"}, planInput(ItemCursor{Index: 1}, 8, 8))
	require.NoError(t, err)
	require.True(t, out.EOF)
	require.Empty(t, out.Batches)
	require.Empty(t, f.staged)
}

func lineCounts(refs []BatchRef) []int {
	out := make([]int, 0, len(refs))
	for _, r := range refs {
		out = append(out, r.LineCount)
	}
	return out
}

// --- keys -----------------------------------------------------------------

func Test_ItemListKey(t *testing.T) {
	require.Equal(t, "sandcrawler/pdfitems/periodic_ingest_MY-COLL-abcd1234.txt",
		ItemListKey("periodic_ingest_MY-COLL", "abcd1234-dead-beef"))
}

func Test_itemCDXKey(t *testing.T) {
	require.Equal(t, "sandcrawler/pdfcdx/COLL/ITEM-1", itemCDXKey("COLL", "ITEM-1"))
}

// --- resolveStartIndex ----------------------------------------------------

func Test_resolveStartIndex(t *testing.T) {
	items := []string{"ITEM-a", "ITEM-b", "ITEM-c"}

	idx, err := resolveStartIndex(items, "ITEM-b")
	require.NoError(t, err)
	require.Equal(t, 1, idx)

	idx, err = resolveStartIndex(items, "ITEM-a")
	require.NoError(t, err)
	require.Equal(t, 0, idx)
}

// Silently starting from the top would re-check every row of the collection
// against fatcat and both indices -- hours of no-ops that look exactly like a
// run that is working. Say so instead.
func Test_resolveStartIndex_missingItemIsAnError(t *testing.T) {
	_, err := resolveStartIndex([]string{"ITEM-a", "ITEM-b"}, "ITEM-typo")
	require.ErrorContains(t, err, "ITEM-typo")
	require.ErrorContains(t, err, "not in this collection")
}
