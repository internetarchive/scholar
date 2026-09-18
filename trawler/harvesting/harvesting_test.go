package harvesting

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// line is one thing ScanLines yielded: the text and the byte offset just past
// its newline, which is where a resume picks up.
type line struct {
	text string
	end  int64
}

// scan collects everything ScanLines yields over a range.
func scan(t *testing.T, body string, start, end int64, chunk int) ([]line, bool) {
	t.Helper()
	var got []line
	consumed, err := ScanLines(bytes.NewReader([]byte(body)), start, end, chunk,
		func(s string, lineEnd int64) bool {
			got = append(got, line{text: s, end: lineEnd})
			return true
		})
	require.NoError(t, err)
	return got, consumed
}

func texts(ls []line) []string {
	out := make([]string, 0, len(ls))
	for _, l := range ls {
		out = append(out, l.text)
	}
	return out
}

func Test_ScanLines_readsWholeObject(t *testing.T) {
	got, consumed := scan(t, "aaaaaaaaa\nbbbbbbbbb\nccccccccc\n", 0, -1, 4096)
	require.Equal(t, []string{"aaaaaaaaa", "bbbbbbbbb", "ccccccccc"}, texts(got))
	require.True(t, consumed)
	require.Equal(t, int64(10), got[0].end)
	require.Equal(t, int64(20), got[1].end)
	require.Equal(t, int64(30), got[2].end)
}

func Test_ScanLines_skipsBlankLinesAndTrimsCR(t *testing.T) {
	got, consumed := scan(t, "a\r\n\n   \nb\r\n", 0, -1, 4096)
	require.Equal(t, []string{"a", "b"}, texts(got))
	require.True(t, consumed)
}

// A final line with no trailing newline is still a record. Dropping it would
// silently abandon the last record of every object that does not end in one.
func Test_ScanLines_unterminatedFinalLine(t *testing.T) {
	got, consumed := scan(t, "aaaaaaaaa\nbbbbbbbbb", 0, -1, 4096)
	require.Equal(t, []string{"aaaaaaaaa", "bbbbbbbbb"}, texts(got))
	require.True(t, consumed)
	require.Equal(t, int64(19), got[1].end)
}

// A line longer than one chunk -- which an ordinary crossref document is --
// must be stitched across reads, not truncated.
func Test_ScanLines_lineSpansChunks(t *testing.T) {
	long := strings.Repeat("x", 200)
	got, consumed := scan(t, long+"\nb\n", 0, -1, 8)
	require.Equal(t, []string{long, "b"}, texts(got))
	require.True(t, consumed)
}

func Test_ScanLines_emptyObject(t *testing.T) {
	got, consumed := scan(t, "", 0, -1, 4096)
	require.Empty(t, got)
	require.True(t, consumed)
}

func Test_ScanLines_offsetPastEnd(t *testing.T) {
	got, consumed := scan(t, "aaaaaaaaa\n", 10, -1, 4096)
	require.Empty(t, got)
	require.True(t, consumed)
}

// A bounded range yields exactly the lines inside it, whatever the chunk size.
// This is the read half of the split: a planner recorded [10, 20) and the
// activity that works it has to see the same single line.
func Test_ScanLines_boundedRange(t *testing.T) {
	body := "aaaaaaaaa\nbbbbbbbbb\nccccccccc\n"
	for _, chunk := range []int{1, 3, 7, 4096} {
		t.Run(fmt.Sprintf("chunk=%d", chunk), func(t *testing.T) {
			got, consumed := scan(t, body, 10, 20, chunk)
			require.Equal(t, []string{"bbbbbbbbb"}, texts(got))
			require.True(t, consumed)
		})
	}
}

// Stopping early is how a planner says "I have enough work", so it has to be
// distinguishable from running out of object -- callers treat the latter as
// EOF and move on.
func Test_ScanLines_stoppingEarlyIsNotConsumed(t *testing.T) {
	var got []string
	consumed, err := ScanLines(bytes.NewReader([]byte("a\nb\n")), 0, -1, 4096,
		func(s string, _ int64) bool {
			got = append(got, s)
			return false
		})
	require.NoError(t, err)
	require.False(t, consumed)
	require.Equal(t, []string{"a"}, got)
}

func Test_ScanLines_rejectsBadChunkSize(t *testing.T) {
	_, err := ScanLines(bytes.NewReader(nil), 0, -1, 0, func(string, int64) bool { return true })
	require.Error(t, err)
}
