package scrape

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// --- scanLines ------------------------------------------------------------

// lines collects everything scanLines yields over a range.
func lines(t *testing.T, body string, start, end int64, chunk int) ([]URLRef, bool) {
	t.Helper()
	var got []URLRef
	consumed, err := scanLines(bytes.NewReader([]byte(body)), start, end, chunk,
		func(u string, lineEnd int64) bool {
			got = append(got, URLRef{URL: u, End: lineEnd})
			return true
		})
	require.NoError(t, err)
	return got, consumed
}

func urls(refs []URLRef) []string {
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		out = append(out, r.URL)
	}
	return out
}

func Test_scanLines_readsWholeList(t *testing.T) {
	got, consumed := lines(t, "http://a/\nhttp://b/\nhttp://c/\n", 0, -1, 4096)
	require.Equal(t, []string{"http://a/", "http://b/", "http://c/"}, urls(got))
	require.True(t, consumed)
	// The offset handed back is the byte after each line's newline, which is
	// where a resume picks up.
	require.Equal(t, int64(10), got[0].End)
	require.Equal(t, int64(20), got[1].End)
	require.Equal(t, int64(30), got[2].End)
}

func Test_scanLines_skipsBlankLinesAndTrimsCR(t *testing.T) {
	got, consumed := lines(t, "http://a/\r\n\n   \nhttp://b/\r\n", 0, -1, 4096)
	require.Equal(t, []string{"http://a/", "http://b/"}, urls(got))
	require.True(t, consumed)
}

// A final line with no trailing newline still counts.
func Test_scanLines_unterminatedFinalLine(t *testing.T) {
	got, consumed := lines(t, "http://a/\nhttp://b/", 0, -1, 4096)
	require.Equal(t, []string{"http://a/", "http://b/"}, urls(got))
	require.True(t, consumed)
	require.Equal(t, int64(19), got[1].End)
}

// A URL longer than one chunk must be stitched across reads, not truncated.
func Test_scanLines_lineSpansChunks(t *testing.T) {
	long := "http://example.com/" + strings.Repeat("x", 200)
	got, consumed := lines(t, long+"\nhttp://b/\n", 0, -1, 8)
	require.Equal(t, []string{long, "http://b/"}, urls(got))
	require.True(t, consumed)
}

func Test_scanLines_emptyObject(t *testing.T) {
	got, consumed := lines(t, "", 0, -1, 4096)
	require.Empty(t, got)
	require.True(t, consumed)
}

func Test_scanLines_offsetPastEnd(t *testing.T) {
	got, consumed := lines(t, "http://a/\n", 10, -1, 4096)
	require.Empty(t, got)
	require.True(t, consumed)
}

// A bounded range yields exactly the lines inside it, whatever the chunk size.
func Test_scanLines_boundedRange(t *testing.T) {
	body := "http://a/\nhttp://b/\nhttp://c/\n"
	for _, chunk := range []int{1, 3, 7, 4096} {
		t.Run(fmt.Sprintf("chunk=%d", chunk), func(t *testing.T) {
			got, consumed := lines(t, body, 10, 20, chunk)
			require.Equal(t, []string{"http://b/"}, urls(got))
			require.True(t, consumed)
		})
	}
}

// Stopping early is how the planner says "I have enough batches", so it has to
// be distinguishable from running out of list.
func Test_scanLines_stoppingEarlyIsNotConsumed(t *testing.T) {
	var got []string
	consumed, err := scanLines(bytes.NewReader([]byte("http://a/\nhttp://b/\n")), 0, -1, 4096,
		func(u string, _ int64) bool {
			got = append(got, u)
			return false
		})
	require.NoError(t, err)
	require.False(t, consumed)
	require.Equal(t, []string{"http://a/"}, got)
}

func Test_scanLines_rejectsBadChunkSize(t *testing.T) {
	_, err := scanLines(bytes.NewReader(nil), 0, -1, 0, func(string, int64) bool { return true })
	require.Error(t, err)
}

// --- planBatches ----------------------------------------------------------

// list builds a URL list body plus the plan input to walk it.
func list(n int) string {
	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b, "http://example.com/%d\n", i)
	}
	return b.String()
}

func plan(t *testing.T, body string, offset int64, batchSize, maxBatches int) PlanBatchesOutput {
	t.Helper()
	out, err := planBatches(PlanBatchesInput{
		Offset:     offset,
		BatchSize:  batchSize,
		MaxBatches: maxBatches,
		ChunkSize:  64,
	}, bytes.NewReader([]byte(body)))
	require.NoError(t, err)
	return out
}

func Test_planBatches_cutsEvenly(t *testing.T) {
	body := list(6)
	out := plan(t, body, 0, 2, 10)

	require.Len(t, out.Batches, 3)
	require.True(t, out.EOF)
	for _, b := range out.Batches {
		require.Equal(t, 2, b.URLCount)
	}
	// Batches tile the list: each starts where the last ended, and the last
	// ends at the end of the object.
	require.Equal(t, int64(0), out.Batches[0].Offset)
	require.Equal(t, out.Batches[0].End, out.Batches[1].Offset)
	require.Equal(t, out.Batches[1].End, out.Batches[2].Offset)
	require.Equal(t, int64(len(body)), out.Batches[2].End)
	require.Equal(t, out.Batches[2].End, out.NextOffset)
}

// A list whose length is not a multiple of batch_size still has a tail, and
// dropping it would silently abandon URLs.
func Test_planBatches_shortFinalBatch(t *testing.T) {
	out := plan(t, list(7), 0, 3, 10)
	require.Len(t, out.Batches, 3)
	require.Equal(t, 3, out.Batches[0].URLCount)
	require.Equal(t, 3, out.Batches[1].URLCount)
	require.Equal(t, 1, out.Batches[2].URLCount)
	require.True(t, out.EOF)
}

// Filling MaxBatches part-way through the object is not EOF: reporting it
// there would abandon the rest of the list.
func Test_planBatches_maxBatchesIsNotEOF(t *testing.T) {
	body := list(10)
	out := plan(t, body, 0, 2, 2)

	require.Len(t, out.Batches, 2)
	require.False(t, out.EOF)
	require.Equal(t, out.Batches[1].End, out.NextOffset)

	// Resuming from NextOffset yields exactly the remainder, no repeats.
	rest := plan(t, body, out.NextOffset, 2, 10)
	require.Len(t, rest.Batches, 3)
	require.True(t, rest.EOF)
	require.Equal(t, out.NextOffset, rest.Batches[0].Offset)
}

func Test_planBatches_emptyObject(t *testing.T) {
	out := plan(t, "", 0, 5, 10)
	require.Empty(t, out.Batches)
	require.True(t, out.EOF)
	require.Equal(t, int64(0), out.NextOffset)
}

// Blank lines are not URLs, but they are bytes, so they must land inside a
// batch's range rather than between two of them.
func Test_planBatches_blankLinesStayInsideBatches(t *testing.T) {
	body := "http://a/\n\n\nhttp://b/\nhttp://c/\n"
	out := plan(t, body, 0, 2, 10)

	require.Len(t, out.Batches, 2)
	require.Equal(t, 2, out.Batches[0].URLCount)
	require.Equal(t, 1, out.Batches[1].URLCount)
	require.Equal(t, out.Batches[0].End, out.Batches[1].Offset)
}

func Test_PlanBatchesActivity_rejectsBadConfig(t *testing.T) {
	_, err := PlanBatchesActivity(t.Context(), PlanBatchesInput{
		S3Key: "b/k", BatchSize: 0, MaxBatches: 1, ChunkSize: 10,
	})
	require.Error(t, err)
}

// --- planning and reading agree -------------------------------------------

// The two halves of the split have to describe the same list. Planning records
// byte ranges and a crawl re-reads them later on another host, so anything that
// makes them disagree shows up as URLs crawled twice or not at all.
func Test_planAndSlice_roundTripsTheWholeList(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"plain", list(20)},
		{"unterminated", strings.TrimSuffix(list(20), "\n")},
		{"blank lines", "http://a/\n\nhttp://b/\n\n\nhttp://c/\nhttp://d/\n"},
		{"crlf", "http://a/\r\nhttp://b/\r\nhttp://c/\r\n"},
		{"trailing blanks", list(5) + "\n\n   \n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := bytes.NewReader([]byte(tc.body))

			var got []string
			offset := int64(0)
			for {
				out, err := planBatches(PlanBatchesInput{
					Offset: offset, BatchSize: 3, MaxBatches: 2, ChunkSize: 8,
				}, r)
				require.NoError(t, err)

				for _, ref := range out.Batches {
					sliced, err := sliceURLs(ref, 8, r)
					require.NoError(t, err)
					require.Len(t, sliced, ref.URLCount,
						"batch at %d promised %d urls", ref.Offset, ref.URLCount)
					got = append(got, urls(sliced)...)
				}

				offset = out.NextOffset
				if out.EOF {
					break
				}
			}

			// Whatever the batching, the URLs come out exactly once each and in
			// list order.
			all, _ := lines(t, tc.body, 0, -1, 4096)
			require.Equal(t, urls(all), got)
		})
	}
}
