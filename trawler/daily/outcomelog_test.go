package daily

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
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

func Test_appendOutcome_writesHeaderThenRows(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 9, 4, 17, 30, 0, 0, time.UTC)

	require.NoError(t, appendOutcome(dir, "wf", Outcome{
		Upstream:   "crossref",
		ExtID:      "doi:10.1234/ok",
		ReleaseID:  "0199-rel",
		SeedURL:    "https://example.org/article/1",
		Status:     StatusOK,
		Detail:     "created file, indexed fulltext",
		FileID:     "0199-file",
		WaybackURL: "https://web.archive.org/web/20260904173000id_/https://example.org/a.pdf",
		At:         at,
	}))
	require.NoError(t, appendOutcome(dir, "wf", Outcome{
		Upstream: "crossref",
		ExtID:    "doi:10.1234/nope",
		Status:   StatusSkip,
		Detail:   "doi-prefix-blocked:10.1234/",
		At:       at,
	}))

	lines := readLog(t, dir, "wf")
	require.Len(t, lines, 3)
	require.Equal(t,
		"upstream\text_id\trelease_id\tseed_url\tstatus\tdetail\tfile_id\twayback_url\tcrawled_at",
		lines[0])

	// A success pairs the record with the PDF's replay URL, which is the whole
	// point of the log.
	ok := strings.Split(lines[1], "\t")
	require.Len(t, ok, 9)
	require.Equal(t, "crossref", ok[0])
	require.Equal(t, "doi:10.1234/ok", ok[1])
	require.Equal(t, "0199-rel", ok[2])
	require.Equal(t, "https://example.org/article/1", ok[3])
	require.Equal(t, "ok", ok[4])
	require.Equal(t, "created file, indexed fulltext", ok[5])
	require.Equal(t, "0199-file", ok[6])
	require.Equal(t,
		"https://web.archive.org/web/20260904173000id_/https://example.org/a.pdf", ok[7])
	require.Equal(t, "2026-09-04T17:30:00Z", ok[8])

	skip := strings.Split(lines[2], "\t")
	require.Equal(t, "skip", skip[4])
	require.Equal(t, "", skip[7], "a skip must not carry a wayback pdf url")
}

// The header goes in once, not on every append and not again on reopen.
func Test_appendOutcome_headerOnlyOnce(t *testing.T) {
	dir := t.TempDir()
	for i := range 4 {
		require.NoError(t, appendOutcome(dir, "wf", Outcome{
			ExtID:  "doi:10.1234/" + string(rune('a'+i)),
			Status: StatusSkip,
		}))
	}
	lines := readLog(t, dir, "wf")
	require.Len(t, lines, 5)
	require.Equal(t, 1, strings.Count(strings.Join(lines, "\n"), "upstream\text_id"))
}

// A detail carrying a tab or newline would silently shift every later column,
// so those are flattened to spaces. Error text is where this actually bites:
// a wrapped HTTP error can carry anything.
func Test_appendOutcome_sanitisesFields(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, appendOutcome(dir, "wf", Outcome{
		ExtID:  "doi:10.1234/x",
		Status: StatusError,
		Detail: "fatcat said:\tno\nand also\rthis",
	}))
	lines := readLog(t, dir, "wf")
	require.Len(t, lines, 2, "an unsanitised newline would have split the row")
	require.Len(t, strings.Split(lines[1], "\t"), 9)
	require.Contains(t, lines[1], "fatcat said: no and also this")
}

func Test_appendOutcome_emptyDirDisables(t *testing.T) {
	require.NoError(t, appendOutcome("", "wf", Outcome{ExtID: "doi:1", Status: StatusOK}))
}

func Test_appendOutcome_createsDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "deeper")
	require.NoError(t, appendOutcome(dir, "wf", Outcome{ExtID: "doi:1", Status: StatusOK}))
	require.Len(t, readLog(t, dir, "wf"), 2)
}

// A log we cannot write must surface as an error rather than be swallowed --
// losing the record of what happened is the thing this log exists to prevent.
//
// Both cases below are wedged structurally (a non-directory in the path,
// ENOTDIR; the log path already being a directory, EISDIR) rather than by
// permission bits. Mode 0500 would prove nothing in CI, which runs the suite
// as root in the golang image: uid 0 bypasses the permission check and the
// write simply succeeds.
func Test_appendOutcome_reportsUnwritableDir(t *testing.T) {
	t.Run("dir cannot be created", func(t *testing.T) {
		base := t.TempDir()
		notADir := filepath.Join(base, "in-the-way")
		require.NoError(t, os.WriteFile(notADir, []byte("i am a file"), 0o644))

		err := appendOutcome(filepath.Join(notADir, "sub"), "wf", Outcome{ExtID: "doi:1"})
		require.Error(t, err)
		require.Contains(t, err.Error(), "outcome log dir")
		require.ErrorIs(t, err, syscall.ENOTDIR)
		require.NotErrorIs(t, err, syscall.EACCES)
	})

	t.Run("file cannot be opened", func(t *testing.T) {
		dir := t.TempDir()
		// Occupy the exact path the log wants with a directory.
		require.NoError(t, os.Mkdir(filepath.Join(dir, logFileName("wf")), 0o755))

		err := appendOutcome(dir, "wf", Outcome{ExtID: "doi:1"})
		require.Error(t, err)
		require.Contains(t, err.Error(), "outcome log")
		require.ErrorIs(t, err, syscall.EISDIR)
		require.NotErrorIs(t, err, syscall.EACCES)
	})
}

func Test_appendOutcome_defaultsTimestamp(t *testing.T) {
	dir := t.TempDir()
	before := time.Now().UTC().Add(-time.Second)
	require.NoError(t, appendOutcome(dir, "wf", Outcome{ExtID: "doi:1", Status: StatusOK}))
	got, err := time.Parse(time.RFC3339, strings.Split(readLog(t, dir, "wf")[1], "\t")[8])
	require.NoError(t, err)
	require.False(t, got.Before(before))
}

// Concurrent lines in one worker append to the same file; every row must
// survive intact.
func Test_appendOutcome_concurrentAppends(t *testing.T) {
	dir := t.TempDir()
	const n = 50
	done := make(chan error, n)
	for range n {
		go func() {
			done <- appendOutcome(dir, "wf", Outcome{
				Upstream: "crossref",
				ExtID:    "doi:10.1234/x",
				Status:   StatusOK,
				Detail:   "created file",
			})
		}()
	}
	for range n {
		require.NoError(t, <-done)
	}
	lines := readLog(t, dir, "wf")
	require.Len(t, lines, n+1)
	for _, l := range lines[1:] {
		require.Len(t, strings.Split(l, "\t"), 9)
	}
}

func Test_logFileName(t *testing.T) {
	require.Equal(t, "crossref_daily_20260520.tsv", logFileName("crossref_daily_20260520"))
	require.Equal(t, "a_b_c.tsv", logFileName("a/b:c"))
	require.Equal(t, "daily.tsv", logFileName(""))
	require.Equal(t, "daily.tsv", logFileName("///"))
}
