package etd

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Per-line verdicts written to the outcome TSV.
const (
	StatusCreated  = "created"      // new release (and files)
	StatusMatchID  = "matched-id"   // existing release found by external ID
	StatusMatchSha = "matched-sha1" // existing release found through a file's sha1 and title
	StatusSkip     = "skip"         // deliberately not ingested; see detail
	StatusReject   = "reject"       // never ingestible as it stands; see detail
	StatusFail     = "fail"         // metadata work failed; see detail
	StatusIndexed  = "indexed"      // index job finished
	StatusIdxFail  = "index-fail"   // index attempt failed; detail says whether parked
	StatusMismatch = "sha1-title-mismatch"
)

var outcomeColumns = []string{
	"at", "stage", "status", "key", "line_hash", "release_id", "file_ids", "score", "detail",
}

// Outcome is one row of the outcome TSV.
type Outcome struct {
	Stage     string // "metadata" or "index"
	Status    string
	Key       string
	LineHash  string
	ReleaseID string
	FileIDs   []string
	// Score is the best title similarity, where one was computed; -1 if not.
	Score  float64
	Detail string
}

func (o Outcome) row() string {
	score := ""
	if o.Score >= 0 {
		score = strconv.FormatFloat(o.Score, 'f', 3, 64)
	}
	return strings.Join([]string{
		time.Now().UTC().Format(time.RFC3339),
		tsvField(o.Stage),
		tsvField(o.Status),
		tsvField(o.Key),
		tsvField(o.LineHash),
		tsvField(o.ReleaseID),
		tsvField(strings.Join(o.FileIDs, ",")),
		score,
		tsvField(o.Detail),
	}, "\t") + "\n"
}

func tsvField(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '\t', '\n', '\r':
			return ' '
		}
		return r
	}, s)
}

// OutcomeLog appends rows to one TSV for the whole run. It holds the file open:
// this is one long-lived local process, not a fleet of activities.
type OutcomeLog struct {
	mu sync.Mutex
	f  *os.File
}

// OpenOutcomeLog opens dir/name.tsv for appending, writing the header if the
// file is new. An empty dir disables logging.
func OpenOutcomeLog(dir, name string) (*OutcomeLog, error) {
	if dir == "" {
		return &OutcomeLog{}, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("could not create outcome log dir %s: %w", dir, err)
	}
	path := filepath.Join(dir, name+".tsv")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("could not open outcome log %s: %w", path, err)
	}
	if st, err := f.Stat(); err == nil && st.Size() == 0 {
		if _, err := f.WriteString(strings.Join(outcomeColumns, "\t") + "\n"); err != nil {
			f.Close()
			return nil, err
		}
	}
	return &OutcomeLog{f: f}, nil
}

func (l *OutcomeLog) Write(o Outcome) {
	if l == nil || l.f == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	// A lost log row is not worth stopping a nine-million-line run for; the
	// state DB is the record that matters.
	l.f.WriteString(o.row())
}

func (l *OutcomeLog) Close() error {
	if l == nil || l.f == nil {
		return nil
	}
	return l.f.Close()
}
