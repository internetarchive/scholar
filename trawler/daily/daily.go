// Package daily turns one day of an upstream metadata API into fatcat records,
// crawled PDFs and search documents.
//
// The entry workflow is DailyCrawlWorkflow. A run scrapes one day out of one
// upstream (crossref, arxiv, doaj, datacite, pubmed) into an ndjson object in
// S3, then works that object's lines in batches: each line becomes a release,
// and a release worth having is crawled through Save Page Now, run through
// GROBID and indexed.
//
// The work is organised in batches rather than one ndjson line per activity. A
// run plans the object into byte ranges (plan.go) and hands each range to one
// activity that works it with a pool of its own (batch.go). Temporal therefore
// sees a few events per few hundred lines instead of a few per line, which is
// what keeps a day of crossref from costing more in workflow history than it
// does in crawling. It is also what lets more than one release be in flight at
// once without needing a worker activity slot each. What Temporal still
// guarantees is the part that matters: a batch whose worker dies comes back,
// and its heartbeat checkpoint says where to resume.
package daily

import (
	"errors"
	"fmt"
	"time"

	"git.archive.org/webgroup/scholar/trawler/counts"
	"github.com/spf13/viper"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// SetConcurrencySignal retunes how many releases are crawled at once without
// restarting the workflow. A day of crossref lasts long enough that "back off,
// SPN is unhappy" needs to be answerable while it is in flight.
const SetConcurrencySignal = "set-concurrency"

const (
	defaultConcurrency          = 8
	defaultMaxConcurrency       = 64
	defaultLinesPerCAN          = 20000
	defaultBatchSize            = 200
	defaultBatchesInFlight      = 4
	defaultBatchesPerPlan       = 32
	defaultChunkSize            = 102400
	defaultMaxConsecutiveErrors = 50
	defaultLineAttempts         = 3
	defaultMaxHops              = 8
)

// DailyCounts is the running tally for a day's crawl: what the upstream
// processors hand back, plus the two numbers a batched run needs and a
// per-line activity never did.
//
// Lines and Errored are here rather than in counts.Counts because they are
// facts about how the run walked the file, not about what was found in it --
// the upstream packages have no way to fill them in and no reason to.
type DailyCounts struct {
	counts.Counts
	// Lines is how many ndjson lines reached a verdict.
	Lines int
	// Errored is how many lines exhausted their retries and were abandoned so
	// the run could continue.
	Errored int
}

// Add merges two tallies. It is used to fold each batch result into the
// workflow's running total, including across ContinueAsNew.
//
// It never mutates either operand, which is what lets a batch snapshot its
// running counts into a heartbeat and keep counting.
func (c DailyCounts) Add(o DailyCounts) DailyCounts {
	return DailyCounts{
		Counts:  c.Counts.Add(o.Counts),
		Lines:   c.Lines + o.Lines,
		Errored: c.Errored + o.Errored,
	}
}

// DailyCrawlWorkflowInput drives the workflow. Callers starting a fresh day set
// Upstream (and optionally Day, SourceOverride and Concurrency); every other
// field is either populated from config on the first run or carried across
// ContinueAsNew.
type DailyCrawlWorkflowInput struct {
	// Day value in format 2006-01-02
	Day string
	// SourceOverride sets a static string to use instead of letting trawler
	// generate a source value dynamically
	SourceOverride string
	// Upstream is which API we're scraping
	Upstream string
	// Concurrency is how many releases the run crawls at once, across all of
	// its batches. Zero means "use the configured default".
	Concurrency int

	// The fields below carry state across ContinueAsNew. Callers starting a
	// fresh day leave them at their zero values; the workflow populates them and
	// threads them through each ContinueAsNew so a day resumes where it left off
	// without re-scraping or re-deriving its provenance label.

	// S3Key points to the scraped .ndjson in s3. Empty on the first run, which
	// is the signal to run the scrape activity; populated and carried forward
	// thereafter.
	S3Key string
	// Source is the provenance label for created records. Pinned on the first
	// run (it embeds the RunID, which changes on every ContinueAsNew) and carried
	// forward so a day's records all share one source string.
	Source string
	// Offset is the byte position in the .ndjson to resume reading from. It only
	// ever sits on a batch boundary.
	Offset int64
	// Counts accumulates results across every run in the ContinueAsNew chain so
	// the final run returns the day's grand total.
	Counts DailyCounts

	// The fields below are viper-sourced settings snapshotted once on the first
	// run (see the SideEffect in the workflow) and threaded across
	// ContinueAsNew, so a config change under a running chain cannot desync its
	// replay. We snapshot rather than read viper in the workflow body on every
	// run because viper reads aren't deterministic across replay: a config
	// change in Ansible would otherwise desync a running workflow's history --
	// or silently shift its behavior between runs.
	//
	// A zero BatchesPerPlan means we haven't captured yet. BatchesPerPlan is the
	// sentinel rather than BatchSize because BatchSize existed before this
	// workflow moved to batches, and meant something else then: lines per S3
	// read. A run handed on by a pre-batch worker carries a non-zero one. Keying
	// off a field only this version sets means such a handoff re-reads config
	// and carries on from its byte offset, instead of planning with the batch
	// settings all zero.
	//
	// ExternalTaskQueue is only used by the first-run scrape but is snapshotted
	// with the rest for the same determinism reason.
	MaxConcurrency       int
	LinesPerCAN          int
	BatchSize            int
	BatchesInFlight      int
	BatchesPerPlan       int
	ChunkSize            int
	MaxConsecutiveErrors int
	LineAttempts         int
	InternalTaskQueue    string
	ExternalTaskQueue    string
}

// dailyConfig is the snapshot of viper-sourced settings the workflow captures
// via SideEffect on the first run of a ContinueAsNew chain.
type dailyConfig struct {
	Concurrency          int
	MaxConcurrency       int
	LinesPerCAN          int
	BatchSize            int
	BatchesInFlight      int
	BatchesPerPlan       int
	ChunkSize            int
	MaxConsecutiveErrors int
	LineAttempts         int
	InternalTaskQueue    string
	ExternalTaskQueue    string
}

func nonZero(v, fallback int) int {
	if v <= 0 {
		return fallback
	}
	return v
}

// splitConcurrency turns the run's release concurrency -- the number operators
// actually think in, and the one the retune signal carries -- into how many
// batch activities to keep in flight and how many lines each of them works at
// once.
//
// Spreading the run over several batches is what puts it on more than one
// worker host, so we prefer to use the whole allowance of batches and divide
// the lines between them. Below one line per batch there is nothing to divide,
// and the batch count comes down instead.
func splitConcurrency(concurrency, batchesInFlight int) (batches, parallelism int) {
	concurrency = max(concurrency, 1)
	batches = min(max(batchesInFlight, 1), concurrency)
	parallelism = (concurrency + batches - 1) / batches
	return batches, parallelism
}

// DailyCrawlWorkflow scrapes one day out of one upstream and walks the result
// in batches, working each batch of ndjson lines in one activity.
//
// Per-line failures never fail the run: a day of crossref will always contain
// records fatcat chokes on and PDFs GROBID will not answer for, and one of them
// must not strand the rest of the day. They are counted and logged inside the
// batch. Only a sustained run of errors -- which means something systemic like
// fatcat being down, not a bad record -- or a batch that cannot run at all
// aborts the workflow.
func DailyCrawlWorkflow(ctx workflow.Context, in DailyCrawlWorkflowInput) (DailyCounts, error) {
	l := workflow.GetLogger(ctx)

	if in.Upstream == "" {
		return in.Counts, errors.New("daily: Upstream is required")
	}

	var err error

	var day time.Time
	if in.Day == "" {
		back := -1
		if in.Upstream == "doaj" {
			// DOAJ limits reads for non-paying API callers to only month old data
			back = -32
		}
		day = workflow.Now(ctx).AddDate(0, 0, back)
		in.Day = day.Format("2006-01-02")
	} else {
		day, err = time.Parse("2006-01-02", in.Day)
		if err != nil {
			return in.Counts, err
		}
	}

	// Snapshot config once, on the first run of the chain. SideEffect records
	// the read in history so replays reuse the recorded values rather than
	// re-reading viper, which is what keeps an in-flight chain deterministic
	// when the deployed config changes underneath it.
	if in.BatchesPerPlan == 0 {
		var cfg dailyConfig
		if err := workflow.SideEffect(ctx, func(workflow.Context) any {
			return dailyConfig{
				Concurrency:          viper.GetInt("daily.concurrency"),
				MaxConcurrency:       viper.GetInt("daily.max_concurrency"),
				LinesPerCAN:          viper.GetInt("daily.lines_per_can"),
				BatchSize:            viper.GetInt("daily.batch_size"),
				BatchesInFlight:      viper.GetInt("daily.batches_in_flight"),
				BatchesPerPlan:       viper.GetInt("daily.batches_per_plan"),
				ChunkSize:            viper.GetInt("daily.chunk_size"),
				MaxConsecutiveErrors: viper.GetInt("daily.max_consecutive_errors"),
				LineAttempts:         viper.GetInt("daily.line_attempts"),
				InternalTaskQueue:    viper.GetString(fmt.Sprintf("%s.internal_task_queue", in.Upstream)),
				ExternalTaskQueue:    viper.GetString(fmt.Sprintf("%s.external_task_queue", in.Upstream)),
			}
		}).Get(&cfg); err != nil {
			return in.Counts, err
		}
		in.MaxConcurrency = nonZero(cfg.MaxConcurrency, defaultMaxConcurrency)
		in.LinesPerCAN = nonZero(cfg.LinesPerCAN, defaultLinesPerCAN)
		in.BatchSize = nonZero(cfg.BatchSize, defaultBatchSize)
		in.BatchesInFlight = nonZero(cfg.BatchesInFlight, defaultBatchesInFlight)
		in.BatchesPerPlan = nonZero(cfg.BatchesPerPlan, defaultBatchesPerPlan)
		in.ChunkSize = nonZero(cfg.ChunkSize, defaultChunkSize)
		in.MaxConsecutiveErrors = nonZero(cfg.MaxConsecutiveErrors, defaultMaxConsecutiveErrors)
		in.LineAttempts = nonZero(cfg.LineAttempts, defaultLineAttempts)
		if in.Concurrency <= 0 {
			in.Concurrency = nonZero(cfg.Concurrency, defaultConcurrency)
		}
		in.InternalTaskQueue = cfg.InternalTaskQueue
		in.ExternalTaskQueue = cfg.ExternalTaskQueue
	}
	if in.Concurrency > in.MaxConcurrency {
		in.Concurrency = in.MaxConcurrency
	}

	// First run of the ContinueAsNew chain only: pin the source label and scrape
	// the upstream. Both are threaded through `in` on every subsequent run so we
	// never re-scrape and a day's records all share one source string.
	if in.S3Key == "" {
		source := in.SourceOverride
		if source == "" {
			rid := workflow.GetInfo(ctx).WorkflowExecution.RunID
			if len(rid) > 8 {
				rid = rid[:8]
			}
			source = fmt.Sprintf("daily-%s-%s-%s", day.Format("20060102"), rid, in.Upstream)
		}
		in.Source = source

		// fetch metadata from the upstream API and store in s3
		scrapeCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
			StartToCloseTimeout: 4 * 60 * 60 * time.Second,
			TaskQueue:           in.ExternalTaskQueue,
			RetryPolicy: &temporal.RetryPolicy{
				InitialInterval:    time.Second,
				BackoffCoefficient: 10.0,
				MaximumInterval:    time.Second * 480,
				MaximumAttempts:    6,
			},
		})
		scrapeIn := scholkitScrapeInput{
			Day:      in.Day,
			Upstream: in.Upstream,
		}
		var scrapeOut scholkitScrapeOutput
		if err := workflow.ExecuteActivity(scrapeCtx, ScholkitScrapeActivity, scrapeIn).Get(scrapeCtx, &scrapeOut); err != nil {
			l.Error(fmt.Sprintf("scholkit %s activity failed: %s", in.Upstream, err))
			return in.Counts, err
		}
		if scrapeOut.S3Key == "" {
			return in.Counts, fmt.Errorf("scholkit %s returned an empty s3 key", in.Upstream)
		}
		in.S3Key = scrapeOut.S3Key
		l.Info(fmt.Sprintf("scholkit %s s3key: %s", in.Upstream, in.S3Key))
	}

	planCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 10 * time.Minute,
		TaskQueue:           in.InternalTaskQueue,
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
	// the checkpoint rather than redoing the batch. Most of what lands here is a
	// worker restart, and a run must survive several of those without giving up
	// on a batch.
	crawlCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 8 * time.Hour,
		HeartbeatTimeout:    20 * time.Minute,
		TaskQueue:           in.InternalTaskQueue,
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
			Source:               in.Source,
			Upstream:             in.Upstream,
			ChunkSize:            in.ChunkSize,
			Parallelism:          parallelism,
			LineAttempts:         in.LineAttempts,
			MaxConsecutiveErrors: in.MaxConsecutiveErrors,
		})
		inFlight++
		sel.AddFuture(f, func(f workflow.Future) {
			inFlight--
			var res CrawlBatchOutput
			if err := f.Get(ctx, &res); err != nil {
				// A batch only fails for something systemic -- an unreadable
				// ndjson, an unwritable log, a batch stopped before it finished.
				// A record that will not process is counted inside the batch and
				// never reaches here, so unlike a single line this is not
				// something to absorb and carry on from.
				if failure == nil {
					failure = fmt.Errorf("daily: batch of %d lines at offset %d failed: %w",
						ref.LineCount, ref.Offset, err)
				}
				l.Error("batch failed", "offset", ref.Offset, "err", err)
				return
			}
			in.Counts = in.Counts.Add(res.Counts)
			if res.Aborted && failure == nil {
				failure = fmt.Errorf("daily: batch at offset %d aborted: %s (resume at offset %d)",
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
	// accounted for. A run that is already failing skips the wait: the records
	// for everything those batches finished are written either way, and grinding
	// hundreds more releases against whatever just broke helps nobody.
	quiesce := func() error {
		if failure == nil {
			drain()
		}
		return failure
	}

	var linesThisRun int
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
			// already running: a batch's parallelism is fixed when it starts. So
			// the knob takes a batch to bite rather than a line, which is the
			// price of not paying Temporal per line.
			batches, parallelism := splitConcurrency(in.Concurrency, in.BatchesInFlight)

			for inFlight >= batches {
				sel.Select(ctx)
			}
			if failure != nil {
				stop = true
				break
			}

			dispatch(ref, parallelism)
			linesThisRun += ref.LineCount
			// Resume just past the batch we last dispatched. Everything
			// dispatched is drained before ContinueAsNew below, so this neither
			// re-processes nor skips.
			in.Offset = ref.End

			if linesThisRun >= in.LinesPerCAN || workflow.GetInfo(ctx).GetContinueAsNewSuggested() {
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
				"daily: planning %s made no progress at offset %d", in.S3Key, in.Offset)
		}

		in.Offset = plan.NextOffset

		if plan.EOF {
			if err := quiesce(); err != nil {
				return in.Counts, err
			}
			l.Info("day complete", "source", in.Source, "s3key", in.S3Key, "counts", in.Counts)
			return in.Counts, nil
		}
	}

	if err := quiesce(); err != nil {
		return in.Counts, err
	}

	l.Info("continue-as-new",
		"source", in.Source, "s3key", in.S3Key, "offset", in.Offset, "lines", linesThisRun)
	return in.Counts, workflow.NewContinueAsNewError(ctx, DailyCrawlWorkflow, in)
}
