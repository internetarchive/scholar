package daily

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	cdx "git.archive.org/webgroup/scholar/trawler/cdx/cdxclient"
	"git.archive.org/webgroup/scholar/trawler/crawling"
	"git.archive.org/webgroup/scholar/trawler/httpclient"
	"git.archive.org/webgroup/scholar/trawler/pdf"
	"git.archive.org/webgroup/scholar/trawler/spn/spnclient"
	"github.com/spf13/viper"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/log"
)

// Working a batch is the other half of a day: one activity takes one BatchRef
// and runs every ndjson line in it to a verdict, keeping a bounded number in
// flight itself.
//
// The unit of work Temporal sees is therefore a batch, not a line. What
// Temporal still provides -- and the only thing this design actually needs
// from it -- is that a batch which never finishes comes back: a worker that
// loses power stops heartbeating, the server hands the batch to another
// worker, and the heartbeat checkpoint tells it where to pick up. The work
// list itself was already durable in S3 and the resume point was already just
// a byte offset, so nothing about the failure story changes except its grain.
//
// The grain is affordable because redoing a line is nearly free. Every step
// checks whether its output is already there -- fatcat by DOI and by sha256,
// both ES indices by document -- and a re-crawl inside the CDX freshness
// window is a lookup rather than another SPN capture. A line redone between
// the last checkpoint and a crash costs a few lookups rather than another
// capture and another GROBID run.

const (
	// A line is retried in-process now rather than by Temporal, so the backoff
	// no longer has to cover a scheduling round trip. Three attempts at these
	// intervals spends at most about a minute of a goroutine waiting on a line
	// that is not going to work -- on top of whatever the attempts themselves
	// cost, which for a crawl is the expensive part.
	lineRetryInitial     = 15 * time.Second
	lineRetryMax         = 2 * time.Minute
	lineRetryCoefficient = 2.0
)

type CrawlBatchInput struct {
	S3Key string
	Batch BatchRef
	// Source is the provenance label stamped on records this run creates.
	Source string
	// Upstream is which API the lines came from.
	Upstream string
	// ChunkSize is how many bytes one range read of the .ndjson pulls.
	ChunkSize int
	// Parallelism is how many of this batch's lines are worked at once.
	Parallelism int
	// LineAttempts is how many times one line is tried before it is abandoned.
	// Abandoning a line is normal: a day of upstream metadata will always
	// contain records fatcat chokes on, and one of them must not strand the
	// rest.
	LineAttempts int
	// MaxConsecutiveErrors trips the circuit breaker. A streak this long means
	// something systemic -- fatcat unreachable, GROBID down, ES refusing writes
	// -- rather than bad records, and is not worth burning the rest of the day
	// against.
	MaxConsecutiveErrors int
}

type CrawlBatchOutput struct {
	// Counts is the whole batch's tally, including any carried across a resume.
	Counts DailyCounts
	// DoneThrough is how many of the batch's lines reached a verdict.
	DoneThrough int
	// NextOffset is the byte offset just past the last line that reached a
	// verdict, so an aborted run can say where to resume.
	NextOffset int64
	// Aborted reports that the circuit breaker tripped. The batch did not fail
	// -- everything it did finish is in Counts and on disk -- but the run
	// should stop.
	Aborted     bool
	AbortReason string
}

// batchHeartbeat is what a batch actually publishes: the checkpoint a retry
// resumes from, plus a note on what the batch was doing when it published.
//
// The note exists for crashes Go cannot report on. PDF processing runs through
// cgo into MuPDF, and a malformed PDF that segfaults there takes the whole
// worker process down -- no error, no deferred log line, no outcome row. What
// survives is the last heartbeat the server accepted, so that heartbeat has to
// say what was in hand.
//
// Phase is deliberately outside batchCheckpoint: a resume decodes the payload
// into a batchCheckpoint, which ignores the extra field, so this stays a
// diagnostic and never becomes resume state.
type batchHeartbeat struct {
	batchCheckpoint
	Phase string
}

// batchCheckpoint is what a batch heartbeats and what a retried batch resumes
// from.
//
// It covers the contiguous prefix of the batch and nothing else. Lines
// finished out of order past the watermark are deliberately left out: a resume
// starts at DoneThrough, so counting them here would count them twice.
type batchCheckpoint struct {
	DoneThrough int
	NextOffset  int64
	Consecutive int
	Counts      DailyCounts
}

// CrawlBatchActivity works one batch of ndjson lines.
//
// It returns an error only for something systemic: the .ndjson cannot be read,
// the outcome log cannot be written, the upstream is unknown, or the batch was
// stopped before it finished. A record that will not process is counted and
// logged inside the batch and never surfaces as an activity failure, which is
// what lets a run walk a whole day without one bad record stopping it.
func CrawlBatchActivity(ctx context.Context, in CrawlBatchInput) (CrawlBatchOutput, error) {
	l := activity.GetLogger(ctx)

	out := CrawlBatchOutput{NextOffset: in.Batch.Offset}

	process, err := upstreamProcessor(in.Upstream)
	if err != nil {
		return out, err
	}

	// Pick up where a previous attempt left off. This is read before anything
	// is recorded, since recording is what replaces it.
	var from batchCheckpoint
	if activity.HasHeartbeatDetails(ctx) {
		if err := activity.GetHeartbeatDetails(ctx, &from); err != nil {
			l.Warn("unreadable checkpoint, redoing the batch from the top", "err", err.Error())
			from = batchCheckpoint{}
		}
	}

	lines, err := readBatchLines(ctx, in.S3Key, in.Batch, in.ChunkSize)
	if err != nil {
		return out, err
	}
	if len(lines) == 0 {
		// Not an error: an ndjson can end on blank lines. There is simply
		// nothing here to work, and the next batch starts where this one would
		// have.
		out.NextOffset = in.Batch.End
		return out, nil
	}

	// latest is what every heartbeat publishes. A crawl heartbeats far more
	// often than a line finishes -- once per hop, once per SPN poll, once per
	// GROBID beat -- and those beats have to carry the same checkpoint as the
	// ones the runner sends, or a beat from mid-crawl would hand a retry a
	// stale resume point.
	var latest atomic.Pointer[batchCheckpoint]
	latest.Store(&from)
	beat := func(phase string) {
		activity.RecordHeartbeat(ctx, batchHeartbeat{
			batchCheckpoint: *latest.Load(),
			Phase:           phase,
		})
	}
	beat(fmt.Sprintf("starting at offset %d", in.Batch.Offset))

	// One HTTP client, one PDF processor and one pair of crawl clients for the
	// whole batch rather than one per line. All of them are safe for concurrent
	// use, so the goroutines below share them, and a batch of hundreds of lines
	// no longer pays to rebuild them hundreds of times. The client is on the
	// shared connection pool, which is what keeps parallel fatcat, SPN and ES
	// traffic from opening a socket per request.
	client := &http.Client{
		Transport: httpclient.Transport(),
		Timeout:   10 * time.Minute,
	}

	processor, err := pdf.NewProcessor(beat)
	if err != nil {
		return out, fmt.Errorf("pdf processor init failed: %w", err)
	}

	spnClient, err := spnclient.NewDefaultClient(spnclient.SPNConfig{
		AccessKey: viper.GetString("spn.access_key"),
		SecretKey: viper.GetString("spn.secret_key"),
		Endpoint:  viper.GetString("spn.endpoint"),
		Debug:     true,
	})
	if err != nil {
		return out, fmt.Errorf("spn client creation failed: %w", err)
	}

	cdxClient := cdx.NewClient(cdx.Config{
		Auth:      viper.GetString("cdx.auth"),
		Endpoint:  viper.GetString("cdx.endpoint"),
		UserAgent: viper.GetString("cdx.user_agent"),
		Retries:   viper.GetInt("cdx.retries"),
		Backoff:   viper.GetDuration("cdx.backoff"),
		// A retrying CDX query is silent for long enough to outlast this
		// activity's heartbeat timeout; without this the server times the
		// activity out and cancels its context mid-batch.
		OnRetry: func(int) { beat("cdx-retry") },
		Debug:   true,
	})

	maxHops := viper.GetInt("daily.max_hops")
	if maxHops <= 0 {
		maxHops = defaultMaxHops
	}

	base := crawling.PDFCrawler{
		SPNClient:       spnClient,
		CDXClient:       cdxClient,
		MaxHops:         maxHops,
		UserAgent:       viper.GetString("crawling.user_agent"),
		WaybackEndpoint: viper.GetString("wayback.replay_endpoint"),
		SimpleGets:      viper.GetStringSlice("crawling.simple_get_list"),
		Logger:          slog.Default(),
		Heartbeater:     beat,
		// Waiting for an SPN session happens in place. A crawl parked in the
		// poll loop holds one of this batch's goroutines rather than a Temporal
		// activity slot, and the lines behind it in the batch keep moving.
		DeferWhenSPNBusy: false,
	}

	reload := rulesReloadInterval()
	rules := func() (crawlRules, error) { return blocklists.get(reload, time.Now()) }

	logDir := outcomeLogDir()
	workflowID := activity.GetInfo(ctx).WorkflowExecution.ID

	deps := lineDeps{
		HTTP:      client,
		Processor: processor,
		Process:   process,
		Source:    in.Source,
		Upstream:  in.Upstream,
		Rules:     rules,
		Crawl: func(ctx context.Context, u string) (crawling.CrawlResult, error) {
			// Copy the batch's crawler rather than share it: the blocklist is
			// read fresh so an edit lands on a running batch -- which at this
			// batch size could otherwise be hours away -- and the goroutines in
			// the pool must not write the same field.
			c := base
			r, err := rules()
			if err != nil {
				l.Warn("using crawl rules as last loaded", "err", err.Error())
			}
			c.Blocklist = r.URLBlocklist
			return c.Crawl(ctx, u)
		},
		Beat: beat,
		Log:  l,
	}

	runner := &batchRunner{
		lines:          lines,
		start:          in.Batch.Offset,
		parallelism:    in.Parallelism,
		attempts:       in.LineAttempts,
		maxConsecutive: in.MaxConsecutiveErrors,
		process: func(ctx context.Context, jsonLine string) (DailyCounts, Outcome, error) {
			return processLine(ctx, deps, jsonLine)
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
		"offset", in.Batch.Offset,
		"lines", len(lines),
		"done", out.DoneThrough,
		"counts", out.Counts,
		"aborted", out.Aborted)
	return out, err
}

// processFn works one ndjson line to a verdict. CrawlBatchActivity supplies
// the real pipeline; tests supply their own.
type processFn func(ctx context.Context, jsonLine string) (DailyCounts, Outcome, error)

// batchRunner works one batch's lines with a bounded pool of goroutines,
// checkpointing the contiguous prefix as it goes.
//
// It is separated from CrawlBatchActivity so the ordering, checkpointing and
// circuit-breaker logic -- the parts a power cut actually exercises -- can be
// tested without SPN, CDX, GROBID, fatcat, ES, S3 or a Temporal activity
// context.
type batchRunner struct {
	// lines is the whole batch, in file order.
	lines []LineRef
	// start is the batch's byte offset: the checkpoint's NextOffset before any
	// line has finished.
	start int64

	parallelism    int
	attempts       int
	maxConsecutive int

	// process runs one line.
	process processFn
	// record appends one row to the outcome log.
	record func(Outcome) error
	// checkpoint publishes progress; in the activity this is a heartbeat.
	checkpoint func(batchCheckpoint)
	// sleep is the backoff between a line's attempts, injected so tests do not
	// have to wait out a real one.
	sleep func(context.Context, time.Duration) error
	// stopped closes when the worker is shutting down. Left nil it never fires,
	// which is what a test wants.
	stopped <-chan struct{}
	// now is the clock the outcome rows are stamped with.
	now func() time.Time
	log log.Logger
}

// lineResult is one line's verdict, held until the watermark reaches it.
type lineResult struct {
	counts  DailyCounts
	outcome Outcome
	errored bool
}

// run works the batch from the given checkpoint.
func (b *batchRunner) run(ctx context.Context, from batchCheckpoint) (CrawlBatchOutput, error) {
	b.parallelism = max(b.parallelism, 1)
	b.attempts = max(b.attempts, 1)
	if b.maxConsecutive < 1 {
		b.maxConsecutive = defaultMaxConsecutiveErrors
	}

	// mark is the watermark: every line below it has a verdict, a row on disk
	// and a place in counts. It is the one number a retry needs.
	mark := min(max(from.DoneThrough, 0), len(b.lines))

	out := CrawlBatchOutput{
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

	// The feeder hands out indices in file order and is the one place the batch
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
				// checking here as well is what stops the batch on the line that
				// tripped the breaker rather than the one after it. The index is
				// simply dropped: done[i] stays unset, the watermark stops below
				// it, and a retry works it.
				if halted.Load() {
					return
				}

				res := b.processOne(ctx, i)
				if res == nil {
					// No verdict, so no row and no tally. Leaving done[i] unset
					// stops the watermark here and the retry re-works it.
					return
				}

				mu.Lock()
				done[i] = res
				// Fold in everything the watermark can now reach. Doing it here
				// rather than as each line lands is what makes the rows come out
				// in file order and the error streak mean what it says, even
				// though the lines themselves finish out of order.
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
						abort = fmt.Sprintf("%d consecutive line failures", consec)
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

	// An unwritable log is a broken deployment, not a per-line hiccup: it stops
	// the batch rather than quietly dropping the record of what happened. The
	// retry resumes from the checkpoint, which only ever advanced over rows that
	// were written, so no row is lost and none is duplicated.
	if logErr != nil {
		return out, logErr
	}

	// A batch small enough to finish without ever reaching MaxConsecutiveErrors
	// can still be telling us the same thing the streak would have.
	if abort == "" && mark == len(b.lines) && out.Counts.Lines > 0 &&
		out.Counts.Errored == out.Counts.Lines {
		abort = fmt.Sprintf("all %d lines in the batch exhausted their retries", out.Counts.Lines)
	}

	if abort != "" {
		out.Aborted = true
		out.AbortReason = abort
		return out, nil
	}

	if mark < len(b.lines) {
		// Stopped before the batch had a verdict on every line. This must be an
		// error: returning successfully would tell the workflow the whole range
		// was worked and move the run past records nobody looked at.
		cause := ctx.Err()
		if cause == nil {
			cause = errors.New("worker is shutting down")
		}
		return out, fmt.Errorf("daily: batch at offset %d stopped after %d of %d lines: %w",
			b.start, mark, len(b.lines), cause)
	}

	return out, nil
}

// processOne works one line to a verdict, retrying in place. It returns nil
// when the context died before a verdict was reached, which is not a verdict
// on the line and must not be recorded as one.
func (b *batchRunner) processOne(ctx context.Context, i int) *lineResult {
	line := b.lines[i].JSON

	var (
		lastErr error
		lastOC  Outcome
	)
	for attempt := 1; attempt <= b.attempts; attempt++ {
		if attempt > 1 {
			if err := b.sleep(ctx, lineBackoff(attempt-1)); err != nil {
				lastErr = err
				break
			}
		}

		counts, oc, err := b.process(ctx, line)
		if err == nil {
			oc.At = b.now()
			return &lineResult{counts: counts, outcome: oc}
		}

		lastErr = err
		// Keep whatever the failed attempt did manage to identify, so the error
		// row names the record rather than just the failure.
		lastOC = oc
		if ctx.Err() != nil {
			break
		}
		if b.log != nil {
			b.log.Warn("line processing failed",
				"ext_id", oc.ExtID, "release_id", oc.ReleaseID,
				"attempt", attempt, "of", b.attempts, "err", err.Error())
		}
	}

	if ctx.Err() != nil {
		return nil
	}

	lastOC.Status = StatusError
	lastOC.Detail = fmt.Sprintf("processing failed after %d attempts: %v", b.attempts, lastErr)
	lastOC.At = b.now()
	return &lineResult{
		errored: true,
		counts:  DailyCounts{Lines: 1, Errored: 1},
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

// lineBackoff is how long to wait after n failed attempts at a line.
func lineBackoff(n int) time.Duration {
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
