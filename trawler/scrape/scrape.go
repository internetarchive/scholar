// Package scrape runs a flat list of URLs through Save Page Now so that
// whatever sits behind them lands in the Wayback Machine.
//
// It is deliberately much smaller than the daily crawl: no upstream metadata
// API, no fatcat records, no GROBID, no search indexing. Read URLs, crawl them,
// count what happened. It exists for piles of URLs we simply want captured --
// for example the crawl-only half of an ETD ingest split, where getting a
// landing page or even a single chapter PDF into Wayback is the entire goal.
//
// PDFCrawler.Crawl is still the crawler, so a URL that does resolve to a PDF is
// reported as such; we just do not do anything with the bytes afterwards.
//
// The work is organised in batches rather than one URL per activity. A run
// plans the list into byte ranges (plan.go) and hands each range to one
// activity that crawls it with a pool of its own (crawl.go). Temporal therefore
// sees a few events per few hundred URLs instead of a few per URL, which is
// what keeps a scrape of millions of URLs from costing more in workflow history
// than it does in crawling. What Temporal still guarantees is the part that
// matters: a batch whose worker dies comes back, and its heartbeat checkpoint
// says where to resume.
package scrape

import (
	"errors"
	"fmt"
	"time"

	"github.com/spf13/viper"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// SetConcurrencySignal retunes how many crawls run at once without restarting
// the workflow. A run over millions of URLs lasts long enough that "back off,
// SPN is unhappy" needs to be answerable while it is in flight.
const SetConcurrencySignal = "set-concurrency"

const (
	defaultConcurrency          = 4
	defaultMaxConcurrency       = 64
	defaultURLsPerCAN           = 50000
	defaultBatchSize            = 200
	defaultBatchesInFlight      = 4
	defaultBatchesPerPlan       = 64
	defaultChunkSize            = 102400
	defaultMaxConsecutiveErrors = 50
	defaultURLAttempts          = 4
)

// ScrapeCounts is the running tally for a scrape. Crawl outcomes that are
// simply "no PDF at the other end" are normal and expected, so they are counted
// rather than treated as failures.
type ScrapeCounts struct {
	// URLs is how many URLs reached a verdict.
	URLs int
	// PDFFound is how many crawls ended on a PDF in Wayback.
	PDFFound int
	// NoPDF is how many crawls completed without reaching a PDF. In most of
	// these SPN still captured a page, so they are not wasted for our purposes.
	NoPDF int
	// Skipped is how many URLs never reached the network at all, because the
	// crawler rejected them up front (blocklist, unparsable URL).
	Skipped int
	// Errored is how many crawls exhausted their retries and were abandoned so
	// the run could continue.
	Errored int
	// FailReasons breaks NoPDF and Skipped down by CrawlResult.FailReason.
	FailReasons map[string]int
}

// Add merges two tallies. It is used to fold each batch result into the
// workflow's running total, including across ContinueAsNew.
//
// It never mutates either operand, which is what lets a batch snapshot its
// running counts into a heartbeat and keep counting.
func (c ScrapeCounts) Add(o ScrapeCounts) ScrapeCounts {
	out := ScrapeCounts{
		URLs:     c.URLs + o.URLs,
		PDFFound: c.PDFFound + o.PDFFound,
		NoPDF:    c.NoPDF + o.NoPDF,
		Skipped:  c.Skipped + o.Skipped,
		Errored:  c.Errored + o.Errored,
	}
	if len(c.FailReasons) > 0 || len(o.FailReasons) > 0 {
		out.FailReasons = make(map[string]int, len(c.FailReasons)+len(o.FailReasons))
		for k, v := range c.FailReasons {
			out.FailReasons[k] += v
		}
		for k, v := range o.FailReasons {
			out.FailReasons[k] += v
		}
	}
	return out
}

// ScrapeToWaybackInput drives the workflow. Callers starting a fresh run set
// S3Key (and optionally Concurrency); every other field is either populated
// from config on the first run or carried across ContinueAsNew.
type ScrapeToWaybackInput struct {
	// S3Key points at a newline-delimited list of URLs, one per line. Blank
	// lines are ignored.
	S3Key string
	// Concurrency is how many URLs the run crawls at once, across all of its
	// batches. Zero means "use the configured default".
	Concurrency int
	// TaskQueue is the Temporal task queue for both activities.
	TaskQueue string

	// Offset is the byte position in the list to resume reading from. It only
	// ever sits on a batch boundary.
	Offset int64
	// Counts accumulates across every run in the ContinueAsNew chain.
	Counts ScrapeCounts

	// The fields below are viper-sourced settings snapshotted once on the first
	// run (see the SideEffect in the workflow) and threaded across
	// ContinueAsNew, so a config change under a running chain cannot desync its
	// replay. A zero BatchesPerPlan means we have not captured them yet.
	//
	// BatchesPerPlan is the sentinel rather than BatchSize because BatchSize
	// existed before this workflow moved to batches, and meant something else
	// then: a run handed on by a pre-batch worker carries a non-zero one. Keying
	// off a field only this version sets means such a handoff re-reads config
	// and carries on from its byte offset, instead of starting with the batch
	// settings all zero.
	MaxConcurrency       int
	URLsPerCAN           int
	BatchSize            int
	BatchesInFlight      int
	BatchesPerPlan       int
	ChunkSize            int
	MaxConsecutiveErrors int
	URLAttempts          int
}

// scrapeConfig is the snapshot of viper-sourced settings taken via SideEffect.
type scrapeConfig struct {
	Concurrency          int
	MaxConcurrency       int
	URLsPerCAN           int
	BatchSize            int
	BatchesInFlight      int
	BatchesPerPlan       int
	ChunkSize            int
	MaxConsecutiveErrors int
	URLAttempts          int
	TaskQueue            string
}

func nonZero(v, fallback int) int {
	if v <= 0 {
		return fallback
	}
	return v
}

// splitConcurrency turns the run's URL concurrency -- the number operators
// actually think in, and the one the retune signal carries -- into how many
// batch activities to keep in flight and how many crawls each of them runs at
// once.
//
// Spreading the run over several batches is what puts it on more than one
// worker host, so we prefer to use the whole allowance of batches and divide
// the crawls between them. Below one crawl per batch there is nothing to
// divide, and the batch count comes down instead.
func splitConcurrency(concurrency, batchesInFlight int) (batches, parallelism int) {
	concurrency = max(concurrency, 1)
	batches = min(max(batchesInFlight, 1), concurrency)
	parallelism = (concurrency + batches - 1) / batches
	return batches, parallelism
}

// ScrapeToWaybackWorkflow walks a URL list in batches, crawling each batch in
// one activity.
//
// Per-URL failures never fail the run: a list of millions of URLs will always
// contain dead hosts, and one of them must not strand the other millions. They
// are counted and logged inside the batch. Only a sustained run of errors --
// which means something systemic like bad SPN credentials, not a bad URL -- or
// a batch that cannot run at all aborts the workflow.
func ScrapeToWaybackWorkflow(ctx workflow.Context, in ScrapeToWaybackInput) (ScrapeCounts, error) {
	l := workflow.GetLogger(ctx)

	if in.S3Key == "" {
		return in.Counts, errors.New("scrape: S3Key is required")
	}

	// Snapshot config once, on the first run of the chain. SideEffect records
	// the read in history so replays reuse the recorded values rather than
	// re-reading viper, which is what keeps an in-flight chain deterministic
	// when the deployed config changes underneath it.
	if in.BatchesPerPlan == 0 {
		var cfg scrapeConfig
		if err := workflow.SideEffect(ctx, func(workflow.Context) any {
			return scrapeConfig{
				Concurrency:          viper.GetInt("scrape.concurrency"),
				MaxConcurrency:       viper.GetInt("scrape.max_concurrency"),
				URLsPerCAN:           viper.GetInt("scrape.urls_per_can"),
				BatchSize:            viper.GetInt("scrape.batch_size"),
				BatchesInFlight:      viper.GetInt("scrape.batches_in_flight"),
				BatchesPerPlan:       viper.GetInt("scrape.batches_per_plan"),
				ChunkSize:            viper.GetInt("scrape.chunk_size"),
				MaxConsecutiveErrors: viper.GetInt("scrape.max_consecutive_errors"),
				URLAttempts:          viper.GetInt("scrape.url_attempts"),
				TaskQueue:            viper.GetString("scrape.task_queue"),
			}
		}).Get(&cfg); err != nil {
			return in.Counts, err
		}
		in.MaxConcurrency = nonZero(cfg.MaxConcurrency, defaultMaxConcurrency)
		in.URLsPerCAN = nonZero(cfg.URLsPerCAN, defaultURLsPerCAN)
		in.BatchSize = nonZero(cfg.BatchSize, defaultBatchSize)
		in.BatchesInFlight = nonZero(cfg.BatchesInFlight, defaultBatchesInFlight)
		in.BatchesPerPlan = nonZero(cfg.BatchesPerPlan, defaultBatchesPerPlan)
		in.ChunkSize = nonZero(cfg.ChunkSize, defaultChunkSize)
		in.MaxConsecutiveErrors = nonZero(cfg.MaxConsecutiveErrors, defaultMaxConsecutiveErrors)
		in.URLAttempts = nonZero(cfg.URLAttempts, defaultURLAttempts)
		if in.Concurrency <= 0 {
			in.Concurrency = nonZero(cfg.Concurrency, defaultConcurrency)
		}
		if in.TaskQueue == "" {
			in.TaskQueue = cfg.TaskQueue
		}
	}
	if in.Concurrency > in.MaxConcurrency {
		in.Concurrency = in.MaxConcurrency
	}

	planCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 10 * time.Minute,
		TaskQueue:           in.TaskQueue,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    5 * time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    2 * time.Minute,
			MaximumAttempts:    5,
		},
	})

	// A batch is hours of work in one activity, so the heartbeat -- not the
	// StartToClose budget -- is what says whether it is alive. StartToClose is
	// only a backstop against a batch that keeps heartbeating and never
	// finishes.
	//
	// The attempt budget is generous because a retry is cheap: it resumes from
	// the checkpoint rather than re-crawling the batch. Most of what lands here
	// is a worker restart, and a run must survive several of those without
	// giving up on a batch.
	crawlCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 6 * time.Hour,
		HeartbeatTimeout:    5 * time.Minute,
		TaskQueue:           in.TaskQueue,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    30 * time.Second,
			BackoffCoefficient: 1.5,
			MaximumInterval:    5 * time.Minute,
			MaximumAttempts:    10,
		},
	})

	sigCh := workflow.GetSignalChannel(ctx, SetConcurrencySignal)

	sel := workflow.NewSelector(ctx)
	inFlight := 0

	// failure is the first reason we have to stop the run. It is set from the
	// selector callback and read by the dispatch loop, which is why it is not
	// simply returned.
	var failure error

	// dispatch starts a batch and registers its completion handler. Results are
	// folded in as they land, in whatever order they land; the selector callback
	// is the only place Counts is mutated.
	dispatch := func(ref BatchRef, parallelism int) {
		f := workflow.ExecuteActivity(crawlCtx, CrawlBatchActivity, CrawlBatchInput{
			S3Key:                in.S3Key,
			Batch:                ref,
			ChunkSize:            in.ChunkSize,
			Parallelism:          parallelism,
			URLAttempts:          in.URLAttempts,
			MaxConsecutiveErrors: in.MaxConsecutiveErrors,
		})
		inFlight++
		sel.AddFuture(f, func(f workflow.Future) {
			inFlight--
			var res CrawlBatchOutput
			if err := f.Get(ctx, &res); err != nil {
				// A batch only fails for something systemic -- an unreadable
				// list, an unwritable log, a batch stopped before it finished.
				// A URL that will not crawl is counted inside the batch and
				// never reaches here, so unlike a single URL this is not
				// something to absorb and carry on from.
				if failure == nil {
					failure = fmt.Errorf("scrape: batch of %d urls at offset %d failed: %w",
						ref.URLCount, ref.Offset, err)
				}
				l.Error("batch failed", "offset", ref.Offset, "err", err)
				return
			}
			in.Counts = in.Counts.Add(res.Counts)
			if res.Aborted && failure == nil {
				failure = fmt.Errorf("scrape: batch at offset %d aborted: %s (resume at offset %d)",
					ref.Offset, res.AbortReason, res.NextOffset)
			}
		})
	}

	drain := func() {
		for inFlight > 0 {
			sel.Select(ctx)
		}
	}

	// quiesce waits for the in-flight batches so the offset we hand on is fully
	// accounted for. A run that is already failing skips the wait: the rows for
	// everything those batches finished are on disk either way, and grinding
	// hundreds more URLs against whatever just broke helps nobody.
	quiesce := func() error {
		if failure == nil {
			drain()
		}
		return failure
	}

	var urlsThisRun int
	stop := false

	for !stop {
		var plan PlanBatchesOutput
		if err := workflow.ExecuteActivity(planCtx, PlanBatchesActivity, PlanBatchesInput{
			S3Key:      in.S3Key,
			Offset:     in.Offset,
			BatchSize:  in.BatchSize,
			MaxBatches: in.BatchesPerPlan,
			ChunkSize:  in.ChunkSize,
		}).Get(planCtx, &plan); err != nil {
			return in.Counts, err
		}

		for _, ref := range plan.Batches {
			// Apply any pending retune before deciding whether the window is
			// full. ReceiveAsync keeps this deterministic without a coroutine.
			var want int
			for sigCh.ReceiveAsync(&want) {
				if want > 0 && want <= in.MaxConcurrency {
					l.Info("concurrency retuned", "from", in.Concurrency, "to", want)
					in.Concurrency = want
				} else {
					l.Warn("ignoring out-of-range concurrency signal",
						"want", want, "max", in.MaxConcurrency)
				}
			}

			// A retune reaches the batches dispatched after it, not the ones
			// already running: a batch's parallelism is fixed when it starts.
			// So the knob takes a batch to bite rather than a URL, which is the
			// price of not paying Temporal per URL.
			batches, parallelism := splitConcurrency(in.Concurrency, in.BatchesInFlight)

			for inFlight >= batches {
				sel.Select(ctx)
			}
			if failure != nil {
				stop = true
				break
			}

			dispatch(ref, parallelism)
			urlsThisRun += ref.URLCount
			// Resume just past the batch we last dispatched. Everything
			// dispatched is drained before ContinueAsNew below, so this neither
			// re-crawls nor skips.
			in.Offset = ref.End

			if urlsThisRun >= in.URLsPerCAN || workflow.GetInfo(ctx).GetContinueAsNewSuggested() {
				stop = true
				break
			}
		}

		if stop {
			break
		}

		// A planning call that yields no batches and does not advance would
		// spin forever. That should be impossible, but this loop is the one
		// place a bad read could hang a run indefinitely rather than fail it.
		if len(plan.Batches) == 0 && !plan.EOF && plan.NextOffset == in.Offset {
			if err := quiesce(); err != nil {
				return in.Counts, err
			}
			return in.Counts, fmt.Errorf(
				"scrape: planning %s made no progress at offset %d", in.S3Key, in.Offset)
		}

		in.Offset = plan.NextOffset

		if plan.EOF {
			if err := quiesce(); err != nil {
				return in.Counts, err
			}
			l.Info("scrape complete", "s3key", in.S3Key, "counts", in.Counts)
			return in.Counts, nil
		}
	}

	if err := quiesce(); err != nil {
		return in.Counts, err
	}

	l.Info("continue-as-new", "s3key", in.S3Key, "offset", in.Offset, "urls", urlsThisRun)
	return in.Counts, workflow.NewContinueAsNewError(ctx, ScrapeToWaybackWorkflow, in)
}
