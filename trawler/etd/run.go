package etd

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"git.archive.org/webgroup/scholar/trawler/httpclient"
	"git.archive.org/webgroup/scholar/trawler/pdf"
)

// Config is one run's settings. The command fills it from flags and [etd].
type Config struct {
	// Path is the ndjson (optionally gzipped) to read. Unused with IndexOnly.
	Path string

	Source           string
	Concurrency      int
	IndexConcurrency int
	Threshold        float64
	MaxPDFBytes      int64

	StateDB string
	LogDir  string

	DryRun    bool
	IndexOnly bool
	// Limit stops reading after this many lines have been handed to workers
	// (settled lines passed over do not count). 0 means no limit.
	Limit int

	// LineAttempts is how many times a line is tried before it is recorded
	// as failed; IndexAttempts likewise for an index job.
	LineAttempts  int
	IndexAttempts int
	// MaxConsecutiveFailures aborts the run when this many lines in a row
	// fail: that is fc2 or wayback being down, not bad records.
	MaxConsecutiveFailures int
	ProgressEvery          time.Duration
}

// counters are the run's progress, reported periodically and at the end.
type counters struct {
	read, settled, created, matchedID, matchedSha, skipped, rejected, failed atomic.Int64
	indexed, indexFailed, indexParked                                        atomic.Int64
}

func (c *counters) String() string {
	return fmt.Sprintf("read=%d settled=%d created=%d matched-id=%d matched-sha1=%d skipped=%d rejected=%d failed=%d | indexed=%d index-failed=%d parked=%d",
		c.read.Load(), c.settled.Load(), c.created.Load(), c.matchedID.Load(), c.matchedSha.Load(),
		c.skipped.Load(), c.rejected.Load(), c.failed.Load(),
		c.indexed.Load(), c.indexFailed.Load(), c.indexParked.Load())
}

// inflightLines is the set of line hashes handed to workers and not yet
// recorded, so an input that repeats a line does not process it twice at once.
type inflightLines struct {
	mu sync.Mutex
	m  map[string]bool
}

func (f *inflightLines) start(hash string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.m[hash] {
		return false
	}
	f.m[hash] = true
	return true
}

func (f *inflightLines) done(hash string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.m, hash)
}

type rawLine struct {
	hash  string
	bytes []byte
}

// Run ingests cfg.Path. Metadata workers and index workers run side by side
// and are coupled only through the state DB's job queue, so the index side
// (GROBID, mostly) falling behind never slows fatcat writes. It returns when
// the file is read, every line is settled and the index queue is drained, or
// when ctx is cancelled, in which case work in flight is abandoned unrecorded
// and picked up by the next run.
func Run(ctx context.Context, cfg Config) error {
	if cfg.DryRun && cfg.IndexOnly {
		return errors.New("--dry-run and --index-only together would do nothing")
	}

	statePath := cfg.StateDB
	if cfg.DryRun {
		// A dry run writes nothing durable. It does not skip lines a real run
		// finished, which is the point when sizing up a fresh file.
		statePath = ":memory:"
	}
	state, err := OpenStateDB(statePath)
	if err != nil {
		return err
	}
	defer state.Close()

	logName := "etd-ingest-" + time.Now().UTC().Format("20060102T150405Z")
	if cfg.DryRun {
		logName += "-dryrun"
	}
	outlog, err := OpenOutcomeLog(cfg.LogDir, logName)
	if err != nil {
		return err
	}
	defer outlog.Close()

	fetcher := newWaybackFetcher(cfg.MaxPDFBytes)
	c := &counters{}

	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	var metadataDone atomic.Bool
	jobQueued := make(chan struct{}, 1)
	var wg sync.WaitGroup

	if cfg.IndexOnly {
		metadataDone.Store(true)
		n, err := state.RequeueFailedJobs(ctx)
		if err != nil {
			return fmt.Errorf("could not requeue failed index jobs: %w", err)
		}
		slog.Info("requeued parked index jobs", "count", n)
	} else {
		deps := lineDeps{
			FC:        fc2API{c: httpclient.Shared()},
			Fetch:     fetcher,
			Source:    cfg.Source,
			Threshold: cfg.Threshold,
			DryRun:    cfg.DryRun,
			Locks:     newKeyedLocks(),
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer metadataDone.Store(true)
			if err := runMetadata(ctx, cfg, deps, state, outlog, c, jobQueued); err != nil {
				cancel(err)
			}
		}()
	}

	if !cfg.DryRun {
		processor, err := pdf.NewProcessor(nil)
		if err != nil {
			return fmt.Errorf("could not build pdf processor: %w", err)
		}
		ix := esIndexer{http: httpclient.Shared(), fetch: fetcher, processor: processor}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := runIndex(ctx, cfg, ix, state, outlog, c, &metadataDone, jobQueued); err != nil {
				cancel(err)
			}
		}()
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()

	every := cfg.ProgressEvery
	if every <= 0 {
		every = 30 * time.Second
	}
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-done:
			report(ctx, state, c, "finished")
			if cause := context.Cause(ctx); cause != nil && !errors.Is(cause, context.Canceled) {
				return cause
			}
			return ctx.Err()
		case <-tick.C:
			report(ctx, state, c, "progress")
		}
	}
}

func report(ctx context.Context, state *StateDB, c *counters, what string) {
	q, err := state.QueueCounts(context.WithoutCancel(ctx))
	if err != nil {
		slog.Warn("could not read index queue counts", "err", err)
	}
	slog.Info(what, "counts", c.String(), "index_queue", q)
}

// runMetadata reads the file and runs the metadata worker pool over it.
func runMetadata(ctx context.Context, cfg Config, d lineDeps, state *StateDB, outlog *OutcomeLog, c *counters, jobQueued chan<- struct{}) error {
	f, err := os.Open(cfg.Path)
	if err != nil {
		return err
	}
	defer f.Close()
	var r io.Reader = f
	if strings.HasSuffix(cfg.Path, ".gz") {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return fmt.Errorf("could not open %s as gzip: %w", cfg.Path, err)
		}
		defer gz.Close()
		r = gz
	}

	lines := make(chan rawLine, cfg.Concurrency*2)
	inflight := &inflightLines{m: map[string]bool{}}
	var consecutive atomic.Int64
	var fatal atomic.Value
	var wg sync.WaitGroup
	for range cfg.Concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for l := range lines {
				err := handleLine(ctx, cfg, d, state, outlog, c, l, &consecutive, jobQueued)
				inflight.done(l.hash)
				if err != nil {
					fatal.CompareAndSwap(nil, err)
					return
				}
			}
		}()
	}

	readErr := readLines(ctx, r, cfg.Limit, state, c, lines, inflight, &fatal)
	close(lines)
	wg.Wait()
	if err, _ := fatal.Load().(error); err != nil {
		return err
	}
	return readErr
}

// readLines feeds unsettled lines to the workers until EOF, the limit, a
// worker's fatal error or cancellation.
func readLines(ctx context.Context, r io.Reader, limit int, state *StateDB, c *counters, out chan<- rawLine, inflight *inflightLines, fatal *atomic.Value) error {
	br := bufio.NewReaderSize(r, 1<<20)
	dispatched := 0
	for {
		if ctx.Err() != nil || fatal.Load() != nil {
			return nil
		}
		if limit > 0 && dispatched >= limit {
			return nil
		}
		bs, err := br.ReadBytes('\n')
		if len(bs) > 0 {
			bs = bytes.TrimRight(bs, "\r\n")
			if len(bs) > 0 {
				c.read.Add(1)
				sum := sha1.Sum(bs)
				hash := hex.EncodeToString(sum[:])
				st, serr := state.LineState(ctx, hash)
				if serr != nil {
					return fmt.Errorf("state lookup: %w", serr)
				}
				if lineIsSettled(st) || !inflight.start(hash) {
					// An identical line still being worked on counts as settled:
					// it is the same work, and the state DB will say so shortly.
					c.settled.Add(1)
				} else {
					select {
					case out <- rawLine{hash: hash, bytes: bs}:
						dispatched++
					case <-ctx.Done():
						return nil
					}
				}
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading input: %w", err)
		}
	}
}

// handleLine runs one line to a recorded verdict. It returns an error only to
// stop the run.
func handleLine(ctx context.Context, cfg Config, d lineDeps, state *StateDB, outlog *OutcomeLog, c *counters, l rawLine, consecutive *atomic.Int64, jobQueued chan<- struct{}) error {
	var rec Record
	if err := json.Unmarshal(l.bytes, &rec); err != nil {
		c.rejected.Add(1)
		outlog.Write(Outcome{Stage: "metadata", Status: StatusReject, LineHash: l.hash, Score: -1, Detail: "unparsable: " + err.Error()})
		if !cfg.DryRun {
			return state.SetLine(ctx, l.hash, "", LineRejected, "", "unparsable")
		}
		return nil
	}

	var res lineResult
	var err error
	for attempt := 1; attempt <= cfg.LineAttempts; attempt++ {
		if attempt > 1 {
			if sleepCtx(ctx, time.Duration(attempt-1)*5*time.Second) != nil {
				return nil
			}
		}
		res, err = processLine(ctx, d, l.hash, rec)
		if err == nil || ctx.Err() != nil {
			break
		}
		slog.Warn("line attempt failed", "key", rec.Key, "attempt", attempt, "err", err)
	}
	if ctx.Err() != nil {
		// Interrupted, not failed: record nothing and let the next run redo it.
		return nil
	}

	if err != nil {
		c.failed.Add(1)
		outlog.Write(Outcome{Stage: "metadata", Status: StatusFail, Key: rec.Key, LineHash: l.hash, Score: -1, Detail: err.Error()})
		if !cfg.DryRun {
			if serr := state.SetLine(ctx, l.hash, rec.Key, LineFailed, "", err.Error()); serr != nil {
				return serr
			}
		}
		if n := consecutive.Add(1); cfg.MaxConsecutiveFailures > 0 && n >= int64(cfg.MaxConsecutiveFailures) {
			return fmt.Errorf("%d lines in a row failed, last: %w", n, err)
		}
		return nil
	}
	consecutive.Store(0)

	switch res.Outcome.Status {
	case StatusCreated:
		c.created.Add(1)
	case StatusMatchID:
		c.matchedID.Add(1)
	case StatusMatchSha:
		c.matchedSha.Add(1)
	case StatusMismatch, StatusSkip:
		c.skipped.Add(1)
	case StatusReject:
		c.rejected.Add(1)
	}
	outlog.Write(res.Outcome)
	if cfg.DryRun {
		return nil
	}

	if res.Job != nil {
		if err := state.FinishMetadata(ctx, rec.Key, res.Outcome.Detail, *res.Job); err != nil {
			return fmt.Errorf("could not queue index job: %w", err)
		}
		select {
		case jobQueued <- struct{}{}:
		default:
		}
		return nil
	}
	return state.SetLine(ctx, l.hash, rec.Key, res.State, res.Outcome.ReleaseID, res.Outcome.Detail)
}

// runIndex feeds queued jobs to the index worker pool until the metadata side
// is finished and the queue holds nothing that could still run.
func runIndex(ctx context.Context, cfg Config, ix Indexer, state *StateDB, outlog *OutcomeLog, c *counters, metadataDone *atomic.Bool, jobQueued <-chan struct{}) error {
	jobs := make(chan IndexJob)
	var inflight atomic.Int64
	var wg sync.WaitGroup
	var fatal atomic.Value
	for range cfg.IndexConcurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				if err := handleJob(ctx, cfg, ix, state, outlog, c, j); err != nil {
					fatal.CompareAndSwap(nil, err)
				}
				inflight.Add(-1)
			}
		}()
	}
	defer func() { close(jobs); wg.Wait() }()

	poll := time.NewTicker(2 * time.Second)
	defer poll.Stop()
	for {
		if err, _ := fatal.Load().(error); err != nil {
			return err
		}
		if ctx.Err() != nil {
			return nil
		}
		// Claim no more than there are idle workers, so a claimed job is never
		// waiting on a channel while a crash would leave it stranded until the
		// next run releases it.
		free := cfg.IndexConcurrency - int(inflight.Load())
		var batch []IndexJob
		if free > 0 {
			var err error
			batch, err = state.ClaimJobs(ctx, free)
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return fmt.Errorf("claiming index jobs: %w", err)
			}
		}
		for _, j := range batch {
			inflight.Add(1)
			select {
			case jobs <- j:
			case <-ctx.Done():
				return nil
			}
		}
		if len(batch) > 0 {
			continue
		}

		if metadataDone.Load() && inflight.Load() == 0 {
			q, err := state.QueueCounts(ctx)
			if err != nil {
				return err
			}
			if q[jobPending] == 0 && q[jobClaimed] == 0 {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-jobQueued:
		case <-poll.C:
		}
	}
}

func handleJob(ctx context.Context, cfg Config, ix Indexer, state *StateDB, outlog *OutcomeLog, c *counters, j IndexJob) error {
	note, err := ix.Index(ctx, j)
	if ctx.Err() != nil {
		return nil // interrupted; the claim is released on the next open
	}
	oc := Outcome{Stage: "index", LineHash: j.LineHash, ReleaseID: j.ReleaseID, FileIDs: j.FileIDs, Score: -1}
	if err == nil {
		c.indexed.Add(1)
		oc.Status, oc.Detail = StatusIndexed, note
		outlog.Write(oc)
		return state.FinishJob(ctx, j.LineHash)
	}

	attempts := j.Attempts + 1
	parked, serr := state.FailJob(ctx, j.LineHash, attempts, cfg.IndexAttempts, time.Duration(attempts)*time.Minute, err)
	if serr != nil {
		return serr
	}
	c.indexFailed.Add(1)
	oc.Status = StatusIdxFail
	oc.Detail = fmt.Sprintf("attempt %d: %s", attempts, err)
	if parked {
		c.indexParked.Add(1)
		oc.Detail += " (parked; rerun with --index-only)"
	}
	outlog.Write(oc)
	return nil
}
