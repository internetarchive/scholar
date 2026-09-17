package periodic

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/spf13/viper"
)

// The per-row outcome log: one TSV row per PDF CDX line saying what we decided
// about it and what came of it.
//
// It exists because batching took the per-line view away. When every line was
// its own activity, "why was this PDF not ingested" was answerable from the
// Temporal UI; a batch of hundreds of lines is one history event, so the
// per-line story has to be written down somewhere else. This is that somewhere.
//
// It is written on the worker host rather than shipped anywhere, so with
// several workers on the queue each holds the rows for the lines it happened to
// ingest. Concatenating the files gives the whole run.
//
// Rows are appended as a batch's watermark advances, so within one batch they
// come out in CDX order even though the lines finish out of order. A sha1 can
// appear more than once: a batch that dies between checkpoints is resumed from
// the last checkpoint that reached Temporal, and the lines it had already
// ingested past that point are ingested -- and logged -- again. Analysis should
// treat a repeated sha1 as one line rather than assume the file is unique on it.

// Status values.
const (
	// StatusOK means the line resulted in a fatcat file record and/or an index
	// document. Detail says which.
	StatusOK = "ok"
	// StatusSkip means we deliberately passed on the line: already known and
	// indexed, denylisted, truncated capture, no external ID, no matching
	// release. Detail says which. This is the common case and not a problem.
	StatusSkip = "skip"
	// StatusFail means the PDF reached GROBID and GROBID made nothing of it.
	StatusFail = "fail"
	// StatusError means the line never reached a verdict: it errored and
	// exhausted its retries, so we abandoned it.
	StatusError = "error"
)

// outcomeColumns is the header, and the field order of every row.
var outcomeColumns = []string{
	"item_id", "sha1_b32", "url", "status", "detail", "file_id", "release_id", "ingested_at",
}

// Outcome is one row of the log.
type Outcome struct {
	ItemID    string
	Sha1B32   string
	URL       string
	Status    string
	Detail    string
	FileID    string
	ReleaseID string
	At        time.Time
}

func (o Outcome) row() string {
	at := o.At
	if at.IsZero() {
		at = time.Now()
	}
	return strings.Join([]string{
		tsvField(o.ItemID),
		tsvField(o.Sha1B32),
		tsvField(o.URL),
		tsvField(o.Status),
		tsvField(o.Detail),
		tsvField(o.FileID),
		tsvField(o.ReleaseID),
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

// logMu serialises appends from the lines being ingested concurrently in one
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
// constrained by WorkflowID(), but these activities will happily log for a
// workflow started by hand, so the sanitising is not redundant.
func logFileName(name string) string {
	safe := strings.Trim(unsafeIDChars.ReplaceAllString(name, "_"), "_")
	if safe == "" {
		safe = "periodic"
	}
	return safe + ".tsv"
}

// outcomeLogDir is where this worker writes outcome rows. It is read per
// activity rather than snapshotted into the workflow because it is a property
// of the host, not of the run.
func outcomeLogDir() string {
	return viper.GetString("periodic.log_dir")
}
