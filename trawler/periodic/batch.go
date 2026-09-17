package periodic

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"git.archive.org/webgroup/scholar/trawler/httpclient"
	"git.archive.org/webgroup/scholar/trawler/pdf"
	"github.com/spf13/viper"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/log"
	"net/http"
)

// Ingesting is the other half of a periodic run: one activity takes one
// BatchRef and works every PDF CDX row in it, keeping a bounded number in
// flight itself.
//
// The unit of work Temporal sees is therefore a batch, not a PDF. What Temporal
// still provides -- and the only thing this design actually needs from it -- is
// that a batch which never finishes comes back: a worker that loses power stops
// heartbeating, the server hands the batch to another worker, and the heartbeat
// checkpoint tells it where to pick up. The work list itself was already
// durable in S3 and the resume point was already just a byte offset, so nothing
// about the failure story changes except its grain.
//
// The grain is affordable because redoing a row is nearly free. Every step of a
// line's ingest checks whether its output is already there -- fatcat by sha1,
// both ES indices by document -- so a row redone between the last checkpoint and
// a crash costs a few lookups rather than another GROBID run.

const (
	// A line's ingest is retried in-process now rather than by Temporal, so the
	// backoff no longer has to cover a scheduling round trip. Three attempts at
	// these intervals spends at most about a minute of a goroutine waiting on a
	// line that is not going to work -- on top of whatever the attempts
	// themselves cost, which for a PDF is the expensive part.
	lineRetryInitial     = 15 * time.Second
	lineRetryMax         = 2 * time.Minute
	lineRetryCoefficient = 2.0

	// defaultSkipRulesReloadInterval is how stale the denylists may get inside a
	// running batch. See skipRulesReloader.
	defaultSkipRulesReloadInterval = 60 * time.Second
)

type IngestBatchInput struct {
	Batch          BatchRef
	CollectionName string
	// Source is the provenance label stamped on records this run creates.
	Source string
	// ChunkSize is how many bytes one range read of the staged CDX pulls.
	ChunkSize int
	// Parallelism is how many of this batch's rows are ingested at once.
	Parallelism int
	// LineAttempts is how many times one row's ingest is tried before the row is
	// abandoned. Abandoning a row is normal: a collection of millions will
	// always contain PDFs petabox will not serve or GROBID will not answer for,
	// and one of them must not strand the rest.
	LineAttempts int
	// MaxConsecutiveErrors trips the circuit breaker. A streak this long means
	// something systemic -- GROBID down, fatcat unreachable, ES refusing writes
	// -- rather than bad PDFs, and is not worth burning the rest of the
	// collection against.
	MaxConsecutiveErrors int
}

type IngestBatchOutput struct {
	// Counts is the whole batch's tally, including any carried across a resume.
	Counts PeriodicCounts
	// DoneThrough is how many of the batch's rows reached a verdict.
	DoneThrough int
	// NextOffset is the byte offset just past the last row that reached a
	// verdict, so an aborted run can say where to resume.
	NextOffset int64
	// Aborted reports that the circuit breaker tripped. The batch did not fail
	// -- everything it did finish is in Counts and on disk -- but the run
	// should stop.
	Aborted     bool
	AbortReason string
}

// batchCheckpoint is what a batch heartbeats and what a retried batch resumes
// from.
//
// It covers the contiguous prefix of the batch and nothing else. Rows finished
// out of order past the watermark are deliberately left out: a resume starts at
// DoneThrough, so counting them here would count them twice.
type batchCheckpoint struct {
	DoneThrough int
	NextOffset  int64
	Consecutive int
	Counts      PeriodicCounts
}

// IngestBatchActivity ingests one batch of PDF CDX rows.
//
// It returns an error only for something systemic: the staged CDX cannot be
// read, the outcome log cannot be written, or the batch was stopped before it
// finished. A row that will not ingest is counted and logged inside the batch
// and never surfaces as an activity failure, which is what lets a run walk a
// whole crawl collection without one bad PDF stopping it.
func IngestBatchActivity(ctx context.Context, in IngestBatchInput) (IngestBatchOutput, error) {
	l := activity.GetLogger(ctx)

	out := IngestBatchOutput{NextOffset: in.Batch.Offset}

	// Pick up where a previous attempt left off. This is read before anything
	// is recorded, since recording is what replaces it.
	var from batchCheckpoint
	if activity.HasHeartbeatDetails(ctx) {
		if err := activity.GetHeartbeatDetails(ctx, &from); err != nil {
			l.Warn("unreadable checkpoint, redoing the batch from the top", "err", err.Error())
			from = batchCheckpoint{}
		}
	}

	lines, err := readBatchLines(ctx, in.Batch, in.ChunkSize)
	if err != nil {
		return out, err
	}
	if len(lines) == 0 {
		// Not an error: a staged item can be empty, or end on blank lines. There
		// is simply nothing here to ingest.
		out.NextOffset = in.Batch.End
		return out, nil
	}

	// latest is what every heartbeat publishes. A PDF being processed heartbeats
	// far more often than a line finishes, and those beats have to carry the
	// same checkpoint as the ones the runner sends, or a beat from mid-GROBID
	// would hand a retry a stale resume point.
	var latest atomic.Pointer[batchCheckpoint]
	latest.Store(&from)
	beat := func(string) { activity.RecordHeartbeat(ctx, *latest.Load()) }
	beat("start")

	// One HTTP client and one PDF processor for the whole batch rather than one
	// per line. Both are safe for concurrent use, so the goroutines below share
	// them, and a batch of hundreds of rows no longer pays to rebuild them
	// hundreds of times. The client is on the shared connection pool, which is
	// what keeps parallel petabox and ES traffic from opening a socket per
	// request.
	client := &http.Client{
		Transport: httpclient.Transport(),
		Timeout:   10 * time.Minute,
	}

	processor, err := pdf.NewProcessor(beat)
	if err != nil {
		return out, fmt.Errorf("could not create pdf processor: %w", err)
	}

	reload := viper.GetDuration("periodic.skip_rules_reload_interval")
	if reload <= 0 {
		reload = defaultSkipRulesReloadInterval
	}

	logDir := outcomeLogDir()
	workflowID := activity.GetInfo(ctx).WorkflowExecution.ID

	// Read once per batch rather than per row: the generation is how the cache
	// is wiped, so it has to be able to change under a running worker, but not
	// so eagerly that one batch consults two generations.
	badPDFs := newBadPDFCache()
	if !badPDFs.enabled() {
		l.Info("bad-pdf cache is disabled; every unusable pdf will be re-processed")
	}

	deps := lineDeps{
		HTTP:      client,
		Processor: processor,
		Source:    in.Source,
		ItemID:    in.Batch.ItemID,
		SkipRules: func() (pdfSkipRules, error) { return skipRules.get(reload, time.Now()) },
		BadPDFs:   badPDFs,
		Beat:      beat,
		Log:       l,
	}

	runner := &batchRunner{
		lines:          lines,
		start:          in.Batch.Offset,
		parallelism:    in.Parallelism,
		attempts:       in.LineAttempts,
		maxConsecutive: in.MaxConsecutiveErrors,
		ingest: func(ctx context.Context, jsonLine string) (PeriodicCounts, Outcome, error) {
			return ingestLine(ctx, deps, jsonLine)
		},
		record: func(o Outcome) error { return appendOutcome(logDir, workflowID, o) },
		checkpoint: func(cp batchCheckpoint) {
			latest.Store(&cp)
			activity.RecordHeartbeat(ctx, cp)
		},
		sleep:   sleepCtx,
		stopped: activity.GetWorkerStopChannel(ctx),
		now:     time.Now,
		log:     l,
	}

	out, err = runner.run(ctx, from)
	l.Info("batch done",
		"item", in.Batch.ItemID,
		"offset", in.Batch.Offset,
		"lines", len(lines),
		"done", out.DoneThrough,
		"counts", out.Counts,
		"aborted", out.Aborted)
	return out, err
}

// ingestFn works one CDX row to a verdict. IngestBatchActivity supplies the
// real pipeline; tests supply their own.
type ingestFn func(ctx context.Context, jsonLine string) (PeriodicCounts, Outcome, error)

// batchRunner ingests one batch's rows with a bounded pool of goroutines,
// checkpointing the contiguous prefix as it goes.
//
// It is separated from IngestBatchActivity so the ordering, checkpointing and
// circuit-breaker logic -- the parts a power cut actually exercises -- can be
// tested without petabox, GROBID, fatcat, ES, S3 or a Temporal activity context.
type batchRunner struct {
	// lines is the whole batch, in CDX order.
	lines []LineRef
	// start is the batch's byte offset: the checkpoint's NextOffset before any
	// row has finished.
	start int64

	parallelism    int
	attempts       int
	maxConsecutive int

	// ingest runs one row.
	ingest ingestFn
	// record appends one row to the outcome log.
	record func(Outcome) error
	// checkpoint publishes progress; in the activity this is a heartbeat.
	checkpoint func(batchCheckpoint)
	// sleep is the backoff between a row's attempts, injected so tests do not
	// have to wait out a real one.
	sleep func(context.Context, time.Duration) error
	// stopped closes when the worker is shutting down. Left nil it never fires,
	// which is what a test wants.
	stopped <-chan struct{}
	// now is the clock the outcome rows are stamped with.
	now func() time.Time
	log log.Logger
}

// lineResult is one row's verdict, held until the watermark reaches it.
type lineResult struct {
	counts  PeriodicCounts
	outcome Outcome
	errored bool
}

// run ingests the batch from the given checkpoint.
func (b *batchRunner) run(ctx context.Context, from batchCheckpoint) (IngestBatchOutput, error) {
	b.parallelism = max(b.parallelism, 1)
	b.attempts = max(b.attempts, 1)
	if b.maxConsecutive < 1 {
		b.maxConsecutive = defaultMaxConsecutiveErrors
	}

	// mark is the watermark: every row below it has a verdict, a row on disk
	// and a place in counts. It is the one number a retry needs.
	mark := min(max(from.DoneThrough, 0), len(b.lines))

	out := IngestBatchOutput{
		Counts:      from.Counts,
		DoneThrough: mark,
		NextOffset:  b.offsetAt(mark),
	}
	if mark == len(b.lines) {
		return out, nil
	}

	var (
		mu     sync.Mutex
		done   = make([]*lineResult, len(b.lines))
		counts = from.Counts
		consec = from.Consecutive
		abort  string
		logErr error
		halted atomic.Bool
	)

	// quit releases the feeder once run returns, so its lifetime is bounded by
	// this call rather than by the workers draining it.
	quit := make(chan struct{})
	defer close(quit)

	// The feeder hands out indices in CDX order and is the one place the batch
	// stops early, whether because the breaker tripped, the log broke, the
	// worker is going down or nobody is waiting any more.
	resumeAt := mark
	next := make(chan int)
	go func() {
		defer close(next)
		for i := resumeAt; i < len(b.lines); i++ {
			if halted.Load() {
				return
			}
			select {
			case next <- i:
			case <-quit:
				return
			case <-ctx.Done():
				return
			case <-b.stopped:
				return
			}
		}
	}()

	var wg sync.WaitGroup
	for range b.parallelism {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				// The feeder runs one index ahead -- its send is already pending
				// while this goroutine is still working on the last one -- so
				// checking here as well is what stops the batch on the row that
				// tripped the breaker rather than the one after it. The index is
				// simply dropped: done[i] stays unset, the watermark stops below
				// it, and a retry ingests it.
				if halted.Load() {
					return
				}

				res := b.ingestOne(ctx, i)
				if res == nil {
					// No verdict, so no row and no tally. Leaving done[i] unset
					// stops the watermark here and the retry re-ingests it.
					return
				}

				mu.Lock()
				done[i] = res
				// Fold in everything the watermark can now reach. Doing it here
				// rather than as each line lands is what makes the rows come out
				// in CDX order and the error streak mean what it says, even
				// though the ingests themselves finish out of order.
				for mark < len(done) && done[mark] != nil {
					r := done[mark]
					if err := b.record(r.outcome); err != nil {
						logErr = err
						break
					}
					counts = counts.Add(r.counts)
					if r.errored {
						consec++
					} else {
						consec = 0
					}
					mark++
					if consec >= b.maxConsecutive && abort == "" {
						abort = fmt.Sprintf("%d consecutive ingest failures", consec)
					}
				}
				cp := batchCheckpoint{
					DoneThrough: mark,
					NextOffset:  b.offsetAt(mark),
					Consecutive: consec,
					Counts:      counts,
				}
				stop := abort != "" || logErr != nil
				mu.Unlock()

				if stop {
					halted.Store(true)
				}
				b.checkpoint(cp)
			}
		}()
	}
	wg.Wait()

	out.Counts = counts
	out.DoneThrough = mark
	out.NextOffset = b.offsetAt(mark)

	// An unwritable log is a broken deployment, not a per-row hiccup: it stops
	// the batch rather than quietly dropping the record of what happened. The
	// retry resumes from the checkpoint, which only ever advanced over rows that
	// were written, so no row is lost and none is duplicated.
	if logErr != nil {
		return out, logErr
	}

	// A batch small enough to finish without ever reaching MaxConsecutiveErrors
	// can still be telling us the same thing the streak would have.
	if abort == "" && mark == len(b.lines) && out.Counts.Lines > 0 &&
		out.Counts.PdfsErrored == out.Counts.Lines {
		abort = fmt.Sprintf("all %d rows in the batch exhausted their retries", out.Counts.Lines)
	}

	if abort != "" {
		out.Aborted = true
		out.AbortReason = abort
		return out, nil
	}

	if mark < len(b.lines) {
		// Stopped before the batch had a verdict on every row. This must be an
		// error: returning successfully would tell the workflow the whole range
		// was ingested and move the run past PDFs nobody looked at.
		cause := ctx.Err()
		if cause == nil {
			cause = errors.New("worker is shutting down")
		}
		return out, fmt.Errorf("periodic: batch at offset %d stopped after %d of %d rows: %w",
			b.start, mark, len(b.lines), cause)
	}

	return out, nil
}

// ingestOne works one row to a verdict, retrying in place. It returns nil when
// the context died before a verdict was reached, which is not a verdict on the
// row and must not be recorded as one.
func (b *batchRunner) ingestOne(ctx context.Context, i int) *lineResult {
	line := b.lines[i].JSON

	var (
		lastErr error
		lastOC  Outcome
	)
	for attempt := 1; attempt <= b.attempts; attempt++ {
		if attempt > 1 {
			if err := b.sleep(ctx, ingestBackoff(attempt-1)); err != nil {
				lastErr = err
				break
			}
		}

		counts, oc, err := b.ingest(ctx, line)
		if err == nil {
			oc.At = b.now()
			return &lineResult{counts: counts, outcome: oc}
		}

		lastErr = err
		// Keep whatever the failed attempt did manage to identify, so the error
		// row names the PDF rather than just the failure.
		lastOC = oc
		if ctx.Err() != nil {
			break
		}
		if b.log != nil {
			b.log.Warn("line ingest failed",
				"sha1_b32", oc.Sha1B32, "url", oc.URL,
				"attempt", attempt, "of", b.attempts, "err", err.Error())
		}
	}

	if ctx.Err() != nil {
		return nil
	}

	lastOC.Status = StatusError
	lastOC.Detail = fmt.Sprintf("ingest failed after %d attempts: %v", b.attempts, lastErr)
	lastOC.At = b.now()
	return &lineResult{
		errored: true,
		counts:  PeriodicCounts{Lines: 1, PdfsErrored: 1},
		outcome: lastOC,
	}
}

// offsetAt is the byte position a resume at index i picks up from.
func (b *batchRunner) offsetAt(i int) int64 {
	if i <= 0 {
		return b.start
	}
	return b.lines[i-1].End
}

// ingestBackoff is how long to wait after n failed attempts at a row.
func ingestBackoff(n int) time.Duration {
	d := lineRetryInitial
	for range max(n-1, 0) {
		d = time.Duration(float64(d) * lineRetryCoefficient)
		if d >= lineRetryMax {
			return lineRetryMax
		}
	}
	return d
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
