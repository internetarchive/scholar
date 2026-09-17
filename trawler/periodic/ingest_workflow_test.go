package periodic

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

func configureViper(concurrency, linesPerCAN int) {
	viper.Set("periodic.task_queue", "periodic_test")
	viper.Set("periodic.concurrency", concurrency)
	viper.Set("periodic.max_concurrency", 64)
	viper.Set("periodic.lines_per_can", linesPerCAN)
	viper.Set("periodic.batch_size", 10)
	viper.Set("periodic.batches_in_flight", 2)
	viper.Set("periodic.batches_per_plan", 8)
	viper.Set("periodic.items_per_plan", 4)
	viper.Set("periodic.chunk_size", 4096)
	viper.Set("periodic.max_consecutive_errors", 50)
	viper.Set("periodic.line_attempts", 3)
}

// itemRefs builds n batches of linesEach rows from one item, tiled from the top
// of that item. Ten bytes per row keeps the arithmetic in the assertions
// readable.
func itemRefs(itemIndex, n, linesEach int) []BatchRef {
	out := make([]BatchRef, 0, n)
	var pos int64
	for range n {
		end := pos + int64(linesEach)*10
		out = append(out, BatchRef{
			ItemIndex:  itemIndex,
			ItemID:     fmt.Sprintf("ITEM-%d", itemIndex),
			S3Key:      fmt.Sprintf("bucket/cdx/ITEM-%d", itemIndex),
			Offset:     pos,
			End:        end,
			LineCount:  linesEach,
			NextCursor: ItemCursor{Index: itemIndex, Offset: end},
		})
		pos = end
	}
	return out
}

// listed mocks the enumeration step. Every run that has not already pinned an
// item list starts with it.
func listed(env *testsuite.TestWorkflowEnvironment, count int) {
	env.OnActivity(ListCollectionActivity, mock.Anything, mock.MatchedBy(func(in ListCollectionInput) bool {
		return in.CollectionName == "COLL" && in.ItemListKey != ""
	})).Return(ListCollectionOutput{ItemCount: count}, nil).Once()
}

func Test_Workflow_HappyPathEOF(t *testing.T) {
	configureViper(4, 1_000_000)

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	listed(env, 12)
	env.OnActivity(PlanBatchesActivity, mock.Anything, mock.MatchedBy(func(in PlanBatchesInput) bool {
		return in.CollectionName == "COLL" && in.Cursor == ItemCursor{} &&
			in.BatchSize == 10 && in.MaxBatches == 8 && in.MaxItems == 4
	})).Return(PlanBatchesOutput{
		Batches:    itemRefs(0, 3, 10),
		NextCursor: ItemCursor{Index: 1},
		EOF:        true,
	}, nil).Once()

	env.OnActivity(IngestBatchActivity, mock.Anything, mock.Anything).
		Return(IngestBatchOutput{
			Counts:      PeriodicCounts{Lines: 10, PdfsSkipped: 7, PdfsAddedToDB: 3, FulltextAddedToES: 3},
			DoneThrough: 10,
		}, nil).Times(3)

	env.ExecuteWorkflow(PeriodicIngestWorkflow, PeriodicIngestInput{CollectionName: "COLL"})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var got PeriodicCounts
	require.NoError(t, env.GetWorkflowResult(&got))
	require.Equal(t, 30, got.Lines)
	require.Equal(t, 21, got.PdfsSkipped)
	require.Equal(t, 9, got.PdfsAddedToDB)
	require.Equal(t, 9, got.FulltextAddedToES)
	env.AssertExpectations(t)
}

// A collection with nothing in it is not an error; it is a collection we have
// nothing to do with.
func Test_Workflow_EmptyCollectionExits(t *testing.T) {
	configureViper(4, 1_000_000)

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()
	listed(env, 0)

	env.ExecuteWorkflow(PeriodicIngestWorkflow, PeriodicIngestInput{CollectionName: "COLL"})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	env.AssertExpectations(t)
}

// Picking up where a separate, earlier run stopped: the named item is resolved
// against the list this run pins, and planning starts there rather than at the
// top of the collection.
func Test_Workflow_StartsAtNamedItem(t *testing.T) {
	configureViper(1, 1_000_000)

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	env.OnActivity(ListCollectionActivity, mock.Anything, mock.MatchedBy(func(in ListCollectionInput) bool {
		return in.StartAtItem == "ITEM-40"
	})).Return(ListCollectionOutput{ItemCount: 120, StartIndex: 40}, nil).Once()

	env.OnActivity(PlanBatchesActivity, mock.Anything, mock.MatchedBy(func(in PlanBatchesInput) bool {
		return in.Cursor == ItemCursor{Index: 40, Offset: 2500}
	})).Return(PlanBatchesOutput{
		Batches:    itemRefs(40, 1, 10),
		NextCursor: ItemCursor{Index: 120},
		EOF:        true,
	}, nil).Once()

	env.OnActivity(IngestBatchActivity, mock.Anything, mock.Anything).
		Return(IngestBatchOutput{Counts: PeriodicCounts{Lines: 10}}, nil).Once()

	env.ExecuteWorkflow(PeriodicIngestWorkflow, PeriodicIngestInput{
		CollectionName: "COLL",
		StartAtItem:    "ITEM-40",
		StartAtOffset:  2500,
	})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	env.AssertExpectations(t)
}

// A run resumed from its own ContinueAsNew carries a cursor and must not
// re-resolve a start-at item against a list it is not going to re-pin.
func Test_Workflow_StartAtIsIgnoredOnResume(t *testing.T) {
	configureViper(1, 1_000_000)

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	env.OnActivity(PlanBatchesActivity, mock.Anything, mock.MatchedBy(func(in PlanBatchesInput) bool {
		return in.Cursor == ItemCursor{Index: 9, Offset: 300}
	})).Return(PlanBatchesOutput{NextCursor: ItemCursor{Index: 10}, EOF: true}, nil).Once()

	env.ExecuteWorkflow(PeriodicIngestWorkflow, PeriodicIngestInput{
		CollectionName: "COLL",
		// Carried from an earlier run of this chain, alongside a start-at that
		// has already been applied once and must not be applied again.
		StartAtItem:          "ITEM-40",
		StartAtOffset:        2500,
		ItemListKey:          "bucket/items.txt",
		ItemCount:            120,
		Cursor:               ItemCursor{Index: 9, Offset: 300},
		Source:               "carried-source",
		Concurrency:          1,
		MaxConcurrency:       8,
		LinesPerCAN:          100,
		BatchSize:            7,
		BatchesInFlight:      1,
		BatchesPerPlan:       3,
		ItemsPerPlan:         2,
		ChunkSize:            99,
		MaxConsecutiveErrors: 5,
		LineAttempts:         9,
	})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	env.AssertExpectations(t)
}

func Test_Workflow_RequiresCollectionName(t *testing.T) {
	configureViper(1, 10)
	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()
	env.ExecuteWorkflow(PeriodicIngestWorkflow, PeriodicIngestInput{})
	require.True(t, env.IsWorkflowCompleted())
	require.ErrorContains(t, env.GetWorkflowError(), "CollectionName is required")
}

// A batch reporting its circuit breaker tripped stops the run, and says where
// to resume from so the collection does not have to be restarted from the top.
func Test_Workflow_AbortedBatchStopsTheRun(t *testing.T) {
	configureViper(1, 1_000_000)

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	listed(env, 5)
	env.OnActivity(PlanBatchesActivity, mock.Anything, mock.Anything).
		Return(PlanBatchesOutput{Batches: itemRefs(2, 4, 10), NextCursor: ItemCursor{Index: 3}, EOF: true}, nil).Once()
	env.OnActivity(IngestBatchActivity, mock.Anything, mock.Anything).
		Return(IngestBatchOutput{
			Counts:      PeriodicCounts{Lines: 3, PdfsErrored: 3},
			DoneThrough: 3,
			NextOffset:  30,
			Aborted:     true,
			AbortReason: "3 consecutive ingest failures",
		}, nil)

	env.ExecuteWorkflow(PeriodicIngestWorkflow, PeriodicIngestInput{CollectionName: "COLL"})

	require.True(t, env.IsWorkflowCompleted())
	err := env.GetWorkflowError()
	require.ErrorContains(t, err, "consecutive ingest failures")
	require.ErrorContains(t, err, "resume at item 2 offset 30")
}

// A batch activity only fails for something systemic, so unlike a single dead
// PDF it is not something the run absorbs and carries on from.
func Test_Workflow_FailedBatchStopsTheRun(t *testing.T) {
	configureViper(1, 1_000_000)

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	listed(env, 5)
	env.OnActivity(PlanBatchesActivity, mock.Anything, mock.Anything).
		Return(PlanBatchesOutput{Batches: itemRefs(0, 2, 10), NextCursor: ItemCursor{Index: 1}, EOF: true}, nil).Once()
	env.OnActivity(IngestBatchActivity, mock.Anything, mock.Anything).
		Return(IngestBatchOutput{}, errors.New("read-only file system"))

	env.ExecuteWorkflow(PeriodicIngestWorkflow, PeriodicIngestInput{CollectionName: "COLL"})

	require.True(t, env.IsWorkflowCompleted())
	require.ErrorContains(t, env.GetWorkflowError(), "read-only file system")
}

// A planning call that yields nothing and does not advance would spin forever.
func Test_Workflow_StopsIfPlanningMakesNoProgress(t *testing.T) {
	configureViper(1, 1_000_000)

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	listed(env, 5)
	env.OnActivity(PlanBatchesActivity, mock.Anything, mock.Anything).
		Return(PlanBatchesOutput{NextCursor: ItemCursor{}, EOF: false}, nil)

	env.ExecuteWorkflow(PeriodicIngestWorkflow, PeriodicIngestInput{CollectionName: "COLL"})

	require.True(t, env.IsWorkflowCompleted())
	require.ErrorContains(t, env.GetWorkflowError(), "made no progress")
}

// A stretch of items with no PDFs at all advances the cursor without producing
// batches, which must not look like a stalled plan.
func Test_Workflow_EmptyItemsAreProgress(t *testing.T) {
	configureViper(1, 1_000_000)

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	listed(env, 9)
	env.OnActivity(PlanBatchesActivity, mock.Anything, mock.MatchedBy(func(in PlanBatchesInput) bool {
		return in.Cursor == ItemCursor{}
	})).Return(PlanBatchesOutput{NextCursor: ItemCursor{Index: 4}, EOF: false}, nil).Once()
	env.OnActivity(PlanBatchesActivity, mock.Anything, mock.MatchedBy(func(in PlanBatchesInput) bool {
		return in.Cursor == ItemCursor{Index: 4}
	})).Return(PlanBatchesOutput{NextCursor: ItemCursor{Index: 9}, EOF: true}, nil).Once()

	env.ExecuteWorkflow(PeriodicIngestWorkflow, PeriodicIngestInput{CollectionName: "COLL"})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	env.AssertExpectations(t)
}

// Hitting lines_per_can hands off to a fresh run rather than growing one run's
// history without bound.
func Test_Workflow_ContinuesAsNewAtThreshold(t *testing.T) {
	configureViper(1, 30)

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	listed(env, 5)
	env.OnActivity(PlanBatchesActivity, mock.Anything, mock.Anything).
		Return(PlanBatchesOutput{Batches: itemRefs(1, 5, 10), NextCursor: ItemCursor{Index: 2}, EOF: false}, nil).Once()
	// Only three of the five batches are dispatched before the handoff.
	env.OnActivity(IngestBatchActivity, mock.Anything, mock.Anything).
		Return(IngestBatchOutput{Counts: PeriodicCounts{Lines: 10, PdfsSkipped: 10}}, nil).Times(3)

	env.ExecuteWorkflow(PeriodicIngestWorkflow, PeriodicIngestInput{CollectionName: "COLL"})

	require.True(t, env.IsWorkflowCompleted())
	var canErr *workflow.ContinueAsNewError
	require.True(t, errors.As(env.GetWorkflowError(), &canErr),
		"expected ContinueAsNewError, got %v", env.GetWorkflowError())
	env.AssertExpectations(t)
}

// The resume cursor is the one carried by the last batch actually dispatched,
// so the next run neither re-ingests nor skips. The pinned item list and the
// source label come across too -- re-deriving either would restart the
// collection or stamp the handoff with a different provenance.
func Test_Workflow_ResumeStateIsLastDispatchedBatch(t *testing.T) {
	configureViper(1, 30)

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	listed(env, 5)

	// The third batch is the last of its item, so it resumes at the next one.
	batches := itemRefs(1, 5, 10)
	batches[2].NextCursor = ItemCursor{Index: 2}

	env.OnActivity(PlanBatchesActivity, mock.Anything, mock.Anything).
		Return(PlanBatchesOutput{Batches: batches, NextCursor: ItemCursor{Index: 3}, EOF: false}, nil).Once()

	var source atomic.Value
	env.OnActivity(IngestBatchActivity, mock.Anything, mock.Anything).
		Return(func(_ context.Context, in IngestBatchInput) (IngestBatchOutput, error) {
			source.Store(in.Source)
			return IngestBatchOutput{Counts: PeriodicCounts{Lines: 10, PdfsSkipped: 10}}, nil
		}).Times(3)

	env.ExecuteWorkflow(PeriodicIngestWorkflow, PeriodicIngestInput{CollectionName: "COLL"})

	var canErr *workflow.ContinueAsNewError
	require.True(t, errors.As(env.GetWorkflowError(), &canErr))

	var next PeriodicIngestInput
	require.NoError(t, converter.GetDefaultDataConverter().FromPayloads(canErr.Input, &next))
	// The cursor the third batch carried, not the plan's NextCursor of item 3.
	require.Equal(t, ItemCursor{Index: 2}, next.Cursor)
	require.Equal(t, 30, next.Counts.Lines)
	require.NotEmpty(t, next.ItemListKey)
	require.Equal(t, 5, next.ItemCount)
	require.Equal(t, source.Load(), next.Source)
	require.Contains(t, next.Source, "COLL")
}

// A resumed run reuses the carried config, item list and cursor instead of
// re-reading viper or re-enumerating the collection. Re-enumerating would shift
// every index under the cursor it is resuming from.
func Test_Workflow_ResumeUsesCarriedState(t *testing.T) {
	configureViper(1, 1_000_000)

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	env.OnActivity(PlanBatchesActivity, mock.Anything, mock.MatchedBy(func(in PlanBatchesInput) bool {
		return in.ItemListKey == "bucket/items.txt" &&
			in.Cursor == ItemCursor{Index: 7, Offset: 500} &&
			in.BatchSize == 7 && in.MaxBatches == 3 && in.MaxItems == 2 && in.ChunkSize == 99
	})).Return(PlanBatchesOutput{
		Batches:    []BatchRef{{ItemIndex: 7, ItemID: "ITEM-7", Offset: 500, End: 570, LineCount: 7}},
		NextCursor: ItemCursor{Index: 8},
		EOF:        true,
	}, nil).Once()

	env.OnActivity(IngestBatchActivity, mock.Anything, mock.MatchedBy(func(in IngestBatchInput) bool {
		return in.ChunkSize == 99 && in.LineAttempts == 9 && in.MaxConsecutiveErrors == 5 &&
			in.Source == "carried-source"
	})).Return(IngestBatchOutput{Counts: PeriodicCounts{Lines: 7, PdfsSkipped: 7}}, nil).Once()

	env.ExecuteWorkflow(PeriodicIngestWorkflow, PeriodicIngestInput{
		CollectionName:       "COLL",
		Source:               "carried-source",
		ItemListKey:          "bucket/items.txt",
		ItemCount:            40,
		Cursor:               ItemCursor{Index: 7, Offset: 500},
		Concurrency:          1,
		MaxConcurrency:       8,
		LinesPerCAN:          100,
		BatchSize:            7,
		BatchesInFlight:      1,
		BatchesPerPlan:       3,
		ItemsPerPlan:         2,
		ChunkSize:            99,
		MaxConsecutiveErrors: 5,
		LineAttempts:         9,
		Counts:               PeriodicCounts{Lines: 42, PdfsSkipped: 40},
	})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var got PeriodicCounts
	require.NoError(t, env.GetWorkflowResult(&got))
	require.Equal(t, 49, got.Lines) // 42 carried + 7 new
	require.Equal(t, 47, got.PdfsSkipped)
	env.AssertExpectations(t)
}

// An explicit source override survives the ContinueAsNew chain unchanged.
func Test_Workflow_SourceOverrideIsUsed(t *testing.T) {
	configureViper(1, 1_000_000)

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	listed(env, 2)
	env.OnActivity(PlanBatchesActivity, mock.Anything, mock.Anything).
		Return(PlanBatchesOutput{Batches: itemRefs(0, 1, 10), NextCursor: ItemCursor{Index: 2}, EOF: true}, nil).Once()
	env.OnActivity(IngestBatchActivity, mock.Anything, mock.MatchedBy(func(in IngestBatchInput) bool {
		return in.Source == "hand-labelled"
	})).Return(IngestBatchOutput{Counts: PeriodicCounts{Lines: 10}}, nil).Once()

	env.ExecuteWorkflow(PeriodicIngestWorkflow, PeriodicIngestInput{
		CollectionName: "COLL",
		SourceOverride: "hand-labelled",
	})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	env.AssertExpectations(t)
}

// Concurrency is clamped so neither a start flag nor a retune signal can point
// an unbounded number of PDFs at GROBID.
func Test_Workflow_ClampsConcurrencyToMax(t *testing.T) {
	configureViper(1, 1_000_000)

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	listed(env, 2)
	env.OnActivity(PlanBatchesActivity, mock.Anything, mock.Anything).
		Return(PlanBatchesOutput{Batches: itemRefs(0, 1, 10), NextCursor: ItemCursor{Index: 2}, EOF: true}, nil).Once()
	env.OnActivity(IngestBatchActivity, mock.Anything, mock.MatchedBy(func(in IngestBatchInput) bool {
		// 64 PDFs over 2 batches in flight, not 10_000 over 2.
		return in.Parallelism == 32
	})).Return(IngestBatchOutput{Counts: PeriodicCounts{Lines: 10}}, nil).Once()

	env.ExecuteWorkflow(PeriodicIngestWorkflow, PeriodicIngestInput{
		CollectionName: "COLL",
		Concurrency:    10_000,
	})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	env.AssertExpectations(t)
}

// The run's concurrency reaches the batch as its pool size, which is the only
// way it reaches GROBID at all now.
func Test_Workflow_ConcurrencyReachesTheBatch(t *testing.T) {
	configureViper(6, 1_000_000)

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	listed(env, 2)
	env.OnActivity(PlanBatchesActivity, mock.Anything, mock.Anything).
		Return(PlanBatchesOutput{Batches: itemRefs(0, 2, 10), NextCursor: ItemCursor{Index: 2}, EOF: true}, nil).Once()
	// batches_in_flight is 2, so six PDFs at once means three per batch.
	env.OnActivity(IngestBatchActivity, mock.Anything, mock.MatchedBy(func(in IngestBatchInput) bool {
		return in.Parallelism == 3
	})).Return(IngestBatchOutput{Counts: PeriodicCounts{Lines: 10}}, nil).Twice()

	env.ExecuteWorkflow(PeriodicIngestWorkflow, PeriodicIngestInput{CollectionName: "COLL"})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	env.AssertExpectations(t)
}

// The window must actually bound batches in flight -- that is what keeps a run
// from handing every batch of a huge collection to the workers at once. Mocked
// returns alone would pass whether the window worked or not, so this drives a
// real activity function that records how many ran at once.
func runWithBatchesInFlight(t *testing.T, inFlight, batches int) int32 {
	t.Helper()
	configureViper(inFlight, 1_000_000)
	viper.Set("periodic.batches_in_flight", inFlight)

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	listed(env, 2)

	var cur, peak atomic.Int32
	env.OnActivity(IngestBatchActivity, mock.Anything, mock.Anything).
		Return(func(context.Context, IngestBatchInput) (IngestBatchOutput, error) {
			n := cur.Add(1)
			for {
				m := peak.Load()
				if n <= m || peak.CompareAndSwap(m, n) {
					break
				}
			}
			time.Sleep(50 * time.Millisecond)
			cur.Add(-1)
			return IngestBatchOutput{Counts: PeriodicCounts{Lines: 10, PdfsSkipped: 10}}, nil
		})

	env.OnActivity(PlanBatchesActivity, mock.Anything, mock.Anything).
		Return(PlanBatchesOutput{
			Batches:    itemRefs(0, batches, 10),
			NextCursor: ItemCursor{Index: 2},
			EOF:        true,
		}, nil).Once()

	env.ExecuteWorkflow(PeriodicIngestWorkflow, PeriodicIngestInput{CollectionName: "COLL"})
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var got PeriodicCounts
	require.NoError(t, env.GetWorkflowResult(&got))
	require.Equal(t, batches*10, got.Lines, "every batch should still be ingested")
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

			listed(env, 2)
			env.OnActivity(PlanBatchesActivity, mock.Anything, mock.Anything).
				Return(PlanBatchesOutput{Batches: itemRefs(0, 4, 10), NextCursor: ItemCursor{Index: 2}, EOF: true}, nil).Once()
			env.OnActivity(IngestBatchActivity, mock.Anything, mock.Anything).
				Return(IngestBatchOutput{Counts: PeriodicCounts{Lines: 10, PdfsSkipped: 10}}, nil).Times(4)

			env.RegisterDelayedCallback(func() {
				env.SignalWorkflow(SetConcurrencySignal, tc.signal)
			}, 0)

			env.ExecuteWorkflow(PeriodicIngestWorkflow, PeriodicIngestInput{CollectionName: "COLL"})

			require.True(t, env.IsWorkflowCompleted())
			require.NoError(t, env.GetWorkflowError())

			var got PeriodicCounts
			require.NoError(t, env.GetWorkflowResult(&got))
			require.Equal(t, 40, got.Lines, "every row is ingested regardless of the signal")
		})
	}
}

// --- splitConcurrency -----------------------------------------------------

// The signal and the CLI flag both speak in PDFs, because that is what an
// operator watching GROBID cares about. Turning that into batches is this
// function's whole job, and getting it wrong means either overrunning GROBID or
// leaving worker hosts idle.
func Test_splitConcurrency(t *testing.T) {
	for _, tc := range []struct {
		concurrency, inFlight int
		batches, parallelism  int
	}{
		{concurrency: 8, inFlight: 4, batches: 4, parallelism: 2},
		{concurrency: 4, inFlight: 4, batches: 4, parallelism: 1},
		// Fewer PDFs than batches: there is nothing to divide, so the batch
		// count comes down instead of the pool going below one.
		{concurrency: 2, inFlight: 4, batches: 2, parallelism: 1},
		{concurrency: 1, inFlight: 4, batches: 1, parallelism: 1},
		// Rounding up rather than down: a run must not quietly ingest fewer PDFs
		// at once than it was told to.
		{concurrency: 10, inFlight: 4, batches: 4, parallelism: 3},
		{concurrency: 0, inFlight: 0, batches: 1, parallelism: 1},
	} {
		t.Run(fmt.Sprintf("c=%d/f=%d", tc.concurrency, tc.inFlight), func(t *testing.T) {
			batches, parallelism := splitConcurrency(tc.concurrency, tc.inFlight)
			require.Equal(t, tc.batches, batches)
			require.Equal(t, tc.parallelism, parallelism)
			require.GreaterOrEqual(t, batches*parallelism, max(tc.concurrency, 1),
				"the run would ingest fewer pdfs at once than asked")
		})
	}
}

func Test_WorkflowID(t *testing.T) {
	require.Equal(t, "periodic_ingest_CATCHUP-CRAWL-2024-10",
		WorkflowID("CATCHUP-CRAWL-2024-10"))
}
