package daily

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"git.archive.org/webgroup/scholar/trawler/counts"
	"git.archive.org/webgroup/scholar/trawler/harvesting"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

// ignored builds a Counts with the given Releases.Ignored value, which the
// tests use as a cheap tracer to confirm per-line results are summed.
func ignored(n int) counts.Counts {
	return counts.Counts{Releases: counts.ReleaseCounts{Ignored: n}}
}

// configureViper sets the queue names and CAN cadence the workflow reads. Tests
// set lines_per_can explicitly so they don't depend on the shared default or on
// each other's ordering (viper state is global).
func configureViper(upstream string, linesPerCAN int) {
	viper.Set(fmt.Sprintf("%s.external_task_queue", upstream), "ext")
	viper.Set(fmt.Sprintf("%s.internal_task_queue", upstream), "int")
	viper.Set("daily.lines_per_can", linesPerCAN)
}

// On the first run (S3Key empty) the workflow scrapes once, processes every line
// in the file, and returns the summed counts when FindLineBatch reports EOF.
func TestDailyCrawlWorkflow_HappyPathEOF(t *testing.T) {
	configureViper("crossref", 1_000_000) // high enough that CAN never triggers

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	env.OnActivity(ScholkitScrapeActivity, mock.Anything, mock.Anything).
		Return(scholkitScrapeOutput{S3Key: "meta.ndjson"}, nil).Once()

	env.OnActivity(harvesting.FindLineBatch, mock.Anything, mock.Anything).
		Return(harvesting.FindLineBatchOutput{
			Offsets:   [][]int64{{0, 10}, {10, 20}, {30, 15}},
			BytesRead: 45,
			EOF:       true,
		}, nil).Once()

	// Every ProcessLine call must see the scraped S3 key and the (overridden)
	// source; each contributes Ignored:1.
	env.OnActivity(ProcessLine, mock.Anything, mock.MatchedBy(func(in harvesting.ProcessLineInput) bool {
		return in.S3Key == "meta.ndjson" && in.Source == "fixed-src" && in.Upstream == "crossref"
	})).Return(ignored(1), nil).Times(3)

	env.ExecuteWorkflow(DailyCrawlWorkflow, DailyCrawlWorkflowInput{
		Day:            "2026-05-20",
		Upstream:       "crossref",
		SourceOverride: "fixed-src",
	})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var result counts.Counts
	require.NoError(t, env.GetWorkflowResult(&result))
	require.Equal(t, 3, result.Releases.Ignored)
	env.AssertExpectations(t)
}

// A continuation run (S3Key already set, as ContinueAsNew would carry it) must
// skip the scrape, resume FindLineBatch at the carried byte offset, reuse the
// pinned source, and fold new results into the carried Counts.
func TestDailyCrawlWorkflow_ResumeSkipsScrapeAndCarriesState(t *testing.T) {
	configureViper("crossref", 1_000_000)

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	// FindLineBatch must be asked to resume at the carried offset on the carried key.
	env.OnActivity(harvesting.FindLineBatch, mock.Anything, mock.MatchedBy(func(in harvesting.FindLineBatchInput) bool {
		return in.S3Key == "carried-key" && in.Offset == 500
	})).Return(harvesting.FindLineBatchOutput{
		Offsets:   [][]int64{{500, 10}, {510, 10}},
		BytesRead: 520,
		EOF:       true,
	}, nil).Once()

	// The pinned source is reused, not re-derived.
	env.OnActivity(ProcessLine, mock.Anything, mock.MatchedBy(func(in harvesting.ProcessLineInput) bool {
		return in.Source == "pinned-src" && in.S3Key == "carried-key"
	})).Return(ignored(1), nil).Times(2)

	env.ExecuteWorkflow(DailyCrawlWorkflow, DailyCrawlWorkflowInput{
		Upstream: "crossref",
		S3Key:    "carried-key",
		Source:   "pinned-src",
		Offset:   500,
		Counts:   ignored(10),
	})

	require.True(t, env.IsWorkflowCompleted())
	// Not mocking ScholkitScrapeActivity means a call to it would fail the run;
	// NoError therefore proves the scrape was skipped.
	require.NoError(t, env.GetWorkflowError())

	var result counts.Counts
	require.NoError(t, env.GetWorkflowResult(&result))
	require.Equal(t, 12, result.Releases.Ignored) // 10 carried + 2 new
	env.AssertExpectations(t)
}

// When a run processes lines_per_can lines without hitting EOF, it hands off via
// ContinueAsNew rather than returning. The CAN check lives inside the per-line
// loop, so a batch larger than lines_per_can hands off mid-batch instead of
// draining the whole batch first: with five offsets and a threshold of three,
// only three lines are processed before CAN.
func TestDailyCrawlWorkflow_ContinuesAsNewAtThreshold(t *testing.T) {
	configureViper("crossref", 3) // CAN after 3 lines

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	env.OnActivity(ScholkitScrapeActivity, mock.Anything, mock.Anything).
		Return(scholkitScrapeOutput{S3Key: "meta.ndjson"}, nil).Once()

	// Five lines in one batch, no EOF: the third line hits 3 >= 3 and triggers
	// CAN, leaving the last two offsets unprocessed this run.
	env.OnActivity(harvesting.FindLineBatch, mock.Anything, mock.Anything).
		Return(harvesting.FindLineBatchOutput{
			Offsets:   [][]int64{{0, 5}, {5, 5}, {10, 5}, {15, 5}, {20, 5}},
			BytesRead: 25,
			EOF:       false,
		}, nil).Once()

	env.OnActivity(ProcessLine, mock.Anything, mock.Anything).
		Return(ignored(1), nil).Times(3)

	env.ExecuteWorkflow(DailyCrawlWorkflow, DailyCrawlWorkflowInput{
		Upstream:       "crossref",
		SourceOverride: "src",
	})

	require.True(t, env.IsWorkflowCompleted())
	err := env.GetWorkflowError()
	require.Error(t, err)
	var canErr *workflow.ContinueAsNewError
	require.True(t, errors.As(err, &canErr), "expected ContinueAsNewError, got %v", err)
	env.AssertExpectations(t)
}

// Config (lines_per_can, batch_size, chunk_size) is read from viper once on the
// first run and threaded into FindLineBatch, rather than the workflow reading
// viper inline on every run. This is what keeps the values fixed and
// replay-deterministic for the whole ContinueAsNew chain.
func TestDailyCrawlWorkflow_CapturesConfigFromViper(t *testing.T) {
	configureViper("crossref", 1_000_000)
	viper.Set("harvesting.batch_size", 250)
	viper.Set("harvesting.chunk_size", 4096)

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	env.OnActivity(ScholkitScrapeActivity, mock.Anything, mock.Anything).
		Return(scholkitScrapeOutput{S3Key: "meta.ndjson"}, nil).Once()

	// FindLineBatch must be handed the viper-sourced batch/chunk sizes even
	// though they were never set on the input struct.
	env.OnActivity(harvesting.FindLineBatch, mock.Anything, mock.MatchedBy(func(in harvesting.FindLineBatchInput) bool {
		return in.BatchSize == 250 && in.ChunkSize == 4096
	})).Return(harvesting.FindLineBatchOutput{
		Offsets:   [][]int64{{0, 10}},
		BytesRead: 10,
		EOF:       true,
	}, nil).Once()

	env.OnActivity(ProcessLine, mock.Anything, mock.Anything).
		Return(ignored(1), nil).Times(1)

	env.ExecuteWorkflow(DailyCrawlWorkflow, DailyCrawlWorkflowInput{
		Upstream:       "crossref",
		SourceOverride: "src",
	})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	env.AssertExpectations(t)
}

// A scrape failure on the first run aborts the workflow before any line
// processing. The error is returned as non-retryable so the test env doesn't
// loop on the default activity retry policy.
func TestDailyCrawlWorkflow_ScrapeErrorAborts(t *testing.T) {
	configureViper("crossref", 1_000_000)

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	env.OnActivity(ScholkitScrapeActivity, mock.Anything, mock.Anything).
		Return(scholkitScrapeOutput{}, temporal.NewNonRetryableApplicationError("boom", "ScrapeError", nil)).Once()

	env.ExecuteWorkflow(DailyCrawlWorkflow, DailyCrawlWorkflowInput{
		Upstream:       "crossref",
		SourceOverride: "src",
	})

	require.True(t, env.IsWorkflowCompleted())
	require.Error(t, env.GetWorkflowError())
	// FindLineBatch / ProcessLine were never mocked; reaching them would fail
	// the run differently, so this also confirms processing never started.
	env.AssertExpectations(t)
}

// An empty Day is resolved from the workflow clock, and the resolved day has to
// reach both the scrape activity and the source label: deriving one without the
// other yields records labelled for a day whose data was never fetched.
//
// DOAJ is where that divergence bites. Their API serves non-paying callers only
// records older than a month, so "no day given" means a month and a day back,
// not yesterday -- and a label-only offset would leave us scraping a window
// DOAJ won't answer.
func TestDailyCrawlWorkflow_ResolvesDOAJDayBackAMonth(t *testing.T) {
	configureViper("doaj", 1_000_000)

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()
	env.SetStartTime(time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC))

	// 32 days before the start time, not 1.
	env.OnActivity(ScholkitScrapeActivity, mock.Anything, mock.MatchedBy(func(in scholkitScrapeInput) bool {
		return in.Day == "2026-04-18" && in.Upstream == "doaj"
	})).Return(scholkitScrapeOutput{S3Key: "meta.ndjson"}, nil).Once()

	env.OnActivity(harvesting.FindLineBatch, mock.Anything, mock.Anything).
		Return(harvesting.FindLineBatchOutput{
			Offsets:   [][]int64{{0, 10}},
			BytesRead: 10,
			EOF:       true,
		}, nil).Once()

	// The generated label names the same day that was scraped. Its RunID segment
	// is dynamic, so match around it.
	env.OnActivity(ProcessLine, mock.Anything, mock.MatchedBy(func(in harvesting.ProcessLineInput) bool {
		return strings.HasPrefix(in.Source, "daily-20260418-") && strings.HasSuffix(in.Source, "-doaj")
	})).Return(ignored(1), nil).Once()

	env.ExecuteWorkflow(DailyCrawlWorkflow, DailyCrawlWorkflowInput{
		Upstream: "doaj",
	})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	env.AssertExpectations(t)
}

// Every other upstream takes the ordinary path: an empty Day is yesterday
// relative to the workflow clock. This is the counterpart to the DOAJ case --
// together they pin the offset as upstream-dependent rather than global.
func TestDailyCrawlWorkflow_ResolvesDayToYesterday(t *testing.T) {
	configureViper("crossref", 1_000_000)

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()
	env.SetStartTime(time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC))

	env.OnActivity(ScholkitScrapeActivity, mock.Anything, mock.MatchedBy(func(in scholkitScrapeInput) bool {
		return in.Day == "2026-05-19" && in.Upstream == "crossref"
	})).Return(scholkitScrapeOutput{S3Key: "meta.ndjson"}, nil).Once()

	env.OnActivity(harvesting.FindLineBatch, mock.Anything, mock.Anything).
		Return(harvesting.FindLineBatchOutput{
			Offsets:   [][]int64{{0, 10}},
			BytesRead: 10,
			EOF:       true,
		}, nil).Once()

	env.OnActivity(ProcessLine, mock.Anything, mock.MatchedBy(func(in harvesting.ProcessLineInput) bool {
		return strings.HasPrefix(in.Source, "daily-20260519-") && strings.HasSuffix(in.Source, "-crossref")
	})).Return(ignored(1), nil).Once()

	env.ExecuteWorkflow(DailyCrawlWorkflow, DailyCrawlWorkflowInput{
		Upstream: "crossref",
	})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	env.AssertExpectations(t)
}

// An explicit Day is used verbatim and the clock is never consulted -- the
// start time here is months away from the requested day, so any fallback to
// "yesterday" would show up in both the scrape input and the label. This is
// also the branch that parses Day back out of the input, which is only exercised
// when a caller supplies one (as `daily one-off --day` now always does).
func TestDailyCrawlWorkflow_ExplicitDayOverridesClock(t *testing.T) {
	configureViper("crossref", 1_000_000)

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()
	env.SetStartTime(time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC))

	env.OnActivity(ScholkitScrapeActivity, mock.Anything, mock.MatchedBy(func(in scholkitScrapeInput) bool {
		return in.Day == "2026-03-05"
	})).Return(scholkitScrapeOutput{S3Key: "meta.ndjson"}, nil).Once()

	env.OnActivity(harvesting.FindLineBatch, mock.Anything, mock.Anything).
		Return(harvesting.FindLineBatchOutput{
			Offsets:   [][]int64{{0, 10}},
			BytesRead: 10,
			EOF:       true,
		}, nil).Once()

	env.OnActivity(ProcessLine, mock.Anything, mock.MatchedBy(func(in harvesting.ProcessLineInput) bool {
		return strings.HasPrefix(in.Source, "daily-20260305-")
	})).Return(ignored(1), nil).Once()

	env.ExecuteWorkflow(DailyCrawlWorkflow, DailyCrawlWorkflowInput{
		Day:      "2026-03-05",
		Upstream: "crossref",
	})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	env.AssertExpectations(t)
}

// A Day that isn't in the documented 2006-01-02 format fails the run outright
// rather than being silently coerced or reaching the scrape activity, which
// parses it again with the same layout.
func TestDailyCrawlWorkflow_MalformedDayFails(t *testing.T) {
	configureViper("crossref", 1_000_000)

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	env.ExecuteWorkflow(DailyCrawlWorkflow, DailyCrawlWorkflowInput{
		Day:      "20260305",
		Upstream: "crossref",
	})

	require.True(t, env.IsWorkflowCompleted())
	require.Error(t, env.GetWorkflowError())
	// Nothing was mocked, so reaching any activity would fail differently.
	env.AssertExpectations(t)
}
