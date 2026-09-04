package scrape

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"git.archive.org/webgroup/scholar/trawler/crawling"
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
		SeedURL:    "https://example.org/thesis/1",
		Status:     StatusOK,
		WaybackURL: "https://web.archive.org/web/20260904173000id_/https://example.org/a.pdf",
		At:         at,
	}))
	require.NoError(t, appendOutcome(dir, "wf", Outcome{
		SeedURL: "https://example.org/thesis/2",
		Status:  StatusFail,
		Reason:  "dead-end",
		At:      at,
	}))

	lines := readLog(t, dir, "wf")
	require.Len(t, lines, 3)
	require.Equal(t, "seed_url\tstatus\twayback_url\treason\tcrawled_at", lines[0])

	// A success pairs the seed URL with the PDF's replay URL, which is the whole
	// point of the log.
	ok := strings.Split(lines[1], "\t")
	require.Len(t, ok, 5)
	require.Equal(t, "https://example.org/thesis/1", ok[0])
	require.Equal(t, "ok", ok[1])
	require.Equal(t, "https://web.archive.org/web/20260904173000id_/https://example.org/a.pdf", ok[2])
	require.Equal(t, "", ok[3])
	require.Equal(t, "2026-09-04T17:30:00Z", ok[4])

	fail := strings.Split(lines[2], "\t")
	require.Equal(t, "fail", fail[1])
	require.Equal(t, "", fail[2], "a fail must not carry a wayback pdf url")
	require.Equal(t, "dead-end", fail[3])
}

// The header goes in once, not on every append and not again on reopen.
func Test_appendOutcome_headerOnlyOnce(t *testing.T) {
	dir := t.TempDir()
	for i := range 4 {
		require.NoError(t, appendOutcome(dir, "wf", Outcome{
			SeedURL: "https://example.org/" + string(rune('a'+i)),
			Status:  StatusFail,
		}))
	}
	lines := readLog(t, dir, "wf")
	require.Len(t, lines, 5)
	require.Equal(t, 1, strings.Count(strings.Join(lines, "\n"), "seed_url\tstatus"))
}

// A reason carrying a tab or newline would silently shift every later column,
// so those are flattened to spaces.
func Test_appendOutcome_sanitisesFields(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, appendOutcome(dir, "wf", Outcome{
		SeedURL: "https://example.org/x",
		Status:  StatusError,
		Reason:  "spn said:\tno\nand also\rthis",
	}))
	lines := readLog(t, dir, "wf")
	require.Len(t, lines, 2, "an unsanitised newline would have split the row")
	require.Equal(t, 5, len(strings.Split(lines[1], "\t")))
	require.Contains(t, lines[1], "spn said: no and also this")
}

func Test_appendOutcome_emptyDirDisables(t *testing.T) {
	require.NoError(t, appendOutcome("", "wf", Outcome{SeedURL: "u", Status: StatusOK}))
}

func Test_appendOutcome_createsDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "deeper")
	require.NoError(t, appendOutcome(dir, "wf", Outcome{SeedURL: "u", Status: StatusOK}))
	require.Len(t, readLog(t, dir, "wf"), 2)
}

// A log we cannot write must surface as an error rather than be swallowed --
// losing the record of what happened is the thing this log exists to prevent.
//
// Both cases below are wedged structurally (a non-directory in the path,
// ENOTDIR; the log path already being a directory, EISDIR) rather than by
// permission bits. Mode 0500 would prove nothing in CI, which runs the suite as
// root in the golang image: uid 0 bypasses the permission check and the write
// simply succeeds.
func Test_appendOutcome_reportsUnwritableDir(t *testing.T) {
	t.Run("dir cannot be created", func(t *testing.T) {
		base := t.TempDir()
		notADir := filepath.Join(base, "in-the-way")
		require.NoError(t, os.WriteFile(notADir, []byte("i am a file"), 0o644))

		err := appendOutcome(filepath.Join(notADir, "sub"), "wf", Outcome{SeedURL: "u"})
		require.Error(t, err)
		require.Contains(t, err.Error(), "outcome log dir")
		// Assert the *kind* of failure, not just that one happened: ENOTDIR is
		// structural, so this stays true as root. An EACCES-based setup would
		// pass locally and vanish in CI.
		require.ErrorIs(t, err, syscall.ENOTDIR)
		require.NotErrorIs(t, err, syscall.EACCES)
	})

	t.Run("file cannot be opened", func(t *testing.T) {
		dir := t.TempDir()
		// Occupy the exact path the log wants with a directory.
		require.NoError(t, os.Mkdir(filepath.Join(dir, logFileName("wf")), 0o755))

		err := appendOutcome(dir, "wf", Outcome{SeedURL: "u"})
		require.Error(t, err)
		require.Contains(t, err.Error(), "outcome log")
		require.ErrorIs(t, err, syscall.EISDIR)
		require.NotErrorIs(t, err, syscall.EACCES)
	})
}

func Test_appendOutcome_defaultsTimestamp(t *testing.T) {
	dir := t.TempDir()
	before := time.Now().UTC().Add(-time.Second)
	require.NoError(t, appendOutcome(dir, "wf", Outcome{SeedURL: "u", Status: StatusOK}))
	got, err := time.Parse(time.RFC3339, strings.Split(readLog(t, dir, "wf")[1], "\t")[4])
	require.NoError(t, err)
	require.False(t, got.Before(before))
}

// Concurrent crawls in one worker append to the same file; every row must
// survive intact.
func Test_appendOutcome_concurrentAppends(t *testing.T) {
	dir := t.TempDir()
	const n = 50
	done := make(chan error, n)
	for i := range n {
		go func() {
			done <- appendOutcome(dir, "wf", Outcome{
				SeedURL:    "https://example.org/doc",
				Status:     StatusOK,
				WaybackURL: "https://web.archive.org/web/1id_/https://example.org/doc.pdf",
			})
		}()
		_ = i
	}
	for range n {
		require.NoError(t, <-done)
	}
	lines := readLog(t, dir, "wf")
	require.Len(t, lines, n+1)
	for _, l := range lines[1:] {
		require.Len(t, strings.Split(l, "\t"), 5)
	}
}

func Test_logFileName(t *testing.T) {
	require.Equal(t, "scrape_to_wayback_bucket_seeds.txt.tsv",
		logFileName("scrape_to_wayback_bucket_seeds.txt"))
	require.Equal(t, "a_b_c.tsv", logFileName("a/b:c"))
	require.Equal(t, "scrape.tsv", logFileName(""))
	require.Equal(t, "scrape.tsv", logFileName("///"))
}

func Test_outcomeFor(t *testing.T) {
	at := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)

	ok := outcomeFor("https://seed/1", crawling.CrawlResult{
		Success:     true,
		SnapshotUrl: "https://web.archive.org/web/1id_/https://seed/1.pdf",
	}, at)
	require.Equal(t, StatusOK, ok.Status)
	require.Equal(t, "https://web.archive.org/web/1id_/https://seed/1.pdf", ok.WaybackURL)
	require.Empty(t, ok.Reason)

	fail := outcomeFor("https://seed/2", crawling.CrawlResult{FailReason: "blocklist"}, at)
	require.Equal(t, StatusFail, fail.Status)
	require.Empty(t, fail.WaybackURL)
	require.Equal(t, "blocklist", fail.Reason)

	// A crawler that stops without saying why still gets a usable row rather
	// than a blank reason column.
	bare := outcomeFor("https://seed/3", crawling.CrawlResult{}, at)
	require.Equal(t, StatusFail, bare.Status)
	require.Equal(t, "unknown", bare.Reason)

	// Defensive: a SnapshotUrl on a non-success result must not leak into the
	// log as if we had captured a PDF.
	odd := outcomeFor("https://seed/4", crawling.CrawlResult{
		FailReason:  "dead-end",
		SnapshotUrl: "https://web.archive.org/web/1id_/https://seed/4",
	}, at)
	require.Empty(t, odd.WaybackURL)
}
