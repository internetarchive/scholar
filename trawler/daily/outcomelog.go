package daily

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/spf13/viper"
)

// The per-line outcome log: one TSV row per ndjson record saying what we
// decided about it and what came of it.
//
// It exists because batching took the per-line view away. When every line was
// its own activity, "why did this DOI never get a PDF" was answerable from the
// Temporal UI; a batch of hundreds of lines is one history event, so the
// per-line story has to be written down somewhere else. This is that somewhere.
//
// It is written on the worker host rather than shipped anywhere, so with
// several workers on the queue each holds the rows for the lines it happened to
// work. Concatenating the files gives the whole day.
//
// Rows are appended as a batch's watermark advances, so within one batch they
// come out in file order even though the lines finish out of order. A record
// can appear more than once: a batch that dies between checkpoints is resumed
// from the last checkpoint that reached Temporal, and the lines it had already
// worked past that point are worked -- and logged -- again. Analysis should
// treat a repeated ext_id as one line rather than assume the file is unique on
// it.

// Status values.
const (
	// StatusOK means the line ended with a PDF in fatcat and/or an index
	// document. Detail says which.
	StatusOK = "ok"
	// StatusSkip means we deliberately passed on the line before ever
	// crawling: the upstream record was not worth a release, the release was
	// not paperlike, its DOI prefix is blocked, it has no fulltext URLs, or it
	// already has files. This is the common case and not a problem.
	StatusSkip = "skip"
	// StatusFail means we tried and came away with nothing: no URL for the
	// release reached a PDF, or the PDF reached GROBID and GROBID made nothing
	// of it. Detail says which.
	StatusFail = "fail"
	// StatusError means the line never reached a verdict: it errored and
	// exhausted its retries, so we abandoned it.
	StatusError = "error"
)

// outcomeColumns is the header, and the field order of every row.
var outcomeColumns = []string{
	"upstream", "ext_id", "release_id", "seed_url", "status", "detail",
	"file_id", "wayback_url", "crawled_at",
}

// Outcome is one row of the log.
type Outcome struct {
	Upstream string
	// ExtID is the external identifier the upstream record carried, as
	// "type:value" -- the only handle on a line that means anything outside
	// this run.
	ExtID     string
	ReleaseID string
	// SeedURL is the fulltext URL the crawl ended on, or the last one tried.
	SeedURL string
	Status  string
	Detail  string
	FileID  string
	// WaybackURL is the replay URL of the captured PDF, set only on a success.
	WaybackURL string
	At         time.Time
}

func (o Outcome) row() string {
	at := o.At
	if at.IsZero() {
		at = time.Now()
	}
	return strings.Join([]string{
		tsvField(o.Upstream),
		tsvField(o.ExtID),
		tsvField(o.ReleaseID),
		tsvField(o.SeedURL),
		tsvField(o.Status),
		tsvField(o.Detail),
		tsvField(o.FileID),
		tsvField(o.WaybackURL),
		at.UTC().Format(time.RFC3339),
	}, "\t") + "\n"
}

// tsvField makes a value safe to drop in a tab-separated column. Identifiers
// and URLs cannot contain whitespace, but a detail string carries error text
// and can hold anything, and one stray tab would silently shift every later
// column.
func tsvField(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '\t', '\n', '\r':
			return ' '
		}
		return r
	}, s)
}

// logMu serialises appends from the lines being worked concurrently in one
// worker process. Rows are written with a single Write to an O_APPEND handle,
// so separate worker processes sharing a path stay line-safe too.
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
	safe := strings.Trim(unsafeIDChars.ReplaceAllString(name, "_"), "_")
	if safe == "" {
		safe = "daily"
	}
	return safe + ".tsv"
}

// outcomeLogDir is where this worker writes outcome rows. It is read per
// activity rather than snapshotted into the workflow because it is a property
// of the host, not of the run.
func outcomeLogDir() string {
	return viper.GetString("daily.log_dir")
}
