package scrape

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	cdx "git.archive.org/webgroup/scholar/trawler/cdx/cdxclient"
	"git.archive.org/webgroup/scholar/trawler/crawling"
	"git.archive.org/webgroup/scholar/trawler/spn/spnclient"
	"github.com/spf13/viper"
	"go.temporal.io/sdk/activity"
)

// Crawling is the other half of a scrape: one activity takes one BatchRef and
// crawls every URL in it, keeping a bounded number in flight itself.
//
// The unit of work Temporal sees is therefore a batch, not a URL. What Temporal
// still provides -- and the only thing this design actually needs from it -- is
// that a batch which never finishes comes back: a worker that loses power stops
// heartbeating, the server hands the batch to another worker, and the heartbeat
// checkpoint tells it where to pick up. The work list itself was already
// durable in S3 and the resume point was already just a byte offset, so nothing
// about the failure story changes except its grain.
//
// The grain is affordable because re-crawling is nearly free. A URL crawled
// again inside the CDX freshness window is a CDX lookup, not another SPN
// capture, so redoing the handful of URLs between the last persisted checkpoint
// and a crash costs a few lookups rather than a few captures.

const (
	// A URL's crawl is retried in-process now rather than by Temporal, so the
	// backoff no longer has to cover a scheduling round trip and can be
	// tighter than the old activity RetryPolicy. Four attempts at these
	// intervals spends at most about two minutes of a goroutine on a URL that
	// is not going to work.
	urlRetryInitial     = 15 * time.Second
	urlRetryMax         = 2 * time.Minute
	urlRetryCoefficient = 2.0

	// defaultBlocklistReloadInterval is how stale the URL blocklist may get
	// inside a running batch. See blocklistReloader.
	defaultBlocklistReloadInterval = 60 * time.Second
)

// skipReasons are the FailReason values PDFCrawler produces before it ever
// touches the network. Everything else means SPN was asked to fetch something.
var skipReasons = map[string]bool{
	"blocklist": true,
	"bad-url":   true,
}

type CrawlBatchInput struct {
	S3Key string
	Batch BatchRef
	// ChunkSize is how many bytes one range read of the list pulls.
	ChunkSize int
	// Parallelism is how many of this batch's URLs are crawled at once.
	Parallelism int
	// URLAttempts is how many times one URL's crawl is tried before the URL is
	// abandoned. Abandoning a URL is normal: a list of millions will always
	// contain dead hosts, and one of them must not strand the rest.
	URLAttempts int
	// MaxConsecutiveErrors trips the circuit breaker. A streak this long means
	// something systemic -- bad SPN credentials, SPN down -- rather than bad
	// URLs, and is not worth burning the rest of the list against.
	MaxConsecutiveErrors int
}

type CrawlBatchOutput struct {
	// Counts is the whole batch's tally, including any carried across a resume.
	Counts ScrapeCounts
	// DoneThrough is how many of the batch's URLs reached a verdict.
	DoneThrough int
	// NextOffset is the byte offset just past the last URL that reached a
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
// It covers the contiguous prefix of the batch and nothing else. URLs finished
// out of order past the watermark are deliberately left out: a resume starts at
// DoneThrough, so counting them here would count them twice.
type batchCheckpoint struct {
	DoneThrough int
	NextOffset  int64
	Consecutive int
	Counts      ScrapeCounts
}

// CrawlBatchActivity crawls one batch of URLs.
//
// It returns an error only for something systemic: the list cannot be read, the
// outcome log cannot be written, or the batch was stopped before it finished.
// A URL that will not crawl is counted and logged inside the batch and never
// surfaces as an activity failure, which is what lets a run walk millions of
// URLs without a dead host stopping it.
func CrawlBatchActivity(ctx context.Context, in CrawlBatchInput) (CrawlBatchOutput, error) {
	l := activity.GetLogger(ctx)

	out := CrawlBatchOutput{NextOffset: in.Batch.Offset}

	// Pick up where a previous attempt left off. This is read before anything
	// is recorded, since recording is what replaces it.
	var from batchCheckpoint
	if activity.HasHeartbeatDetails(ctx) {
		if err := activity.GetHeartbeatDetails(ctx, &from); err != nil {
			l.Warn("unreadable checkpoint, redoing the batch from the top", "err", err.Error())
			from = batchCheckpoint{}
		}
	}

	urls, err := readBatchURLs(ctx, in.S3Key, in.Batch, in.ChunkSize)
	if err != nil {
		return out, err
	}
	if len(urls) == 0 {
		// Not an error: a list can end on blank lines. There is simply nothing
		// here to crawl, and the next batch starts where this one would have.
		out.NextOffset = in.Batch.End
		return out, nil
	}

	// latest is what every heartbeat publishes. The crawler heartbeats far more
	// often than a URL finishes -- once per hop, once per SPN poll -- and those
	// beats have to carry the same checkpoint as the ones the runner sends, or
	// a beat from mid-crawl would hand a retry a stale resume point.
	var latest atomic.Pointer[batchCheckpoint]
	latest.Store(&from)
	beat := func() { activity.RecordHeartbeat(ctx, *latest.Load()) }
	beat()

	spnClient, err := spnclient.NewDefaultClient(spnclient.SPNConfig{
		AccessKey: viper.GetString("spn.access_key"),
		SecretKey: viper.GetString("spn.secret_key"),
		Endpoint:  viper.GetString("spn.endpoint"),
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
		OnRetry: func(int) { beat() },
	})

	// One pair of clients for the whole batch rather than one per URL. Both are
	// stateless wrappers over the shared connection pool, so the goroutines
	// below share them safely, and a batch of hundreds of URLs no longer pays
	// to rebuild them hundreds of times.
	base := crawling.PDFCrawler{
		SPNClient:       spnClient,
		CDXClient:       cdxClient,
		MaxHops:         viper.GetInt("scrape.max_hops"),
		UserAgent:       viper.GetString("crawling.user_agent"),
		WaybackEndpoint: viper.GetString("wayback.replay_endpoint"),
		SimpleGets:      viper.GetStringSlice("crawling.simple_get_list"),
		Logger:          slog.Default(),
		Heartbeater:     func(string) { beat() },
		FreshnessCutoff: 21915 * time.Hour,
		// Waiting for an SPN session happens in place now. A crawl parked in
		// the poll loop holds one of this batch's goroutines rather than a
		// Temporal activity slot, and the URLs behind it in the batch keep
		// moving -- which is what handing the URL back to Temporal used to buy,
		// at the cost of a full retry cycle per deferred URL. Sizing
		// Parallelism above the SPN session budget is what keeps that true:
		// PDFCrawler only asks SPN for a session once a CDX lookup has come up
		// empty, so URLs already in Wayback never queue behind a busy SPN.
		DeferWhenSPNBusy: false,
	}
	if base.MaxHops <= 0 {
		base.MaxHops = 8
	}

	reload := viper.GetDuration("scrape.blocklist_reload_interval")
	if reload <= 0 {
		reload = defaultBlocklistReloadInterval
	}

	logDir := viper.GetString("scrape.log_dir")
	workflowID := activity.GetInfo(ctx).WorkflowExecution.ID

	runner := &batchRunner{
		urls:           urls,
		start:          in.Batch.Offset,
		parallelism:    in.Parallelism,
		attempts:       in.URLAttempts,
		maxConsecutive: in.MaxConsecutiveErrors,
		crawl: func(ctx context.Context, u string) (crawling.CrawlResult, error) {
			c := base
			// Read fresh so a blocklist edit lands on a running batch rather
			// than waiting for the next one, which at this batch size could be
			// hours away.
			patterns, err := blocklist.get(reload, time.Now())
			if err != nil {
				l.Warn("using url blocklist as last loaded", "err", err.Error())
			}
			c.Blocklist = patterns
			return c.Crawl(ctx, u)
		},
		record: func(o Outcome) error { return appendOutcome(logDir, workflowID, o) },
		checkpoint: func(cp batchCheckpoint) {
			latest.Store(&cp)
			activity.RecordHeartbeat(ctx, cp)
		},
		sleep:   sleepCtx,
		stopped: activity.GetWorkerStopChannel(ctx),
		now:     time.Now,
	}

	out, err = runner.run(ctx, from)
	l.Info("batch done",
		"offset", in.Batch.Offset,
		"urls", len(urls),
		"done", out.DoneThrough,
		"counts", out.Counts,
		"aborted", out.Aborted)
	return out, err
}

// crawlFn crawls one URL. CrawlBatchActivity supplies the real crawler; tests
// supply their own.
type crawlFn func(ctx context.Context, url string) (crawling.CrawlResult, error)

// batchRunner crawls one batch's URLs with a bounded pool of goroutines,
// checkpointing the contiguous prefix as it goes.
//
// It is separated from CrawlBatchActivity so the ordering, checkpointing and
// circuit-breaker logic -- the parts a power cut actually exercises -- can be
// tested without SPN, CDX, S3 or a Temporal activity context.
type batchRunner struct {
	// urls is the whole batch, in list order.
	urls []URLRef
	// start is the batch's byte offset: the checkpoint's NextOffset before any
	// URL has finished.
	start int64

	parallelism    int
	attempts       int
	maxConsecutive int

	// crawl runs one URL. Any CrawlResult.Content it returns belongs to the
	// runner, which drains and closes it.
	crawl crawlFn
	// record appends one row to the outcome log.
	record func(Outcome) error
	// checkpoint publishes progress; in the activity this is a heartbeat.
	checkpoint func(batchCheckpoint)
	// sleep is the backoff between a URL's attempts, injected so tests do not
	// have to wait out a real one.
	sleep func(context.Context, time.Duration) error
	// stopped closes when the worker is shutting down. Left nil it never fires,
	// which is what a test wants.
	stopped <-chan struct{}
	// now is the clock the outcome rows are stamped with.
	now func() time.Time
}

// urlResult is one URL's verdict, held until the watermark reaches it.
type urlResult struct {
	counts  ScrapeCounts
	outcome Outcome
	errored bool
}

// run crawls the batch from the given checkpoint.
func (b *batchRunner) run(ctx context.Context, from batchCheckpoint) (CrawlBatchOutput, error) {
	b.parallelism = max(b.parallelism, 1)
	b.attempts = max(b.attempts, 1)
	if b.maxConsecutive < 1 {
		b.maxConsecutive = defaultMaxConsecutiveErrors
	}

	// mark is the watermark: every URL below it has a verdict, a row on disk
	// and a place in counts. It is the one number a retry needs.
	mark := min(max(from.DoneThrough, 0), len(b.urls))

	out := CrawlBatchOutput{
		Counts:      from.Counts,
		DoneThrough: mark,
		NextOffset:  b.offsetAt(mark),
	}
	if mark == len(b.urls) {
		return out, nil
	}

	var (
		mu     sync.Mutex
		done   = make([]*urlResult, len(b.urls))
		counts = from.Counts
		consec = from.Consecutive
		abort  string
		logErr error
		halted atomic.Bool
	)

	// quit releases the feeder once run returns, so its lifetime is bounded by
	// this call rather than by the workers draining it. They do drain it today
	// -- every path out of a worker consumes an index first, so the feeder's
	// pending send always completes and it sees halted on the next turn -- but
	// that is an accounting argument about code in two places, and the cost of
	// not relying on it is one channel.
	quit := make(chan struct{})
	defer close(quit)

	// The feeder hands out indices in list order and is the one place the batch
	// stops early, whether because the breaker tripped, the log broke, the
	// worker is going down or nobody is waiting any more.
	resumeAt := mark
	next := make(chan int)
	go func() {
		defer close(next)
		for i := resumeAt; i < len(b.urls); i++ {
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
				// The feeder runs one index ahead -- its send is already
				// pending while this goroutine is still working on the last
				// one -- so checking here as well is what stops the batch on
				// the URL that tripped the breaker rather than the one after
				// it. The index is simply dropped: done[i] stays unset, the
				// watermark stops below it, and a retry crawls it.
				if halted.Load() {
					return
				}

				res := b.crawlOne(ctx, i)
				if res == nil {
					// No verdict, so no row and no tally. Leaving done[i] unset
					// stops the watermark here and the retry re-crawls it.
					return
				}

				mu.Lock()
				done[i] = res
				// Fold in everything the watermark can now reach. Doing it here
				// rather than as each crawl lands is what makes the rows come
				// out in list order and the error streak mean what it says,
				// even though the crawls themselves finish out of order.
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
						abort = fmt.Sprintf("%d consecutive crawl failures", consec)
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

	// An unwritable log is a broken deployment, not a per-URL hiccup: it stops
	// the batch rather than quietly dropping the record of what happened. The
	// retry resumes from the checkpoint, which only ever advanced over rows
	// that were written, so no row is lost and none is duplicated.
	if logErr != nil {
		return out, logErr
	}

	// A batch small enough to finish without ever reaching MaxConsecutiveErrors
	// can still be telling us the same thing the streak would have.
	if abort == "" && mark == len(b.urls) && out.Counts.URLs > 0 &&
		out.Counts.Errored == out.Counts.URLs {
		abort = fmt.Sprintf("all %d urls in the batch exhausted their retries", out.Counts.URLs)
	}

	if abort != "" {
		out.Aborted = true
		out.AbortReason = abort
		return out, nil
	}

	if mark < len(b.urls) {
		// Stopped before the batch had a verdict on every URL. This must be an
		// error: returning successfully would tell the workflow the whole range
		// was crawled and move the run past URLs nobody looked at.
		cause := ctx.Err()
		if cause == nil {
			cause = errors.New("worker is shutting down")
		}
		return out, fmt.Errorf("scrape: batch at offset %d stopped after %d of %d urls: %w",
			b.start, mark, len(b.urls), cause)
	}

	return out, nil
}

// crawlOne crawls one URL to a verdict, retrying in place. It returns nil when
// the context died before a verdict was reached, which is not a verdict on the
// URL and must not be recorded as one.
func (b *batchRunner) crawlOne(ctx context.Context, i int) *urlResult {
	u := b.urls[i].URL

	var lastErr error
	for attempt := 1; attempt <= b.attempts; attempt++ {
		if attempt > 1 {
			if err := b.sleep(ctx, crawlBackoff(attempt-1)); err != nil {
				lastErr = err
				break
			}
		}

		res, err := b.crawl(ctx, u)
		// Crawl hands back the Wayback replay body on success and never closes
		// it. We do not want the bytes, but at this volume leaking a connection
		// per URL would exhaust the worker, so drain and close it first.
		if res.Content != nil {
			io.Copy(io.Discard, res.Content)
			res.Content.Close()
		}
		if err == nil {
			return &urlResult{
				counts:  countsFor(res),
				outcome: outcomeFor(u, res, b.now()),
			}
		}

		lastErr = err
		if ctx.Err() != nil {
			break
		}
	}

	if ctx.Err() != nil {
		return nil
	}

	return &urlResult{
		errored: true,
		counts:  ScrapeCounts{URLs: 1, Errored: 1},
		outcome: Outcome{
			SeedURL: u,
			Status:  StatusError,
			Reason:  fmt.Sprintf("crawl of %q failed: %v", u, lastErr),
			At:      b.now(),
		},
	}
}

// offsetAt is the byte position a resume at index i picks up from.
func (b *batchRunner) offsetAt(i int) int64 {
	if i <= 0 {
		return b.start
	}
	return b.urls[i-1].End
}

// countsFor turns one crawl's verdict into a tally the workflow can fold in.
// Crawl outcomes that are simply "no PDF at the other end" are normal and
// expected, so they are counted rather than treated as failures.
func countsFor(res crawling.CrawlResult) ScrapeCounts {
	c := ScrapeCounts{URLs: 1}
	if res.Success {
		c.PDFFound = 1
		return c
	}
	reason := res.FailReason
	if reason == "" {
		reason = "unknown"
	}
	if skipReasons[reason] {
		c.Skipped = 1
	} else {
		c.NoPDF = 1
	}
	c.FailReasons = map[string]int{reason: 1}
	return c
}

// crawlBackoff is how long to wait after n failed attempts at a URL.
func crawlBackoff(n int) time.Duration {
	d := urlRetryInitial
	for range max(n-1, 0) {
		d = time.Duration(float64(d) * urlRetryCoefficient)
		if d >= urlRetryMax {
			return urlRetryMax
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
