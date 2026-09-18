package daily

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"git.archive.org/webgroup/scholar/trawler/counts"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

// configureViper sets the queue names and the batch settings the workflow
// reads. Tests set them explicitly so they don't depend on the shared defaults
// or on each other's ordering (viper state is global).
func configureViper(upstream string, concurrency, linesPerCAN int) {
	viper.Set(fmt.Sprintf("%s.external_task_queue", upstream), "ext")
	viper.Set(fmt.Sprintf("%s.internal_task_queue", upstream), "int")
	viper.Set("daily.concurrency", concurrency)
	viper.Set("daily.max_concurrency", 64)
	viper.Set("daily.lines_per_can", linesPerCAN)
	viper.Set("daily.batch_size", 10)
	viper.Set("daily.batches_in_flight", 2)
	viper.Set("daily.batches_per_plan", 8)
	viper.Set("daily.chunk_size", 4096)
	viper.Set("daily.max_consecutive_errors", 50)
	viper.Set("daily.line_attempts", 3)
}

// refs builds n batches of linesEach lines, tiled from the given offset. Ten
// bytes per line keeps the arithmetic in the assertions readable.
func refs(n, linesEach int, from int64) []BatchRef {
	out := make([]BatchRef, 0, n)
	pos := from
	for range n {
		end := pos + int64(linesEach)*10
		out = append(out, BatchRef{Offset: pos, End: end, LineCount: linesEach})
		pos = end
	}
	return out
}

// ingested builds a batch tally with the given number of lines, all of them
// ingested -- a cheap tracer for confirming batch results are summed.
func ingested(lines int) DailyCounts {
	return DailyCounts{
		Counts: counts.Counts{Releases: counts.ReleaseCounts{Ingested: lines}},
		Lines:  lines,
	}
}

// scraped stubs the first-run scrape so a test can get past it.
func scraped(env *testsuite.TestWorkflowEnvironment, key string) {
	env.OnActivity(ScholkitScrapeActivity, mock.Anything, mock.Anything).
		Return(scholkitScrapeOutput{S3Key: key}, nil).Once()
}

// --- splitConcurrency -----------------------------------------------------

// The signal and the CLI flag both speak in releases, because that is what an
// operator watching SPN cares about. Turning that into batches is this
// function's whole job, and getting it wrong means either overrunning SPN or
// leaving worker hosts idle.
func Test_splitConcurrency(t *testing.T) {
	for _, tc := range []struct {
		concurrency, inFlight int
		batches, parallelism  int
	}{
		{concurrency: 8, inFlight: 4, batches: 4, parallelism: 2},
		{concurrency: 4, inFlight: 4, batches: 4, parallelism: 1},
		// Fewer lines than batches: there is nothing to divide, so the batch
		// count comes down instead of the pool going below one.
		{concurrency: 2, inFlight: 4, batches: 2, parallelism: 1},
		{concurrency: 1, inFlight: 4, batches: 1, parallelism: 1},
		// Rounding up rather than down: a run must not quietly work fewer lines
		// at once than it was told to.
		{concurrency: 10, inFlight: 4, batches: 4, parallelism: 3},
		{concurrency: 0, inFlight: 0, batches: 1, parallelism: 1},
	} {
		t.Run(fmt.Sprintf("c=%d/f=%d", tc.concurrency, tc.inFlight), func(t *testing.T) {
			batches, parallelism := splitConcurrency(tc.concurrency, tc.inFlight)
			require.Equal(t, tc.batches, batches)
			require.Equal(t, tc.parallelism, parallelism)
			require.GreaterOrEqual(t, batches*parallelism, max(tc.concurrency, 1),
				"the run would work fewer lines at once than asked")
		})
	}
}

// --- the day's shape ------------------------------------------------------

// On the first run (S3Key empty) the workflow scrapes once, plans the result
// into batches, and returns the summed counts when planning reports EOF.
func Test_Workflow_HappyPathEOF(t *testing.T) {
	configureViper("crossref", 4, 1_000_000)

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	scraped(env, "meta.ndjson")

	env.OnActivity(PlanBatchesActivity, mock.Anything, mock.MatchedBy(func(in PlanBatchesInput) bool {
		return in.S3Key == "meta.ndjson" && in.Offset == 0 &&
			in.BatchSize == 10 && in.MaxBatches == 8 && in.ChunkSize == 4096
	})).Return(PlanBatchesOutput{Batches: refs(3, 10, 0), NextOffset: 300, EOF: true}, nil).Once()

	// Every batch must see the scraped key, the pinned source and the upstream.
	env.OnActivity(CrawlBatchActivity, mock.Anything, mock.MatchedBy(func(in CrawlBatchInput) bool {
		return in.S3Key == "meta.ndjson" && in.Source == "fixed-src" && in.Upstream == "crossref"
	})).Return(CrawlBatchOutput{Counts: ingested(10), DoneThrough: 10}, nil).Times(3)

	env.ExecuteWorkflow(DailyCrawlWorkflow, DailyCrawlWorkflowInput{
		Day:            "2026-05-20",
		Upstream:       "crossref",
		SourceOverride: "fixed-src",
	})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var got DailyCounts
	require.NoError(t, env.GetWorkflowResult(&got))
	require.Equal(t, 30, got.Lines)
	require.Equal(t, 30, got.Releases.Ingested)
	env.AssertExpectations(t)
}

// A continuation run (S3Key already set, as ContinueAsNew would carry it) must
// skip the scrape, resume planning at the carried byte offset, reuse the
// pinned source, and fold new results into the carried Counts.
func Test_Workflow_ResumeSkipsScrapeAndCarriesState(t *testing.T) {
	configureViper("crossref", 4, 1_000_000)

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	env.OnActivity(PlanBatchesActivity, mock.Anything, mock.MatchedBy(func(in PlanBatchesInput) bool {
		return in.S3Key == "carried-key" && in.Offset == 500
	})).Return(PlanBatchesOutput{Batches: refs(2, 10, 500), NextOffset: 700, EOF: true}, nil).Once()

	env.OnActivity(CrawlBatchActivity, mock.Anything, mock.MatchedBy(func(in CrawlBatchInput) bool {
		return in.Source == "pinned-src" && in.S3Key == "carried-key"
	})).Return(CrawlBatchOutput{Counts: ingested(10)}, nil).Twice()

	env.ExecuteWorkflow(DailyCrawlWorkflow, DailyCrawlWorkflowInput{
		Upstream: "crossref",
		S3Key:    "carried-key",
		Source:   "pinned-src",
		Offset:   500,
		Counts:   ingested(100),
	})

	require.True(t, env.IsWorkflowCompleted())
	// Not mocking ScholkitScrapeActivity means a call to it would fail the run;
	// NoError therefore proves the scrape was skipped.
	require.NoError(t, env.GetWorkflowError())

	var got DailyCounts
	require.NoError(t, env.GetWorkflowResult(&got))
	require.Equal(t, 120, got.Lines) // 100 carried + 20 new
	env.AssertExpectations(t)
}

// Batch results are folded in whatever order they land.
func Test_Workflow_FoldsBatchCounts(t *testing.T) {
	configureViper("crossref", 4, 1_000_000)

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	scraped(env, "meta.ndjson")
	env.OnActivity(PlanBatchesActivity, mock.Anything, mock.Anything).
		Return(PlanBatchesOutput{Batches: refs(2, 10, 0), NextOffset: 200, EOF: true}, nil).Once()
	env.OnActivity(CrawlBatchActivity, mock.Anything, mock.Anything).
		Return(CrawlBatchOutput{
			Counts: DailyCounts{
				Counts: counts.Counts{Releases: counts.ReleaseCounts{
					Skipped: 6, CrawlWanted: 4, Acquired: 3, Ingested: 2,
				}},
				Lines:   10,
				Errored: 1,
			},
		}, nil).Twice()

	env.ExecuteWorkflow(DailyCrawlWorkflow, DailyCrawlWorkflowInput{
		Upstream: "crossref", SourceOverride: "src",
	})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var got DailyCounts
	require.NoError(t, env.GetWorkflowResult(&got))
	require.Equal(t, 20, got.Lines)
	require.Equal(t, 2, got.Errored)
	require.Equal(t, 12, got.Releases.Skipped)
	require.Equal(t, 8, got.Releases.CrawlWanted)
	require.Equal(t, 6, got.Releases.Acquired)
	require.Equal(t, 4, got.Releases.Ingested)
}

// A batch reporting its circuit breaker tripped stops the run, and says where
// to resume from so the day does not have to be restarted from the top.
func Test_Workflow_AbortedBatchStopsTheRun(t *testing.T) {
	configureViper("crossref", 1, 1_000_000)

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	scraped(env, "meta.ndjson")
	env.OnActivity(PlanBatchesActivity, mock.Anything, mock.Anything).
		Return(PlanBatchesOutput{Batches: refs(4, 10, 0), NextOffset: 400, EOF: true}, nil).Once()
	env.OnActivity(CrawlBatchActivity, mock.Anything, mock.Anything).
		Return(CrawlBatchOutput{
			Counts:      DailyCounts{Lines: 3, Errored: 3},
			DoneThrough: 3,
			NextOffset:  30,
			Aborted:     true,
			AbortReason: "3 consecutive line failures",
		}, nil)

	env.ExecuteWorkflow(DailyCrawlWorkflow, DailyCrawlWorkflowInput{
		Upstream: "crossref", SourceOverride: "src",
	})

	require.True(t, env.IsWorkflowCompleted())
	err := env.GetWorkflowError()
	require.ErrorContains(t, err, "consecutive line failures")
	require.ErrorContains(t, err, "resume at offset 30")
}

// A batch activity only fails for something systemic, so unlike a single bad
// record it is not something the run absorbs and carries on from.
func Test_Workflow_FailedBatchStopsTheRun(t *testing.T) {
	configureViper("crossref", 1, 1_000_000)

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	scraped(env, "meta.ndjson")
	env.OnActivity(PlanBatchesActivity, mock.Anything, mock.Anything).
		Return(PlanBatchesOutput{Batches: refs(2, 10, 0), NextOffset: 200, EOF: true}, nil).Once()
	env.OnActivity(CrawlBatchActivity, mock.Anything, mock.Anything).
		Return(CrawlBatchOutput{}, errors.New("read-only file system"))

	env.ExecuteWorkflow(DailyCrawlWorkflow, DailyCrawlWorkflowInput{
		Upstream: "crossref", SourceOverride: "src",
	})

	require.True(t, env.IsWorkflowCompleted())
	require.ErrorContains(t, env.GetWorkflowError(), "read-only file system")
}

// A planning call that yields nothing and does not advance would spin forever.
func Test_Workflow_StopsIfPlanningMakesNoProgress(t *testing.T) {
	configureViper("crossref", 1, 1_000_000)

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	scraped(env, "meta.ndjson")
	env.OnActivity(PlanBatchesActivity, mock.Anything, mock.Anything).
		Return(PlanBatchesOutput{NextOffset: 0, EOF: false}, nil)

	env.ExecuteWorkflow(DailyCrawlWorkflow, DailyCrawlWorkflowInput{
		Upstream: "crossref", SourceOverride: "src",
	})

	require.True(t, env.IsWorkflowCompleted())
	require.ErrorContains(t, env.GetWorkflowError(), "made no progress")
}

// --- continue-as-new ------------------------------------------------------

// Hitting lines_per_can hands off to a fresh run rather than growing one run's
// history without bound.
func Test_Workflow_ContinuesAsNewAtThreshold(t *testing.T) {
	configureViper("crossref", 1, 30)

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	scraped(env, "meta.ndjson")
	env.OnActivity(PlanBatchesActivity, mock.Anything, mock.Anything).
		Return(PlanBatchesOutput{Batches: refs(5, 10, 0), NextOffset: 500, EOF: false}, nil).Once()
	// Only three of the five batches are dispatched before the handoff.
	env.OnActivity(CrawlBatchActivity, mock.Anything, mock.Anything).
		Return(CrawlBatchOutput{Counts: ingested(10)}, nil).Times(3)

	env.ExecuteWorkflow(DailyCrawlWorkflow, DailyCrawlWorkflowInput{
		Upstream: "crossref", SourceOverride: "src",
	})

	require.True(t, env.IsWorkflowCompleted())
	var canErr *workflow.ContinueAsNewError
	require.True(t, errors.As(env.GetWorkflowError(), &canErr),
		"expected ContinueAsNewError, got %v", env.GetWorkflowError())
	env.AssertExpectations(t)
}

// The handoff carries the scraped key, the pinned source and the offset of the
// last batch actually dispatched -- so the next run neither re-scrapes,
// re-labels, nor re-works anything.
func Test_Workflow_HandoffCarriesScrapeAndOffset(t *testing.T) {
	configureViper("crossref", 1, 30)

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	scraped(env, "meta.ndjson")
	env.OnActivity(PlanBatchesActivity, mock.Anything, mock.Anything).
		Return(PlanBatchesOutput{Batches: refs(5, 10, 0), NextOffset: 500, EOF: false}, nil).Once()
	env.OnActivity(CrawlBatchActivity, mock.Anything, mock.Anything).
		Return(CrawlBatchOutput{Counts: ingested(10)}, nil).Times(3)

	env.ExecuteWorkflow(DailyCrawlWorkflow, DailyCrawlWorkflowInput{
		Upstream: "crossref", Day: "2026-05-20",
	})

	var canErr *workflow.ContinueAsNewError
	require.True(t, errors.As(env.GetWorkflowError(), &canErr))

	var next DailyCrawlWorkflowInput
	require.NoError(t, converter.GetDefaultDataConverter().FromPayloads(canErr.Input, &next))
	// Three batches of ten lines at ten bytes each -- not the plan's NextOffset
	// of 500.
	require.Equal(t, int64(300), next.Offset)
	require.Equal(t, 30, next.Counts.Lines)
	require.Equal(t, "meta.ndjson", next.S3Key)
	require.True(t, strings.HasPrefix(next.Source, "daily-20260520-"))
	require.Equal(t, "2026-05-20", next.Day)
}

// --- config ---------------------------------------------------------------

// Config is read from viper once on the first run and threaded into the
// activities, rather than the workflow reading viper inline on every run.
// This is what keeps the values fixed and replay-deterministic for the whole
// ContinueAsNew chain.
func Test_Workflow_CapturesConfigFromViper(t *testing.T) {
	configureViper("crossref", 4, 1_000_000)
	viper.Set("daily.batch_size", 250)
	viper.Set("daily.chunk_size", 8192)
	viper.Set("daily.line_attempts", 7)
	t.Cleanup(func() { configureViper("crossref", 4, 1_000_000) })

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	scraped(env, "meta.ndjson")
	env.OnActivity(PlanBatchesActivity, mock.Anything, mock.MatchedBy(func(in PlanBatchesInput) bool {
		return in.BatchSize == 250 && in.ChunkSize == 8192
	})).Return(PlanBatchesOutput{Batches: refs(1, 10, 0), NextOffset: 100, EOF: true}, nil).Once()
	env.OnActivity(CrawlBatchActivity, mock.Anything, mock.MatchedBy(func(in CrawlBatchInput) bool {
		return in.ChunkSize == 8192 && in.LineAttempts == 7 && in.MaxConsecutiveErrors == 50
	})).Return(CrawlBatchOutput{Counts: ingested(10)}, nil).Once()

	env.ExecuteWorkflow(DailyCrawlWorkflow, DailyCrawlWorkflowInput{
		Upstream: "crossref", SourceOverride: "src",
	})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	env.AssertExpectations(t)
}

// A resumed run reuses the carried config and offset instead of re-reading
// viper, which is what keeps a ContinueAsNew chain replay-deterministic.
func Test_Workflow_ResumeUsesCarriedConfig(t *testing.T) {
	configureViper("crossref", 4, 1_000_000)

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	env.OnActivity(PlanBatchesActivity, mock.Anything, mock.MatchedBy(func(in PlanBatchesInput) bool {
		return in.Offset == 500 && in.BatchSize == 7 && in.MaxBatches == 3 && in.ChunkSize == 99
	})).Return(PlanBatchesOutput{Batches: refs(1, 7, 500), NextOffset: 570, EOF: true}, nil).Once()

	env.OnActivity(CrawlBatchActivity, mock.Anything, mock.MatchedBy(func(in CrawlBatchInput) bool {
		return in.ChunkSize == 99 && in.LineAttempts == 9 && in.MaxConsecutiveErrors == 5
	})).Return(CrawlBatchOutput{Counts: ingested(7)}, nil).Once()

	env.ExecuteWorkflow(DailyCrawlWorkflow, DailyCrawlWorkflowInput{
		Upstream:             "crossref",
		S3Key:                "carried-key",
		Source:               "pinned-src",
		Offset:               500,
		Concurrency:          1,
		MaxConcurrency:       8,
		LinesPerCAN:          100,
		BatchSize:            7,
		BatchesInFlight:      1,
		BatchesPerPlan:       3,
		ChunkSize:            99,
		MaxConsecutiveErrors: 5,
		LineAttempts:         9,
		InternalTaskQueue:    "int",
		ExternalTaskQueue:    "ext",
		Counts:               ingested(42),
	})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var got DailyCounts
	require.NoError(t, env.GetWorkflowResult(&got))
	require.Equal(t, 49, got.Lines) // 42 carried + 7 new
	env.AssertExpectations(t)
}

// A run handed on by a worker from before this workflow used batches carries a
// BatchSize that meant something else -- lines per S3 read -- and none of the
// batch settings. It has to notice that and re-read config, or it would plan
// with a zero batch count and fail the moment it tried.
func Test_Workflow_ResumeFromPreBatchInputRereadsConfig(t *testing.T) {
	configureViper("crossref", 4, 1_000_000)

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	env.OnActivity(PlanBatchesActivity, mock.Anything, mock.MatchedBy(func(in PlanBatchesInput) bool {
		// The configured values, not the carried ones.
		return in.Offset == 4096 && in.BatchSize == 10 && in.MaxBatches == 8 && in.ChunkSize == 4096
	})).Return(PlanBatchesOutput{Batches: refs(1, 10, 4096), NextOffset: 4196, EOF: true}, nil).Once()
	env.OnActivity(CrawlBatchActivity, mock.Anything, mock.Anything).
		Return(CrawlBatchOutput{Counts: ingested(10)}, nil).Once()

	env.ExecuteWorkflow(DailyCrawlWorkflow, DailyCrawlWorkflowInput{
		Upstream: "crossref",
		S3Key:    "carried-key",
		Source:   "pinned-src",
		// Everything a pre-batch run would have been carrying, and nothing else.
		Offset:            4096,
		LinesPerCAN:       2000,
		BatchSize:         10000,
		ChunkSize:         102400,
		InternalTaskQueue: "int",
		ExternalTaskQueue: "ext",
		Counts: DailyCounts{
			Counts: counts.Counts{Releases: counts.ReleaseCounts{Ingested: 900}},
		},
	})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	// The byte offset and the running totals survive the handoff -- that is the
	// whole point of letting it resume rather than making it start over.
	var got DailyCounts
	require.NoError(t, env.GetWorkflowResult(&got))
	require.Equal(t, 910, got.Releases.Ingested)
	require.Equal(t, 10, got.Lines)
	env.AssertExpectations(t)
}

// --- concurrency ----------------------------------------------------------

// Concurrency is clamped so neither a start flag nor a retune signal can point
// an unbounded number of crawls at SPN.
func Test_Workflow_ClampsConcurrencyToMax(t *testing.T) {
	configureViper("crossref", 1, 1_000_000)

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	scraped(env, "meta.ndjson")
	env.OnActivity(PlanBatchesActivity, mock.Anything, mock.Anything).
		Return(PlanBatchesOutput{Batches: refs(1, 10, 0), NextOffset: 100, EOF: true}, nil).Once()
	env.OnActivity(CrawlBatchActivity, mock.Anything, mock.MatchedBy(func(in CrawlBatchInput) bool {
		// 64 lines over 2 batches in flight, not 10_000 over 2.
		return in.Parallelism == 32
	})).Return(CrawlBatchOutput{Counts: ingested(10)}, nil).Once()

	env.ExecuteWorkflow(DailyCrawlWorkflow, DailyCrawlWorkflowInput{
		Upstream: "crossref", SourceOverride: "src", Concurrency: 10_000,
	})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	env.AssertExpectations(t)
}

// The run's concurrency reaches the batch as its pool size, which is the only
// way it reaches SPN and GROBID at all now.
func Test_Workflow_ConcurrencyReachesTheBatch(t *testing.T) {
	configureViper("crossref", 6, 1_000_000)

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	scraped(env, "meta.ndjson")
	env.OnActivity(PlanBatchesActivity, mock.Anything, mock.Anything).
		Return(PlanBatchesOutput{Batches: refs(2, 10, 0), NextOffset: 200, EOF: true}, nil).Once()
	// batches_in_flight is 2, so six lines at once means three per batch.
	env.OnActivity(CrawlBatchActivity, mock.Anything, mock.MatchedBy(func(in CrawlBatchInput) bool {
		return in.Parallelism == 3
	})).Return(CrawlBatchOutput{Counts: ingested(10)}, nil).Twice()

	env.ExecuteWorkflow(DailyCrawlWorkflow, DailyCrawlWorkflowInput{
		Upstream: "crossref", SourceOverride: "src",
	})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	env.AssertExpectations(t)
}

// The window must actually bound batches in flight -- that is what keeps a run
// from handing every batch of a day to the workers at once. Mocked returns
// alone would pass whether the window worked or not, so this drives a real
// activity function that records how many ran at once.
func runWithBatchesInFlight(t *testing.T, inFlight, batches int) int32 {
	t.Helper()
	configureViper("crossref", inFlight, 1_000_000)
	viper.Set("daily.batches_in_flight", inFlight)
	t.Cleanup(func() { configureViper("crossref", 4, 1_000_000) })

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	var cur, peak atomic.Int32
	env.OnActivity(CrawlBatchActivity, mock.Anything, mock.Anything).
		Return(func(context.Context, CrawlBatchInput) (CrawlBatchOutput, error) {
			n := cur.Add(1)
			for {
				m := peak.Load()
				if n <= m || peak.CompareAndSwap(m, n) {
					break
				}
			}
			time.Sleep(50 * time.Millisecond)
			cur.Add(-1)
			return CrawlBatchOutput{Counts: ingested(10)}, nil
		})

	scraped(env, "meta.ndjson")
	env.OnActivity(PlanBatchesActivity, mock.Anything, mock.Anything).
		Return(PlanBatchesOutput{
			Batches:    refs(batches, 10, 0),
			NextOffset: int64(batches) * 100,
			EOF:        true,
		}, nil).Once()

	env.ExecuteWorkflow(DailyCrawlWorkflow, DailyCrawlWorkflowInput{
		Upstream: "crossref", SourceOverride: "src",
	})
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var got DailyCounts
	require.NoError(t, env.GetWorkflowResult(&got))
	require.Equal(t, batches*10, got.Lines, "every batch should still be worked")
	return peak.Load()
}

func Test_Workflow_BatchesInFlightIsBounded(t *testing.T) {
	peak := runWithBatchesInFlight(t, 3, 9)
	require.LessOrEqual(t, peak, int32(3), "the window let more than 3 batches run at once")
	require.Greater(t, peak, int32(1), "batches ran serially; the window is not dispatching")
}

func Test_Workflow_OneBatchInFlightIsSerial(t *testing.T) {
	require.Equal(t, int32(1), runWithBatchesInFlight(t, 1, 4))
}

// A retune signal is applied mid-run, and an out-of-range one is ignored
// rather than either clamping silently to something surprising or wedging the
// run.
func Test_Workflow_RetuneSignal(t *testing.T) {
	for _, tc := range []struct {
		name   string
		signal int
	}{
		{"in range", 2},
		{"above max", 10_000},
		{"zero", 0},
		{"negative", -5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configureViper("crossref", 1, 1_000_000)

			var ts testsuite.WorkflowTestSuite
			env := ts.NewTestWorkflowEnvironment()

			scraped(env, "meta.ndjson")
			env.OnActivity(PlanBatchesActivity, mock.Anything, mock.Anything).
				Return(PlanBatchesOutput{Batches: refs(4, 10, 0), NextOffset: 400, EOF: true}, nil).Once()
			env.OnActivity(CrawlBatchActivity, mock.Anything, mock.Anything).
				Return(CrawlBatchOutput{Counts: ingested(10)}, nil).Times(4)

			env.RegisterDelayedCallback(func() {
				env.SignalWorkflow(SetConcurrencySignal, tc.signal)
			}, 0)

			env.ExecuteWorkflow(DailyCrawlWorkflow, DailyCrawlWorkflowInput{
				Upstream: "crossref", SourceOverride: "src",
			})

			require.True(t, env.IsWorkflowCompleted())
			require.NoError(t, env.GetWorkflowError())

			var got DailyCounts
			require.NoError(t, env.GetWorkflowResult(&got))
			require.Equal(t, 40, got.Lines, "every line is worked regardless of the signal")
		})
	}
}

// --- the scrape and the day label -----------------------------------------

// A scrape failure on the first run aborts the workflow before any line
// processing. The error is returned as non-retryable so the test env doesn't
// loop on the default activity retry policy.
func Test_Workflow_ScrapeErrorAborts(t *testing.T) {
	configureViper("crossref", 4, 1_000_000)

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	env.OnActivity(ScholkitScrapeActivity, mock.Anything, mock.Anything).
		Return(scholkitScrapeOutput{},
			temporal.NewNonRetryableApplicationError("boom", "ScrapeError", nil)).Once()

	env.ExecuteWorkflow(DailyCrawlWorkflow, DailyCrawlWorkflowInput{
		Upstream: "crossref", SourceOverride: "src",
	})

	require.True(t, env.IsWorkflowCompleted())
	require.Error(t, env.GetWorkflowError())
	// Planning and batching were never mocked; reaching them would fail the run
	// differently, so this also confirms processing never started.
	env.AssertExpectations(t)
}

// A scrape that reports success but hands back no key is a broken run, not an
// empty day: planning an empty key would fail somewhere far less legible.
func Test_Workflow_EmptyScrapeKeyFails(t *testing.T) {
	configureViper("crossref", 4, 1_000_000)

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	scraped(env, "")

	env.ExecuteWorkflow(DailyCrawlWorkflow, DailyCrawlWorkflowInput{
		Upstream: "crossref", SourceOverride: "src",
	})

	require.True(t, env.IsWorkflowCompleted())
	require.ErrorContains(t, env.GetWorkflowError(), "empty s3 key")
}

// An empty Day is resolved from the workflow clock, and the resolved day has to
// reach both the scrape activity and the source label: deriving one without the
// other yields records labelled for a day whose data was never fetched.
//
// DOAJ is where that divergence bites. Their API serves non-paying callers only
// records older than a month, so "no day given" means a month and a day back,
// not yesterday -- and a label-only offset would leave us scraping a window
// DOAJ won't answer.
func Test_Workflow_ResolvesDOAJDayBackAMonth(t *testing.T) {
	configureViper("doaj", 4, 1_000_000)

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()
	env.SetStartTime(time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC))

	// 32 days before the start time, not 1.
	env.OnActivity(ScholkitScrapeActivity, mock.Anything, mock.MatchedBy(func(in scholkitScrapeInput) bool {
		return in.Day == "2026-04-18" && in.Upstream == "doaj"
	})).Return(scholkitScrapeOutput{S3Key: "meta.ndjson"}, nil).Once()

	env.OnActivity(PlanBatchesActivity, mock.Anything, mock.Anything).
		Return(PlanBatchesOutput{Batches: refs(1, 10, 0), NextOffset: 100, EOF: true}, nil).Once()

	// The generated label names the same day that was scraped. Its RunID segment
	// is dynamic, so match around it.
	env.OnActivity(CrawlBatchActivity, mock.Anything, mock.MatchedBy(func(in CrawlBatchInput) bool {
		return strings.HasPrefix(in.Source, "daily-20260418-") && strings.HasSuffix(in.Source, "-doaj")
	})).Return(CrawlBatchOutput{Counts: ingested(10)}, nil).Once()

	env.ExecuteWorkflow(DailyCrawlWorkflow, DailyCrawlWorkflowInput{Upstream: "doaj"})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	env.AssertExpectations(t)
}

// Every other upstream takes the ordinary path: an empty Day is yesterday
// relative to the workflow clock. This is the counterpart to the DOAJ case --
// together they pin the offset as upstream-dependent rather than global.
func Test_Workflow_ResolvesDayToYesterday(t *testing.T) {
	configureViper("crossref", 4, 1_000_000)

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()
	env.SetStartTime(time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC))

	env.OnActivity(ScholkitScrapeActivity, mock.Anything, mock.MatchedBy(func(in scholkitScrapeInput) bool {
		return in.Day == "2026-05-19" && in.Upstream == "crossref"
	})).Return(scholkitScrapeOutput{S3Key: "meta.ndjson"}, nil).Once()

	env.OnActivity(PlanBatchesActivity, mock.Anything, mock.Anything).
		Return(PlanBatchesOutput{Batches: refs(1, 10, 0), NextOffset: 100, EOF: true}, nil).Once()

	env.OnActivity(CrawlBatchActivity, mock.Anything, mock.MatchedBy(func(in CrawlBatchInput) bool {
		return strings.HasPrefix(in.Source, "daily-20260519-") && strings.HasSuffix(in.Source, "-crossref")
	})).Return(CrawlBatchOutput{Counts: ingested(10)}, nil).Once()

	env.ExecuteWorkflow(DailyCrawlWorkflow, DailyCrawlWorkflowInput{Upstream: "crossref"})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	env.AssertExpectations(t)
}

// An explicit Day is used verbatim and the clock is never consulted -- the
// start time here is months away from the requested day, so any fallback to
// "yesterday" would show up in both the scrape input and the label.
func Test_Workflow_ExplicitDayOverridesClock(t *testing.T) {
	configureViper("crossref", 4, 1_000_000)

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()
	env.SetStartTime(time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC))

	env.OnActivity(ScholkitScrapeActivity, mock.Anything, mock.MatchedBy(func(in scholkitScrapeInput) bool {
		return in.Day == "2026-03-05"
	})).Return(scholkitScrapeOutput{S3Key: "meta.ndjson"}, nil).Once()

	env.OnActivity(PlanBatchesActivity, mock.Anything, mock.Anything).
		Return(PlanBatchesOutput{Batches: refs(1, 10, 0), NextOffset: 100, EOF: true}, nil).Once()

	env.OnActivity(CrawlBatchActivity, mock.Anything, mock.MatchedBy(func(in CrawlBatchInput) bool {
		return strings.HasPrefix(in.Source, "daily-20260305-")
	})).Return(CrawlBatchOutput{Counts: ingested(10)}, nil).Once()

	env.ExecuteWorkflow(DailyCrawlWorkflow, DailyCrawlWorkflowInput{
		Day: "2026-03-05", Upstream: "crossref",
	})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	env.AssertExpectations(t)
}

// A Day that isn't in the documented 2006-01-02 format fails the run outright
// rather than being silently coerced or reaching the scrape activity, which
// parses it again with the same layout.
func Test_Workflow_MalformedDayFails(t *testing.T) {
	configureViper("crossref", 4, 1_000_000)

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	env.ExecuteWorkflow(DailyCrawlWorkflow, DailyCrawlWorkflowInput{
		Day: "20260305", Upstream: "crossref",
	})

	require.True(t, env.IsWorkflowCompleted())
	require.Error(t, env.GetWorkflowError())
	// Nothing was mocked, so reaching any activity would fail differently.
	env.AssertExpectations(t)
}

// An upstream is the one thing the workflow cannot do without: it picks the
// task queues, the transform and the scrape target.
func Test_Workflow_RequiresUpstream(t *testing.T) {
	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()
	env.ExecuteWorkflow(DailyCrawlWorkflow, DailyCrawlWorkflowInput{})
	require.True(t, env.IsWorkflowCompleted())
	require.ErrorContains(t, env.GetWorkflowError(), "Upstream is required")
}

func Test_WorkflowID(t *testing.T) {
	require.Equal(t, "crossref_daily_20260520", WorkflowID("crossref", "20260520"))
}
