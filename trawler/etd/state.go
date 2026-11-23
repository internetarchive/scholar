package etd

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// The state DB is the run's memory: which input lines are finished, and the
// queue of index work that metadata workers hand to index workers. It is a
// local SQLite file so that a crash, a Ctrl-C or a deliberate stop loses
// nothing and the next run picks up where this one left off.
//
// A line is identified by the sha1 of its bytes, so an upstream that edits a
// record produces a new line that is processed afresh, while re-reading an
// unchanged file skips everything already done.

// Line states.
const (
	// LineIndexing: fatcat writes are done and an index job is queued. The
	// reader treats it like done -- the index queue owns the line now.
	LineIndexing = "indexing"
	// LineDone: indexed. Never processed again.
	LineDone = "done"
	// LineSkipped: deliberately not ingested for a reason that might change
	// (a sha1 already in fatcat under a different title). Retried next run.
	LineSkipped = "skipped"
	// LineFailed: ran out of attempts on something that looked transient.
	// Retried next run.
	LineFailed = "failed"
	// LineRejected: never going to be ingested as it stands (withdrawn, no
	// usable PDF). Not retried; a changed upstream record is a new line anyway.
	LineRejected = "rejected"
)

// lineIsSettled reports whether the reader should pass over a line in state s.
func lineIsSettled(s string) bool {
	return s == LineDone || s == LineIndexing || s == LineRejected
}

// Index job states.
const (
	jobPending = "pending"
	jobClaimed = "claimed"
	jobDone    = "done"
	jobFailed  = "failed"
)

// IndexJob is everything an index worker needs to finish a line.
type IndexJob struct {
	LineHash  string
	ReleaseID string
	// FileIDs are every file the line touched, all of which get a fatcat_file
	// doc. The fulltext doc is built from BestFileID only.
	FileIDs    []string
	BestFileID string
	// BestFetchURL is re-fetched for GROBID rather than spooled to disk.
	BestFetchURL string
	Attempts     int
}

type StateDB struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS lines (
	hash       TEXT PRIMARY KEY,
	key        TEXT NOT NULL,
	state      TEXT NOT NULL,
	release_id TEXT,
	detail     TEXT,
	updated    INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS lines_state ON lines(state);
CREATE TABLE IF NOT EXISTS index_jobs (
	hash           TEXT PRIMARY KEY,
	release_id     TEXT NOT NULL,
	file_ids       TEXT NOT NULL,
	best_file_id   TEXT NOT NULL,
	best_fetch_url TEXT NOT NULL,
	state          TEXT NOT NULL,
	attempts       INTEGER NOT NULL DEFAULT 0,
	not_before     INTEGER NOT NULL DEFAULT 0,
	last_error     TEXT,
	updated        INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS index_jobs_state ON index_jobs(state, not_before);
`

// OpenStateDB opens (creating if need be) the state DB at path. Claims left
// behind by a run that died are released back to pending.
func OpenStateDB(path string) (*StateDB, error) {
	dsn := "file:" + path +
		"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(30000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("could not open state db %s: %w", path, err)
	}
	// SQLite has one writer regardless; a single connection turns would-be
	// SQLITE_BUSY errors into queueing in database/sql, which is what we want
	// with dozens of goroutines writing small rows.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("could not create state db schema: %w", err)
	}
	if _, err := db.Exec(`UPDATE index_jobs SET state = ? WHERE state = ?`, jobPending, jobClaimed); err != nil {
		db.Close()
		return nil, fmt.Errorf("could not release stale index claims: %w", err)
	}
	return &StateDB{db: db}, nil
}

func (s *StateDB) Close() error { return s.db.Close() }

// LineState returns the recorded state for a line hash, or "" if unseen.
func (s *StateDB) LineState(ctx context.Context, hash string) (string, error) {
	var st string
	err := s.db.QueryRowContext(ctx, `SELECT state FROM lines WHERE hash = ?`, hash).Scan(&st)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return st, err
}

// SetLine records a line's outcome.
func (s *StateDB) SetLine(ctx context.Context, hash, key, state, releaseID, detail string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO lines (hash, key, state, release_id, detail, updated) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(hash) DO UPDATE SET state = excluded.state, release_id = excluded.release_id,
			detail = excluded.detail, updated = excluded.updated`,
		hash, key, state, releaseID, detail, time.Now().Unix())
	return err
}

// FinishMetadata marks a line's fatcat work complete and queues its index job,
// atomically: a line is never "indexing" without a job, nor has a job while
// still eligible to be reprocessed.
func (s *StateDB) FinishMetadata(ctx context.Context, key, detail string, job IndexJob) error {
	fids, err := json.Marshal(job.FileIDs)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO lines (hash, key, state, release_id, detail, updated) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(hash) DO UPDATE SET state = excluded.state, release_id = excluded.release_id,
			detail = excluded.detail, updated = excluded.updated`,
		job.LineHash, key, LineIndexing, job.ReleaseID, detail, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO index_jobs (hash, release_id, file_ids, best_file_id, best_fetch_url, state, attempts, updated)
		VALUES (?, ?, ?, ?, ?, ?, 0, ?)
		ON CONFLICT(hash) DO UPDATE SET release_id = excluded.release_id, file_ids = excluded.file_ids,
			best_file_id = excluded.best_file_id, best_fetch_url = excluded.best_fetch_url,
			state = excluded.state, attempts = 0, not_before = 0, last_error = NULL, updated = excluded.updated`,
		job.LineHash, job.ReleaseID, string(fids), job.BestFileID, job.BestFetchURL, jobPending, now); err != nil {
		return err
	}
	return tx.Commit()
}

// ClaimJobs hands out up to n pending jobs that are due, marking them claimed.
func (s *StateDB) ClaimJobs(ctx context.Context, n int) ([]IndexJob, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `
		SELECT hash, release_id, file_ids, best_file_id, best_fetch_url, attempts
		FROM index_jobs WHERE state = ? AND not_before <= ? LIMIT ?`,
		jobPending, time.Now().Unix(), n)
	if err != nil {
		return nil, err
	}
	var jobs []IndexJob
	for rows.Next() {
		var j IndexJob
		var fids string
		if err := rows.Scan(&j.LineHash, &j.ReleaseID, &fids, &j.BestFileID, &j.BestFetchURL, &j.Attempts); err != nil {
			rows.Close()
			return nil, err
		}
		if err := json.Unmarshal([]byte(fids), &j.FileIDs); err != nil {
			rows.Close()
			return nil, fmt.Errorf("corrupt file_ids for %s: %w", j.LineHash, err)
		}
		jobs = append(jobs, j)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, j := range jobs {
		if _, err := tx.ExecContext(ctx, `UPDATE index_jobs SET state = ? WHERE hash = ?`, jobClaimed, j.LineHash); err != nil {
			return nil, err
		}
	}
	return jobs, tx.Commit()
}

// FinishJob marks an index job done and its line done.
func (s *StateDB) FinishJob(ctx context.Context, hash string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	if _, err := tx.ExecContext(ctx, `UPDATE index_jobs SET state = ?, updated = ? WHERE hash = ?`, jobDone, now, hash); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE lines SET state = ?, updated = ? WHERE hash = ?`, LineDone, now, hash); err != nil {
		return err
	}
	return tx.Commit()
}

// FailJob records a failed index attempt. Below maxAttempts the job goes back
// to pending after backoff; at it, the job is parked as failed until
// RequeueFailedJobs.
func (s *StateDB) FailJob(ctx context.Context, hash string, attempts, maxAttempts int, backoff time.Duration, cause error) (parked bool, err error) {
	state := jobPending
	if attempts >= maxAttempts {
		state = jobFailed
	}
	_, err = s.db.ExecContext(ctx, `
		UPDATE index_jobs SET state = ?, attempts = ?, not_before = ?, last_error = ?, updated = ? WHERE hash = ?`,
		state, attempts, time.Now().Add(backoff).Unix(), cause.Error(), time.Now().Unix(), hash)
	return state == jobFailed, err
}

// RequeueFailedJobs puts parked jobs back in the queue with a fresh attempt
// budget, for an --index-only pass after whatever broke has been fixed.
func (s *StateDB) RequeueFailedJobs(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE index_jobs SET state = ?, attempts = 0, not_before = 0 WHERE state = ?`, jobPending, jobFailed)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// QueueCounts reports how many index jobs are in each state.
func (s *StateDB) QueueCounts(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT state, count(*) FROM index_jobs GROUP BY state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return nil, err
		}
		out[st] = n
	}
	return out, rows.Err()
}
