// Package periodic ingests PDFs out of WARCs on petabox, rather than off the
// live web the way `daily` does. We complement the daily crawling with periodic
// heritrix (or whatever) wide crawls in the hope of capturing PDFs, and this is
// what turns the results of one of those crawls into fatcat files and search
// documents.
//
// The entry workflow is PeriodicIngestWorkflow. A run walks one IA collection:
// it pins the collection's item list, stages each item's PDF CDX rows in S3,
// and works those rows in batches.
//
// The work is organised in batches rather than one CDX row per activity. A run
// plans the collection into byte ranges (plan.go) and hands each range to one
// activity that ingests it with a pool of its own (batch.go). Temporal
// therefore sees a few events per few hundred PDFs instead of a few per PDF,
// which is what keeps a collection of millions of rows from costing more in
// workflow history than it does in GROBID time. It is also what lets more than
// one PDF be in flight at once without needing a worker activity slot each.
// What Temporal still guarantees is the part that matters: a batch whose worker
// dies comes back, and its heartbeat checkpoint says where to resume.
package periodic

import (
	"errors"
	"fmt"
	"time"

	"github.com/spf13/viper"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// SetConcurrencySignal retunes how many PDFs are ingested at once without
// restarting the workflow. A run over a whole crawl collection lasts long
// enough that "back off, GROBID is unhappy" needs to be answerable while it is
// in flight.
const SetConcurrencySignal = "set-concurrency"

const (
	defaultConcurrency          = 8
	defaultMaxConcurrency       = 64
	defaultLinesPerCAN          = 20000
	defaultBatchSize            = 100
	defaultBatchesInFlight      = 4
	defaultBatchesPerPlan       = 32
	defaultItemsPerPlan         = 8
	defaultChunkSize            = 102400
	defaultMaxConsecutiveErrors = 50
	defaultLineAttempts         = 3
)

// PeriodicCounts is the running tally for an ingest. Most PDF CDX rows are
// skipped -- already in fatcat, already indexed, no external ID to hang a
// release off -- so skipping is counted rather than treated as a failure.
type PeriodicCounts struct {
	// Lines is how many PDF CDX rows reached a verdict.
	Lines int
	// PdfsSkipped is how many rows we passed on without adding to any DB or index
	PdfsSkipped int
	// PdfsKnownBad is how many rows the negative cache let us pass on without
	// reading the PDF at all. It is counted apart from PdfsSkipped so that the
	// cache's contribution is legible: on a first pass it is zero, and on a
	// replay it is the work that did not have to happen. See badPDFCache.
	PdfsKnownBad int
	// FilesUpdated is how many existing file records we added a URL to
	FilesUpdated int
	// PdfsFailed is how many PDFs grobid choked on
	PdfsFailed int
	// PdfsErrored is how many rows exhausted their retries and were abandoned so
	// the run could continue.
	PdfsErrored int
	// PdfsAddedToDB is how many new file records we created
	PdfsAddedToDB int
	// PdfsAddedToES is how many PDFs we added to fatcat's file index
	PdfsAddedToES int
	// FulltextAddedToES is how many PDFs whose full text we ingested into ES
	FulltextAddedToES int
}

// Add merges two tallies. It is used to fold each batch result into the
// workflow's running total, including across ContinueAsNew.
//
// It never mutates either operand, which is what lets a batch snapshot its
// running counts into a heartbeat and keep counting.
func (c PeriodicCounts) Add(oc PeriodicCounts) PeriodicCounts {
	return PeriodicCounts{
		Lines:             c.Lines + oc.Lines,
		PdfsSkipped:       c.PdfsSkipped + oc.PdfsSkipped,
		PdfsKnownBad:      c.PdfsKnownBad + oc.PdfsKnownBad,
		PdfsAddedToDB:     c.PdfsAddedToDB + oc.PdfsAddedToDB,
		PdfsAddedToES:     c.PdfsAddedToES + oc.PdfsAddedToES,
		PdfsFailed:        c.PdfsFailed + oc.PdfsFailed,
		PdfsErrored:       c.PdfsErrored + oc.PdfsErrored,
		FulltextAddedToES: c.FulltextAddedToES + oc.FulltextAddedToES,
		FilesUpdated:      c.FilesUpdated + oc.FilesUpdated,
	}
}

// PeriodicIngestInput drives the workflow. Callers starting a fresh run set
// CollectionName (and optionally SourceOverride and Concurrency); every other
// field is either populated from config on the first run or carried across
// ContinueAsNew.
type PeriodicIngestInput struct {
	// CollectionName is which petabox collection of warcs/cdx to ingest.
	CollectionName string
	// SourceOverride overrides the default source name generation.
	SourceOverride string
	// Concurrency is how many PDFs the run ingests at once, across all of its
	// batches. Zero means "use the configured default".
	Concurrency int
	// TaskQueue is the Temporal task queue for every activity.
	TaskQueue string

	// StartAtItem begins the run at a named item rather than at the top of the
	// collection, and StartAtOffset begins it part way into that item. They are
	// resolved against the item list this run pins, so they are honoured only on
	// a fresh start -- a run resumed from its own ContinueAsNew carries a Cursor
	// and ignores these.
	//
	// They exist for picking up where a previous, separate run left off: the
	// per-row work is idempotent, but proving a row is already done still costs
	// lookups against fatcat and both ES indices, and doing that for a
	// collection somebody already ingested is hours of no-ops. Naming the item
	// rather than an index is deliberate -- an index means nothing across two
	// enumerations of a collection that has grown since.
	//
	// The offset is a byte position in the item's staged PDF CDX and must land
	// on a line boundary. One that does not costs a single garbage row, logged
	// as an unparsable skip; the scan re-syncs at the next newline.
	StartAtItem   string
	StartAtOffset int64

	// Source is the resolved provenance label. It is resolved once, on the first
	// run, and carried: deriving it per run would stamp every ContinueAsNew
	// handoff of the same ingest with a different source.
	Source string
	// ItemListKey is the pinned list of items this run walks; see ItemListKey.
	ItemListKey string
	// ItemCount is how many items that list holds, for progress logging.
	ItemCount int
	// Cursor is the resume point: which item, and how far into it.
	Cursor ItemCursor
	// Counts accumulates across every run in the ContinueAsNew chain.
	Counts PeriodicCounts

	// The fields below are viper-sourced settings snapshotted once on the first
	// run (see the SideEffect in the workflow) and threaded across
	// ContinueAsNew, so a config change under a running chain cannot desync its
	// replay. A zero BatchesPerPlan means we have not captured them yet.
	MaxConcurrency       int
	LinesPerCAN          int
	BatchSize            int
	BatchesInFlight      int
	BatchesPerPlan       int
	ItemsPerPlan         int
	ChunkSize            int
	MaxConsecutiveErrors int
	LineAttempts         int
}

// periodicConfig is the snapshot of viper-sourced settings taken via SideEffect.
type periodicConfig struct {
	Concurrency          int
	MaxConcurrency       int
	LinesPerCAN          int
	BatchSize            int
	BatchesInFlight      int
	BatchesPerPlan       int
	ItemsPerPlan         int
	ChunkSize            int
	MaxConsecutiveErrors int
	LineAttempts         int
	TaskQueue            string
}

func nonZero(v, fallback int) int {
	if v <= 0 {
		return fallback
	}
	return v
}

// splitConcurrency turns the run's PDF concurrency -- the number operators
// actually think in, and the one the retune signal carries -- into how many
// batch activities to keep in flight and how many rows each of them ingests at
// once.
//
// Spreading the run over several batches is what puts it on more than one
// worker host, so we prefer to use the whole allowance of batches and divide
// the rows between them. Below one row per batch there is nothing to divide,
// and the batch count comes down instead.
func splitConcurrency(concurrency, batchesInFlight int) (batches, parallelism int) {
	concurrency = max(concurrency, 1)
	batches = min(max(batchesInFlight, 1), concurrency)
	parallelism = (concurrency + batches - 1) / batches
	return batches, parallelism
}

// PeriodicIngestWorkflow walks an IA crawl collection in batches, ingesting
// each batch of PDF CDX rows in one activity.
//
// Per-row failures never fail the run: a crawl collection will always contain
// PDFs petabox will not serve and PDFs GROBID will not answer for, and one of
// them must not strand the rest of the collection. They are counted and logged
// inside the batch. Only a sustained run of errors -- which means something
// systemic like GROBID being down, not a bad PDF -- or a batch that cannot run
// at all aborts the workflow.
func PeriodicIngestWorkflow(ctx workflow.Context, in PeriodicIngestInput) (PeriodicCounts, error) {
	l := workflow.GetLogger(ctx)

	if in.CollectionName == "" {
		return in.Counts, errors.New("periodic: CollectionName is required")
	}

	// Snapshot config once, on the first run of the chain. SideEffect records
	// the read in history so replays reuse the recorded values rather than
	// re-reading viper, which is what keeps an in-flight chain deterministic
	// when the deployed config changes underneath it.
	if in.BatchesPerPlan == 0 {
		var cfg periodicConfig
		if err := workflow.SideEffect(ctx, func(workflow.Context) any {
			return periodicConfig{
				Concurrency:          viper.GetInt("periodic.concurrency"),
				MaxConcurrency:       viper.GetInt("periodic.max_concurrency"),
				LinesPerCAN:          viper.GetInt("periodic.lines_per_can"),
				BatchSize:            viper.GetInt("periodic.batch_size"),
				BatchesInFlight:      viper.GetInt("periodic.batches_in_flight"),
				BatchesPerPlan:       viper.GetInt("periodic.batches_per_plan"),
				ItemsPerPlan:         viper.GetInt("periodic.items_per_plan"),
				ChunkSize:            viper.GetInt("periodic.chunk_size"),
				MaxConsecutiveErrors: viper.GetInt("periodic.max_consecutive_errors"),
				LineAttempts:         viper.GetInt("periodic.line_attempts"),
				TaskQueue:            viper.GetString("periodic.task_queue"),
			}
		}).Get(&cfg); err != nil {
			return in.Counts, err
		}
		in.MaxConcurrency = nonZero(cfg.MaxConcurrency, defaultMaxConcurrency)
		in.LinesPerCAN = nonZero(cfg.LinesPerCAN, defaultLinesPerCAN)
		in.BatchSize = nonZero(cfg.BatchSize, defaultBatchSize)
		in.BatchesInFlight = nonZero(cfg.BatchesInFlight, defaultBatchesInFlight)
		in.BatchesPerPlan = nonZero(cfg.BatchesPerPlan, defaultBatchesPerPlan)
		in.ItemsPerPlan = nonZero(cfg.ItemsPerPlan, defaultItemsPerPlan)
		in.ChunkSize = nonZero(cfg.ChunkSize, defaultChunkSize)
		in.MaxConsecutiveErrors = nonZero(cfg.MaxConsecutiveErrors, defaultMaxConsecutiveErrors)
		in.LineAttempts = nonZero(cfg.LineAttempts, defaultLineAttempts)
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

	info := workflow.GetInfo(ctx)

	// Resolve the provenance label once and carry it. Deriving it per run would
	// stamp every ContinueAsNew handoff of one ingest with a different source,
	// which is exactly what makes a source label useless later.
	if in.Source == "" {
		if in.SourceOverride != "" {
			in.Source = in.SourceOverride
		} else {
			runid := info.WorkflowExecution.RunID
			if len(runid) > 8 {
				runid = runid[:8]
			}
			in.Source = fmt.Sprintf("periodic-%s-%s-%s",
				workflow.Now(ctx).Format("20060102"), runid, in.CollectionName)
		}
	}

	listCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 2 * time.Hour,
		HeartbeatTimeout:    10 * time.Minute,
		TaskQueue:           in.TaskQueue,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    30 * time.Second,
			BackoffCoefficient: 1.5,
			MaximumInterval:    3 * time.Minute,
			MaximumAttempts:    10,
		},
	})

	// Enumerating the collection is the one thing that has to happen before
	// anything else, and exactly once: the cursor is an index into the list it
	// writes, and a collection that grows while we ingest it would shift every
	// index under a running cursor.
	if in.ItemListKey == "" {
		key := ItemListKey(info.WorkflowExecution.ID, info.WorkflowExecution.RunID)
		var listOut ListCollectionOutput
		if err := workflow.ExecuteActivity(listCtx, ListCollectionActivity, ListCollectionInput{
			CollectionName: in.CollectionName,
			ItemListKey:    key,
			StartAtItem:    in.StartAtItem,
		}).Get(ctx, &listOut); err != nil {
			return in.Counts, err
		}
		if listOut.ItemCount == 0 {
			l.Warn("found no items in collection, exiting", "collection", in.CollectionName)
			return in.Counts, nil
		}
		in.ItemListKey = key
		in.ItemCount = listOut.ItemCount
		l.Info("pinned collection item list",
			"collection", in.CollectionName, "items", in.ItemCount, "key", key)

		if in.StartAtItem != "" {
			in.Cursor = ItemCursor{Index: listOut.StartIndex, Offset: in.StartAtOffset}
			l.Info("skipping ahead to a named item",
				"item", in.StartAtItem,
				"index", listOut.StartIndex,
				"of", in.ItemCount,
				"offset", in.StartAtOffset)
		}
	}

	// Planning stages items, which means downloading and filtering whole rollup
	// CDX files, so it gets a real timeout and heartbeats rather than the
	// seconds a pure list-cutting call would need.
	planCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 1 * time.Hour,
		HeartbeatTimeout:    10 * time.Minute,
		TaskQueue:           in.TaskQueue,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    30 * time.Second,
			BackoffCoefficient: 1.5,
			MaximumInterval:    3 * time.Minute,
			MaximumAttempts:    10,
		},
	})

	// A batch is hours of work in one activity, so the heartbeat -- not the
	// StartToClose budget -- is what says whether it is alive. StartToClose is
	// only a backstop against a batch that keeps heartbeating and never
	// finishes.
	//
	// The attempt budget is generous because a retry is cheap: it resumes from
	// the checkpoint rather than re-ingesting the batch. Most of what lands here
	// is a worker restart, and a run must survive several of those without
	// giving up on a batch.
	ingestCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 8 * time.Hour,
		HeartbeatTimeout:    20 * time.Minute,
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
		f := workflow.ExecuteActivity(ingestCtx, IngestBatchActivity, IngestBatchInput{
			Batch:                ref,
			CollectionName:       in.CollectionName,
			Source:               in.Source,
			ChunkSize:            in.ChunkSize,
			Parallelism:          parallelism,
			LineAttempts:         in.LineAttempts,
			MaxConsecutiveErrors: in.MaxConsecutiveErrors,
		})
		inFlight++
		sel.AddFuture(f, func(f workflow.Future) {
			inFlight--
			var res IngestBatchOutput
			if err := f.Get(ctx, &res); err != nil {
				// A batch only fails for something systemic -- an unreadable
				// staged CDX, an unwritable log, a batch stopped before it
				// finished. A PDF that will not ingest is counted inside the
				// batch and never reaches here, so unlike a single row this is
				// not something to absorb and carry on from.
				if failure == nil {
					failure = fmt.Errorf("periodic: batch of %d rows from %s at offset %d failed: %w",
						ref.LineCount, ref.ItemID, ref.Offset, err)
				}
				l.Error("batch failed", "item", ref.ItemID, "offset", ref.Offset, "err", err)
				return
			}
			in.Counts = in.Counts.Add(res.Counts)
			if res.Aborted && failure == nil {
				failure = fmt.Errorf(
					"periodic: batch from %s at offset %d aborted: %s (resume at item %d offset %d)",
					ref.ItemID, ref.Offset, res.AbortReason, ref.ItemIndex, res.NextOffset)
			}
		})
	}

	drain := func() {
		for inFlight > 0 {
			sel.Select(ctx)
		}
	}

	// quiesce waits for the in-flight batches so the cursor we hand on is fully
	// accounted for. A run that is already failing skips the wait: the records
	// for everything those batches finished are written either way, and grinding
	// hundreds more PDFs against whatever just broke helps nobody.
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
			CollectionName: in.CollectionName,
			ItemListKey:    in.ItemListKey,
			Cursor:         in.Cursor,
			BatchSize:      in.BatchSize,
			MaxBatches:     in.BatchesPerPlan,
			MaxItems:       in.ItemsPerPlan,
			ChunkSize:      in.ChunkSize,
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
			// the knob takes a batch to bite rather than a PDF, which is the
			// price of not paying Temporal per PDF.
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
			// re-ingests nor skips.
			in.Cursor = ref.NextCursor

			if linesThisRun >= in.LinesPerCAN || workflow.GetInfo(ctx).GetContinueAsNewSuggested() {
				stop = true
				break
			}
		}

		if stop {
			break
		}

		// A planning call that yields no batches and does not advance would spin
		// forever. That should be impossible, but this loop is the one place a
		// bad read could hang a run indefinitely rather than fail it.
		if len(plan.Batches) == 0 && !plan.EOF && plan.NextCursor == in.Cursor {
			if err := quiesce(); err != nil {
				return in.Counts, err
			}
			return in.Counts, fmt.Errorf(
				"periodic: planning %s made no progress at item %d offset %d",
				in.CollectionName, in.Cursor.Index, in.Cursor.Offset)
		}

		in.Cursor = plan.NextCursor

		if plan.EOF {
			if err := quiesce(); err != nil {
				return in.Counts, err
			}
			l.Info("collection complete",
				"collection", in.CollectionName, "items", in.ItemCount, "counts", in.Counts)
			return in.Counts, nil
		}
	}

	if err := quiesce(); err != nil {
		return in.Counts, err
	}

	l.Info("continue-as-new",
		"collection", in.CollectionName,
		"item", in.Cursor.Index,
		"of", in.ItemCount,
		"offset", in.Cursor.Offset,
		"lines", linesThisRun)
	return in.Counts, workflow.NewContinueAsNewError(ctx, PeriodicIngestWorkflow, in)
}
