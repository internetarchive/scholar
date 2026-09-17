package periodic

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func readLog(t *testing.T, dir, name string) []string {
	t.Helper()
	bs, err := os.ReadFile(filepath.Join(dir, logFileName(name)))
	require.NoError(t, err)
	return strings.Split(strings.TrimRight(string(bs), "\n"), "\n")
}

func Test_appendOutcome_writesHeaderOnce(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)

	for _, o := range []Outcome{
		{ItemID: "ITEM-1", Sha1B32: "AAAA", URL: "http://a/1.pdf", Status: StatusSkip, Detail: "cursed sha1", At: at},
		{ItemID: "ITEM-1", Sha1B32: "BBBB", URL: "http://a/2.pdf", Status: StatusOK, Detail: "created file",
			FileID: "fid-1", ReleaseID: "rid-1", At: at},
	} {
		require.NoError(t, appendOutcome(dir, "periodic_ingest_COLL", o))
	}

	got := readLog(t, dir, "periodic_ingest_COLL")
	require.Len(t, got, 3)
	require.Equal(t, strings.Join(outcomeColumns, "\t"), got[0])
	require.Equal(t,
		"ITEM-1\tAAAA\thttp://a/1.pdf\tskip\tcursed sha1\t\t\t2026-09-15T12:00:00Z", got[1])
	require.Equal(t,
		"ITEM-1\tBBBB\thttp://a/2.pdf\tok\tcreated file\tfid-1\trid-1\t2026-09-15T12:00:00Z", got[2])
}

// A detail string carries error text and can hold anything; one stray tab would
// silently shift every later column.
func Test_appendOutcome_escapesWhitespaceInDetail(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, appendOutcome(dir, "run", Outcome{
		Status: StatusError,
		Detail: "range read failed:\tgot\n503\r",
	}))

	got := readLog(t, dir, "run")
	require.Len(t, got, 2)
	require.Equal(t, len(outcomeColumns)-1, strings.Count(got[1], "\t"))
	require.Contains(t, got[1], "range read failed: got 503")
}

// An empty log dir is how a worker says "do not write this file at all".
func Test_appendOutcome_emptyDirIsDisabled(t *testing.T) {
	require.NoError(t, appendOutcome("", "run", Outcome{Status: StatusOK}))
}

func Test_logFileName(t *testing.T) {
	require.Equal(t, "periodic_ingest_COLL.tsv", logFileName("periodic_ingest_COLL"))
	require.Equal(t, "hand_run.tsv", logFileName("hand/run"))
	require.Equal(t, "periodic.tsv", logFileName(""))
}
