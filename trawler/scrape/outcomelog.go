package scrape

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"git.archive.org/webgroup/scholar/trawler/crawling"
)

// The per-URL outcome log: one TSV row per seed URL saying whether we got a PDF
// into Wayback and, when we did, where it landed.
//
// It is written on the worker host rather than shipped anywhere, so with
// several workers on the queue each holds the rows for the URLs it happened to
// crawl. Concatenating the files gives the whole run.

// Status values. These are the whole vocabulary of the log on purpose: the
// question it answers is "did this URL end up as a PDF in Wayback, and where".
const (
	// StatusOK means the crawl ended on a PDF and WaybackURL points at it.
	StatusOK = "ok"
	// StatusFail means the crawl finished but did not reach a PDF. In most of
	// these SPN still captured something, so the URL is in Wayback even though
	// no PDF came of it -- Reason says what stopped us.
	StatusFail = "fail"
	// StatusError means the crawl never produced a verdict: it errored and
	// exhausted its retries, so we abandoned the URL.
	StatusError = "error"
)

// outcomeColumns is the header, and the field order of every row.
var outcomeColumns = []string{"seed_url", "status", "wayback_url", "reason", "crawled_at"}

// Outcome is one row of the log.
type Outcome struct {
	SeedURL    string
	Status     string
	WaybackURL string
	Reason     string
	At         time.Time
}

func (o Outcome) row() string {
	at := o.At
	if at.IsZero() {
		at = time.Now()
	}
	return strings.Join([]string{
		tsvField(o.SeedURL),
		tsvField(o.Status),
		tsvField(o.WaybackURL),
		tsvField(o.Reason),
		at.UTC().Format(time.RFC3339),
	}, "\t") + "\n"
}

// tsvField makes a value safe to drop in a tab-separated column. Seed URLs
// cannot contain whitespace, but a crawler fail reason or an error string can
// carry anything, and one stray tab would silently shift every later column.
func tsvField(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '\t', '\n', '\r':
			return ' '
		}
		return r
	}, s)
}

// logMu serialises appends from the crawls running concurrently in one worker
// process. Rows are written with a single Write to an O_APPEND handle, so
// separate worker processes sharing a path stay line-safe too.
var logMu sync.Mutex

// appendOutcome writes one row to dir/<name>.tsv, creating the file with a
// header if it is not there yet. An empty dir disables logging.
func appendOutcome(dir, name string, o Outcome) error {
	if dir == "" {
		return nil
	}

	logMu.Lock()
	defer logMu.Unlock()

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("could not create outcome log dir %s: %w", dir, err)
	}

	path := filepath.Join(dir, logFileName(name))
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("could not open outcome log %s: %w", path, err)
	}
	defer f.Close()

	body := o.row()
	// A zero-length file is one we just created (or an empty one left behind),
	// so it needs the header before its first row.
	if st, err := f.Stat(); err == nil && st.Size() == 0 {
		body = strings.Join(outcomeColumns, "\t") + "\n" + body
	}

	if _, err := f.WriteString(body); err != nil {
		return fmt.Errorf("could not append to outcome log %s: %w", path, err)
	}
	return nil
}

// logFileName turns a workflow ID into a filename. Workflow IDs are already
// constrained by WorkflowID(), but this activity will happily log for a
// workflow started by hand, so the sanitising is not redundant.
func logFileName(name string) string {
	if name == "" {
		name = "scrape"
	}
	safe := unsafeIDChars.ReplaceAllString(name, "_")
	safe = strings.Trim(safe, "_")
	if safe == "" {
		safe = "scrape"
	}
	return safe + ".tsv"
}

// outcomeFor turns a finished crawl into its log row.
func outcomeFor(seedURL string, res crawling.CrawlResult, at time.Time) Outcome {
	if res.Success {
		return Outcome{
			SeedURL:    seedURL,
			Status:     StatusOK,
			WaybackURL: res.SnapshotUrl,
			At:         at,
		}
	}
	// SnapshotUrl is only set when the crawl landed on a PDF, but leave nothing
	// to chance: a fail row must never carry a URL that implies we captured one.
	reason := res.FailReason
	if reason == "" {
		reason = "unknown"
	}
	return Outcome{
		SeedURL: seedURL,
		Status:  StatusFail,
		Reason:  reason,
		At:      at,
	}
}
