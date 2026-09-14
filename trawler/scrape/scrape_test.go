package scrape

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

func configureViper(concurrency, urlsPerCAN int) {
	viper.Set("scrape.task_queue", "scrape_test")
	viper.Set("scrape.concurrency", concurrency)
	viper.Set("scrape.max_concurrency", 64)
	viper.Set("scrape.urls_per_can", urlsPerCAN)
	viper.Set("scrape.batch_size", 10)
	viper.Set("scrape.batches_in_flight", 2)
	viper.Set("scrape.batches_per_plan", 8)
	viper.Set("scrape.chunk_size", 4096)
	viper.Set("scrape.max_consecutive_errors", 50)
	viper.Set("scrape.url_attempts", 4)
}

// refs builds n batches of urlsEach URLs, tiled from the given offset. Ten
// bytes per URL keeps the arithmetic in the assertions readable.
func refs(n, urlsEach int, from int64) []BatchRef {
	out := make([]BatchRef, 0, n)
	pos := from
	for range n {
		end := pos + int64(urlsEach)*10
		out = append(out, BatchRef{Offset: pos, End: end, URLCount: urlsEach})
		pos = end
	}
	return out
}

// --- splitConcurrency -----------------------------------------------------

// The signal and the CLI flag both speak in URLs, because that is what an
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
		// Fewer URLs than batches: there is nothing to divide, so the batch
		// count comes down instead of the pool going below one.
		{concurrency: 2, inFlight: 4, batches: 2, parallelism: 1},
		{concurrency: 1, inFlight: 4, batches: 1, parallelism: 1},
		// Rounding up rather than down: a run must not quietly crawl fewer URLs
		// at once than it was told to.
		{concurrency: 10, inFlight: 4, batches: 4, parallelism: 3},
		{concurrency: 0, inFlight: 0, batches: 1, parallelism: 1},
	} {
		t.Run(fmt.Sprintf("c=%d/f=%d", tc.concurrency, tc.inFlight), func(t *testing.T) {
			batches, parallelism := splitConcurrency(tc.concurrency, tc.inFlight)
			require.Equal(t, tc.batches, batches)
			require.Equal(t, tc.parallelism, parallelism)
			require.GreaterOrEqual(t, batches*parallelism, max(tc.concurrency, 1),
				"the run would crawl fewer urls at once than asked")
		})
	}
}

// --- workflow -------------------------------------------------------------

func Test_Workflow_HappyPathEOF(t *testing.T) {
	configureViper(4, 1_000_000)

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	env.OnActivity(PlanBatchesActivity, mock.Anything, mock.MatchedBy(func(in PlanBatchesInput) bool {
		return in.S3Key == "bucket/urls.txt" && in.Offset == 0 &&
			in.BatchSize == 10 && in.MaxBatches == 8
	})).Return(PlanBatchesOutput{Batches: refs(3, 10, 0), NextOffset: 300, EOF: true}, nil).Once()

	env.OnActivity(CrawlBatchActivity, mock.Anything, mock.Anything).
		Return(CrawlBatchOutput{
			Counts:      ScrapeCounts{URLs: 10, PDFFound: 7, NoPDF: 3},
			DoneThrough: 10,
		}, nil).Times(3)

	env.ExecuteWorkflow(ScrapeToWaybackWorkflow, ScrapeToWaybackInput{S3Key: "bucket/urls.txt"})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var got ScrapeCounts
	require.NoError(t, env.GetWorkflowResult(&got))
	require.Equal(t, 30, got.URLs)
	require.Equal(t, 21, got.PDFFound)
	require.Equal(t, 9, got.NoPDF)
	env.AssertExpectations(t)
}

// Batch results are folded in whatever order they land, including their fail
// reason breakdowns.
func Test_Workflow_FoldsBatchCounts(t *testing.T) {
	configureViper(4, 1_000_000)

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	env.OnActivity(PlanBatchesActivity, mock.Anything, mock.Anything).
		Return(PlanBatchesOutput{Batches: refs(2, 10, 0), NextOffset: 200, EOF: true}, nil).Once()

	env.OnActivity(CrawlBatchActivity, mock.Anything, mock.Anything).
		Return(CrawlBatchOutput{
			Counts: ScrapeCounts{
				URLs: 10, NoPDF: 9, Skipped: 1,
				FailReasons: map[string]int{"dead-end": 9, "blocklist": 1},
			},
		}, nil).Twice()

	env.ExecuteWorkflow(ScrapeToWaybackWorkflow, ScrapeToWaybackInput{S3Key: "bucket/urls.txt"})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var got ScrapeCounts
	require.NoError(t, env.GetWorkflowResult(&got))
	require.Equal(t, 20, got.URLs)
	require.Equal(t, 18, got.NoPDF)
	require.Equal(t, 2, got.Skipped)
	require.Equal(t, map[string]int{"dead-end": 18, "blocklist": 2}, got.FailReasons)
}

// A batch reporting its circuit breaker tripped stops the run, and says where
// to resume from so the list does not have to be restarted from the top.
func Test_Workflow_AbortedBatchStopsTheRun(t *testing.T) {
	configureViper(1, 1_000_000)

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	env.OnActivity(PlanBatchesActivity, mock.Anything, mock.Anything).
		Return(PlanBatchesOutput{Batches: refs(4, 10, 0), NextOffset: 400, EOF: true}, nil).Once()
	env.OnActivity(CrawlBatchActivity, mock.Anything, mock.Anything).
		Return(CrawlBatchOutput{
			Counts:      ScrapeCounts{URLs: 3, Errored: 3},
			DoneThrough: 3,
			NextOffset:  30,
			Aborted:     true,
			AbortReason: "3 consecutive crawl failures",
		}, nil)

	env.ExecuteWorkflow(ScrapeToWaybackWorkflow, ScrapeToWaybackInput{S3Key: "bucket/urls.txt"})

	require.True(t, env.IsWorkflowCompleted())
	err := env.GetWorkflowError()
	require.ErrorContains(t, err, "consecutive crawl failures")
	require.ErrorContains(t, err, "resume at offset 30")
}

// A batch activity only fails for something systemic, so unlike a single dead
// URL it is not something the run absorbs and carries on from.
func Test_Workflow_FailedBatchStopsTheRun(t *testing.T) {
	configureViper(1, 1_000_000)

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	env.OnActivity(PlanBatchesActivity, mock.Anything, mock.Anything).
		Return(PlanBatchesOutput{Batches: refs(2, 10, 0), NextOffset: 200, EOF: true}, nil).Once()
	env.OnActivity(CrawlBatchActivity, mock.Anything, mock.Anything).
		Return(CrawlBatchOutput{}, errors.New("read-only file system"))

	env.ExecuteWorkflow(ScrapeToWaybackWorkflow, ScrapeToWaybackInput{S3Key: "bucket/urls.txt"})

	require.True(t, env.IsWorkflowCompleted())
	require.ErrorContains(t, env.GetWorkflowError(), "read-only file system")
}

// A planning call that yields nothing and does not advance would spin forever.
func Test_Workflow_StopsIfPlanningMakesNoProgress(t *testing.T) {
	configureViper(1, 1_000_000)

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	env.OnActivity(PlanBatchesActivity, mock.Anything, mock.Anything).
		Return(PlanBatchesOutput{NextOffset: 0, EOF: false}, nil)

	env.ExecuteWorkflow(ScrapeToWaybackWorkflow, ScrapeToWaybackInput{S3Key: "bucket/urls.txt"})

	require.True(t, env.IsWorkflowCompleted())
	require.ErrorContains(t, env.GetWorkflowError(), "made no progress")
}

// Hitting urls_per_can hands off to a fresh run rather than growing one run's
// history without bound.
func Test_Workflow_ContinuesAsNewAtThreshold(t *testing.T) {
	configureViper(1, 30)

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	env.OnActivity(PlanBatchesActivity, mock.Anything, mock.Anything).
		Return(PlanBatchesOutput{Batches: refs(5, 10, 0), NextOffset: 500, EOF: false}, nil).Once()
	// Only three of the five batches are dispatched before the handoff.
	env.OnActivity(CrawlBatchActivity, mock.Anything, mock.Anything).
		Return(CrawlBatchOutput{Counts: ScrapeCounts{URLs: 10, PDFFound: 10}}, nil).Times(3)

	env.ExecuteWorkflow(ScrapeToWaybackWorkflow, ScrapeToWaybackInput{S3Key: "bucket/urls.txt"})

	require.True(t, env.IsWorkflowCompleted())
	var canErr *workflow.ContinueAsNewError
	require.True(t, errors.As(env.GetWorkflowError(), &canErr),
		"expected ContinueAsNewError, got %v", env.GetWorkflowError())
	env.AssertExpectations(t)
}

// The resume offset is the end of the last batch actually dispatched, so the
// next run neither re-crawls nor skips. It only ever sits on a batch boundary.
func Test_Workflow_ResumeOffsetIsLastDispatchedBatch(t *testing.T) {
	configureViper(1, 30)

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	env.OnActivity(PlanBatchesActivity, mock.Anything, mock.Anything).
		Return(PlanBatchesOutput{Batches: refs(5, 10, 0), NextOffset: 500, EOF: false}, nil).Once()
	env.OnActivity(CrawlBatchActivity, mock.Anything, mock.Anything).
		Return(CrawlBatchOutput{Counts: ScrapeCounts{URLs: 10, PDFFound: 10}}, nil).Times(3)

	env.ExecuteWorkflow(ScrapeToWaybackWorkflow, ScrapeToWaybackInput{S3Key: "bucket/urls.txt"})

	var canErr *workflow.ContinueAsNewError
	require.True(t, errors.As(env.GetWorkflowError(), &canErr))

	var next ScrapeToWaybackInput
	require.NoError(t, converter.GetDefaultDataConverter().FromPayloads(canErr.Input, &next))
	// Three batches of ten URLs at ten bytes each -- not the plan's NextOffset
	// of 500.
	require.Equal(t, int64(300), next.Offset)
	require.Equal(t, 30, next.Counts.URLs)
}

// A resumed run reuses the carried config and offset instead of re-reading
// viper, which is what keeps a ContinueAsNew chain replay-deterministic.
func Test_Workflow_ResumeUsesCarriedConfig(t *testing.T) {
	configureViper(1, 1_000_000)

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	env.OnActivity(PlanBatchesActivity, mock.Anything, mock.MatchedBy(func(in PlanBatchesInput) bool {
		return in.Offset == 500 && in.BatchSize == 7 && in.MaxBatches == 3 && in.ChunkSize == 99
	})).Return(PlanBatchesOutput{Batches: refs(1, 7, 500), NextOffset: 570, EOF: true}, nil).Once()

	env.OnActivity(CrawlBatchActivity, mock.Anything, mock.MatchedBy(func(in CrawlBatchInput) bool {
		return in.ChunkSize == 99 && in.URLAttempts == 9 && in.MaxConsecutiveErrors == 5
	})).Return(CrawlBatchOutput{Counts: ScrapeCounts{URLs: 7, PDFFound: 7}}, nil).Once()

	env.ExecuteWorkflow(ScrapeToWaybackWorkflow, ScrapeToWaybackInput{
		S3Key:                "bucket/urls.txt",
		Offset:               500,
		Concurrency:          1,
		MaxConcurrency:       8,
		URLsPerCAN:           100,
		BatchSize:            7,
		BatchesInFlight:      1,
		BatchesPerPlan:       3,
		ChunkSize:            99,
		MaxConsecutiveErrors: 5,
		URLAttempts:          9,
		Counts:               ScrapeCounts{URLs: 42, PDFFound: 40},
	})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var got ScrapeCounts
	require.NoError(t, env.GetWorkflowResult(&got))
	require.Equal(t, 49, got.URLs) // 42 carried + 7 new
	require.Equal(t, 47, got.PDFFound)
	env.AssertExpectations(t)
}

func Test_Workflow_RequiresS3Key(t *testing.T) {
	configureViper(1, 10)
	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()
	env.ExecuteWorkflow(ScrapeToWaybackWorkflow, ScrapeToWaybackInput{})
	require.True(t, env.IsWorkflowCompleted())
	require.ErrorContains(t, env.GetWorkflowError(), "S3Key is required")
}

// Concurrency is clamped so neither a start flag nor a retune signal can point
// an unbounded number of crawls at SPN.
func Test_Workflow_ClampsConcurrencyToMax(t *testing.T) {
	configureViper(1, 1_000_000)

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	env.OnActivity(PlanBatchesActivity, mock.Anything, mock.Anything).
		Return(PlanBatchesOutput{Batches: refs(1, 10, 0), NextOffset: 100, EOF: true}, nil).Once()
	env.OnActivity(CrawlBatchActivity, mock.Anything, mock.MatchedBy(func(in CrawlBatchInput) bool {
		// 64 URLs over 2 batches in flight, not 10_000 over 2.
		return in.Parallelism == 32
	})).Return(CrawlBatchOutput{Counts: ScrapeCounts{URLs: 10}}, nil).Once()

	env.ExecuteWorkflow(ScrapeToWaybackWorkflow, ScrapeToWaybackInput{
		S3Key:       "bucket/urls.txt",
		Concurrency: 10_000,
	})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	env.AssertExpectations(t)
}

// The run's concurrency reaches the batch as its pool size, which is the only
// way it reaches SPN at all now.
func Test_Workflow_ConcurrencyReachesTheBatch(t *testing.T) {
	configureViper(6, 1_000_000)

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	env.OnActivity(PlanBatchesActivity, mock.Anything, mock.Anything).
		Return(PlanBatchesOutput{Batches: refs(2, 10, 0), NextOffset: 200, EOF: true}, nil).Once()
	// batches_in_flight is 2, so six URLs at once means three per batch.
	env.OnActivity(CrawlBatchActivity, mock.Anything, mock.MatchedBy(func(in CrawlBatchInput) bool {
		return in.Parallelism == 3
	})).Return(CrawlBatchOutput{Counts: ScrapeCounts{URLs: 10}}, nil).Twice()

	env.ExecuteWorkflow(ScrapeToWaybackWorkflow, ScrapeToWaybackInput{S3Key: "bucket/urls.txt"})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	env.AssertExpectations(t)
}

// The window must actually bound batches in flight -- that is what keeps a run
// from handing every batch of a huge list to the workers at once. Mocked
// returns alone would pass whether the window worked or not, so this drives a
// real activity function that records how many ran at once.
func runWithBatchesInFlight(t *testing.T, inFlight, batches int) int32 {
	t.Helper()
	configureViper(inFlight, 1_000_000)
	viper.Set("scrape.batches_in_flight", inFlight)

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
			return CrawlBatchOutput{Counts: ScrapeCounts{URLs: 10, PDFFound: 10}}, nil
		})

	env.OnActivity(PlanBatchesActivity, mock.Anything, mock.Anything).
		Return(PlanBatchesOutput{
			Batches:    refs(batches, 10, 0),
			NextOffset: int64(batches) * 100,
			EOF:        true,
		}, nil).Once()

	env.ExecuteWorkflow(ScrapeToWaybackWorkflow, ScrapeToWaybackInput{S3Key: "bucket/urls.txt"})
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var got ScrapeCounts
	require.NoError(t, env.GetWorkflowResult(&got))
	require.Equal(t, batches*10, got.URLs, "every batch should still be crawled")
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

// A retune signal is applied mid-run, and an out-of-range one is ignored rather
// than either clamping silently to something surprising or wedging the run.
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
			configureViper(1, 1_000_000)

			var ts testsuite.WorkflowTestSuite
			env := ts.NewTestWorkflowEnvironment()

			env.OnActivity(PlanBatchesActivity, mock.Anything, mock.Anything).
				Return(PlanBatchesOutput{Batches: refs(4, 10, 0), NextOffset: 400, EOF: true}, nil).Once()
			env.OnActivity(CrawlBatchActivity, mock.Anything, mock.Anything).
				Return(CrawlBatchOutput{Counts: ScrapeCounts{URLs: 10, PDFFound: 10}}, nil).Times(4)

			env.RegisterDelayedCallback(func() {
				env.SignalWorkflow(SetConcurrencySignal, tc.signal)
			}, 0)

			env.ExecuteWorkflow(ScrapeToWaybackWorkflow, ScrapeToWaybackInput{S3Key: "bucket/urls.txt"})

			require.True(t, env.IsWorkflowCompleted())
			require.NoError(t, env.GetWorkflowError())

			var got ScrapeCounts
			require.NoError(t, env.GetWorkflowResult(&got))
			require.Equal(t, 40, got.URLs, "every url is crawled regardless of the signal")
		})
	}
}

func Test_WorkflowID(t *testing.T) {
	require.Equal(t, "scrape_to_wayback_bucket_etd_wayback-seeds.txt",
		WorkflowID("bucket/etd/wayback-seeds.txt"))
}

// A run handed on by a worker from before this workflow used batches carries a
// BatchSize that meant something else -- URLs per S3 read -- and none of the
// batch settings. It has to notice that and re-read config, or it would plan
// with a zero batch count and fail the moment it tried.
func Test_Workflow_ResumeFromPreBatchInputRereadsConfig(t *testing.T) {
	configureViper(4, 1_000_000)

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	env.OnActivity(PlanBatchesActivity, mock.Anything, mock.MatchedBy(func(in PlanBatchesInput) bool {
		// The configured values, not the carried ones.
		return in.Offset == 4096 && in.BatchSize == 10 && in.MaxBatches == 8 && in.ChunkSize == 4096
	})).Return(PlanBatchesOutput{Batches: refs(1, 10, 4096), NextOffset: 4196, EOF: true}, nil).Once()
	env.OnActivity(CrawlBatchActivity, mock.Anything, mock.Anything).
		Return(CrawlBatchOutput{Counts: ScrapeCounts{URLs: 10, PDFFound: 10}}, nil).Once()

	env.ExecuteWorkflow(ScrapeToWaybackWorkflow, ScrapeToWaybackInput{
		S3Key: "bucket/urls.txt",
		// Everything a pre-batch run would have been carrying, and nothing else.
		Offset:               4096,
		Concurrency:          4,
		MaxConcurrency:       64,
		URLsPerCAN:           2000,
		BatchSize:            1000,
		ChunkSize:            102400,
		MaxConsecutiveErrors: 50,
		Counts:               ScrapeCounts{URLs: 900_000, PDFFound: 500_000},
	})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	// The byte offset and the running totals survive the handoff -- that is the
	// whole point of letting it resume rather than making it start over.
	var got ScrapeCounts
	require.NoError(t, env.GetWorkflowResult(&got))
	require.Equal(t, 900_010, got.URLs)
	require.Equal(t, 500_010, got.PDFFound)
	env.AssertExpectations(t)
}
