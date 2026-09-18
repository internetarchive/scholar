package daily

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// scanLines itself is harvesting.ScanLines and is tested there (and again in
// scrape). What is worth testing here is the cutting built on top of it, and
// above all that planning and reading describe the same file: planning records
// byte ranges and a batch re-reads them later on another host, so anything
// that makes the two disagree shows up as records worked twice or not at all.

// ndjson builds a plausible day of upstream metadata: n one-line JSON docs.
func ndjson(n int) string {
	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b, `{"DOI":"10.1234/x%d","title":"paper %d"}`+"\n", i, i)
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

func jsons(refs []LineRef) []string {
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		out = append(out, r.JSON)
	}
	return out
}

func Test_planBatches_cutsEvenly(t *testing.T) {
	body := ndjson(6)
	out := plan(t, body, 0, 2, 10)

	require.Len(t, out.Batches, 3)
	require.True(t, out.EOF)
	for _, b := range out.Batches {
		require.Equal(t, 2, b.LineCount)
	}
	// Batches tile the object: each starts where the last ended, and the last
	// ends at the end of the object.
	require.Equal(t, int64(0), out.Batches[0].Offset)
	require.Equal(t, out.Batches[0].End, out.Batches[1].Offset)
	require.Equal(t, out.Batches[1].End, out.Batches[2].Offset)
	require.Equal(t, int64(len(body)), out.Batches[2].End)
	require.Equal(t, out.Batches[2].End, out.NextOffset)
}

// A day whose record count is not a multiple of batch_size still has a tail,
// and dropping it would silently abandon records.
func Test_planBatches_shortFinalBatch(t *testing.T) {
	out := plan(t, ndjson(7), 0, 3, 10)
	require.Len(t, out.Batches, 3)
	require.Equal(t, 3, out.Batches[0].LineCount)
	require.Equal(t, 3, out.Batches[1].LineCount)
	require.Equal(t, 1, out.Batches[2].LineCount)
	require.True(t, out.EOF)
}

// Filling MaxBatches part-way through the object is not EOF: reporting it
// there would abandon the rest of the day.
func Test_planBatches_maxBatchesIsNotEOF(t *testing.T) {
	body := ndjson(10)
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

// Blank lines are not records, but they are bytes, so they must land inside a
// batch's range rather than between two of them.
func Test_planBatches_blankLinesStayInsideBatches(t *testing.T) {
	body := "{\"a\":1}\n\n\n{\"b\":2}\n{\"c\":3}\n"
	out := plan(t, body, 0, 2, 10)

	require.Len(t, out.Batches, 2)
	require.Equal(t, 2, out.Batches[0].LineCount)
	require.Equal(t, 1, out.Batches[1].LineCount)
	require.Equal(t, out.Batches[0].End, out.Batches[1].Offset)
}

func Test_PlanBatchesActivity_rejectsBadConfig(t *testing.T) {
	_, err := PlanBatchesActivity(t.Context(), PlanBatchesInput{
		S3Key: "b/k", BatchSize: 0, MaxBatches: 1, ChunkSize: 10,
	})
	require.Error(t, err)
}

// A record longer than one chunk -- an ordinary crossref document is -- must
// be stitched across reads rather than truncated into unparsable JSON.
func Test_sliceLines_recordSpansChunks(t *testing.T) {
	long := `{"DOI":"10.1234/long","abstract":"` + strings.Repeat("x", 500) + `"}`
	body := long + "\n" + `{"DOI":"10.1234/short"}` + "\n"
	r := bytes.NewReader([]byte(body))

	out := plan(t, body, 0, 2, 10)
	require.Len(t, out.Batches, 1)

	got, err := sliceLines(out.Batches[0], 8, r)
	require.NoError(t, err)
	require.Equal(t, []string{long, `{"DOI":"10.1234/short"}`}, jsons(got))
}

// The two halves of the split have to describe the same file.
func Test_planAndSlice_roundTripsTheWholeDay(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"plain", ndjson(20)},
		{"unterminated", strings.TrimSuffix(ndjson(20), "\n")},
		{"blank lines", "{\"a\":1}\n\n{\"b\":2}\n\n\n{\"c\":3}\n{\"d\":4}\n"},
		{"crlf", "{\"a\":1}\r\n{\"b\":2}\r\n{\"c\":3}\r\n"},
		{"trailing blanks", ndjson(5) + "\n\n   \n"},
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
					sliced, err := sliceLines(ref, 8, r)
					require.NoError(t, err)
					require.Len(t, sliced, ref.LineCount,
						"batch at %d promised %d lines", ref.Offset, ref.LineCount)
					got = append(got, jsons(sliced)...)
				}

				offset = out.NextOffset
				if out.EOF {
					break
				}
			}

			// Whatever the batching, the records come out exactly once each and
			// in file order.
			var all []string
			_, err := scanLines(r, 0, -1, 4096, func(line string, _ int64) bool {
				all = append(all, line)
				return true
			})
			require.NoError(t, err)
			require.Equal(t, all, got)
		})
	}
}
