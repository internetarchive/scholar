package scrape

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
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

// --- scanURLs -------------------------------------------------------------

func scan(t *testing.T, body string, offset int64, maxURLs, chunk int) ReadURLBatchOutput {
	t.Helper()
	out, err := scanURLs(ReadURLBatchInput{
		Offset:    offset,
		MaxURLs:   maxURLs,
		ChunkSize: chunk,
	}, bytes.NewReader([]byte(body)))
	require.NoError(t, err)
	return out
}

func urls(refs []URLRef) []string {
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		out = append(out, r.URL)
	}
	return out
}

func Test_scanURLs_readsWholeList(t *testing.T) {
	out := scan(t, "http://a/\nhttp://b/\nhttp://c/\n", 0, 100, 4096)
	require.Equal(t, []string{"http://a/", "http://b/", "http://c/"}, urls(out.URLs))
	require.True(t, out.EOF)
	require.Equal(t, int64(30), out.NextOffset)
	// End is the byte after each line's newline, which is where a resume picks up.
	require.Equal(t, int64(10), out.URLs[0].End)
	require.Equal(t, int64(20), out.URLs[1].End)
	require.Equal(t, int64(30), out.URLs[2].End)
}

func Test_scanURLs_skipsBlankLinesAndTrimsCR(t *testing.T) {
	out := scan(t, "http://a/\r\n\n   \nhttp://b/\r\n", 0, 100, 4096)
	require.Equal(t, []string{"http://a/", "http://b/"}, urls(out.URLs))
	require.True(t, out.EOF)
}

// A final line with no trailing newline still counts.
func Test_scanURLs_unterminatedFinalLine(t *testing.T) {
	out := scan(t, "http://a/\nhttp://b/", 0, 100, 4096)
	require.Equal(t, []string{"http://a/", "http://b/"}, urls(out.URLs))
	require.True(t, out.EOF)
	require.Equal(t, int64(19), out.URLs[1].End)
}

// Filling MaxURLs part-way through the object is not EOF: reporting it there
// would silently abandon the rest of the list.
func Test_scanURLs_batchFullIsNotEOF(t *testing.T) {
	out := scan(t, "http://a/\nhttp://b/\nhttp://c/\n", 0, 2, 4096)
	require.Equal(t, []string{"http://a/", "http://b/"}, urls(out.URLs))
	require.False(t, out.EOF)
	require.Equal(t, int64(20), out.NextOffset)

	// Resuming from NextOffset yields exactly the remainder, no repeats.
	rest := scan(t, "http://a/\nhttp://b/\nhttp://c/\n", out.NextOffset, 2, 4096)
	require.Equal(t, []string{"http://c/"}, urls(rest.URLs))
	require.True(t, rest.EOF)
}

// A URL longer than one chunk must be stitched across reads, not truncated.
func Test_scanURLs_lineSpansChunks(t *testing.T) {
	long := "http://example.com/" + string(bytes.Repeat([]byte("x"), 200))
	out := scan(t, long+"\nhttp://b/\n", 0, 100, 8)
	require.Equal(t, []string{long, "http://b/"}, urls(out.URLs))
	require.True(t, out.EOF)
}

func Test_scanURLs_emptyObject(t *testing.T) {
	out := scan(t, "", 0, 100, 4096)
	require.Empty(t, out.URLs)
	require.True(t, out.EOF)
	require.Equal(t, int64(0), out.NextOffset)
}

func Test_scanURLs_offsetPastEnd(t *testing.T) {
	out := scan(t, "http://a/\n", 10, 100, 4096)
	require.Empty(t, out.URLs)
	require.True(t, out.EOF)
}

func Test_ReadURLBatch_rejectsBadConfig(t *testing.T) {
	_, err := ReadURLBatch(t.Context(), ReadURLBatchInput{S3Key: "b/k", MaxURLs: 0, ChunkSize: 10})
	require.Error(t, err)
}

// --- counts ---------------------------------------------------------------

func Test_CrawlOutcomeCounts(t *testing.T) {
	pdf := CrawlToWaybackOutput{Success: true}.Counts()
	require.Equal(t, 1, pdf.URLs)
	require.Equal(t, 1, pdf.PDFFound)
	require.Empty(t, pdf.FailReasons)

	// A dead end still means SPN captured a page, so it is NoPDF, not Skipped.
	deadEnd := CrawlToWaybackOutput{FailReason: "dead-end"}.Counts()
	require.Equal(t, 1, deadEnd.NoPDF)
	require.Equal(t, 0, deadEnd.Skipped)
	require.Equal(t, map[string]int{"dead-end": 1}, deadEnd.FailReasons)

	// Blocklisted URLs never reached the network at all.
	blocked := CrawlToWaybackOutput{FailReason: "blocklist"}.Counts()
	require.Equal(t, 1, blocked.Skipped)
	require.Equal(t, 0, blocked.NoPDF)
}

func Test_ScrapeCountsAdd(t *testing.T) {
	a := ScrapeCounts{URLs: 2, PDFFound: 1, NoPDF: 1, FailReasons: map[string]int{"dead-end": 1}}
	b := ScrapeCounts{URLs: 3, NoPDF: 3, FailReasons: map[string]int{"dead-end": 2, "status-404": 1}}
	got := a.Add(b)
	require.Equal(t, 5, got.URLs)
	require.Equal(t, 1, got.PDFFound)
	require.Equal(t, 4, got.NoPDF)
	require.Equal(t, map[string]int{"dead-end": 3, "status-404": 1}, got.FailReasons)

	// Adding empties must not allocate a map that then serialises as {}.
	require.Nil(t, ScrapeCounts{}.Add(ScrapeCounts{}).FailReasons)
}

// --- workflow -------------------------------------------------------------

func configureViper(concurrency, urlsPerCAN int) {
	viper.Set("scrape.task_queue", "scrape_test")
	viper.Set("scrape.concurrency", concurrency)
	viper.Set("scrape.max_concurrency", 64)
	viper.Set("scrape.urls_per_can", urlsPerCAN)
	viper.Set("scrape.batch_size", 1000)
	viper.Set("scrape.chunk_size", 4096)
	viper.Set("scrape.max_consecutive_errors", 50)
}

func refs(n int, from int64) []URLRef {
	out := make([]URLRef, 0, n)
	for i := range n {
		out = append(out, URLRef{
			URL: fmt.Sprintf("http://example.com/%d", i),
			End: from + int64(i+1)*10,
		})
	}
	return out
}

func Test_Workflow_HappyPathEOF(t *testing.T) {
	configureViper(2, 1_000_000)

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	env.OnActivity(ReadURLBatch, mock.Anything, mock.MatchedBy(func(in ReadURLBatchInput) bool {
		return in.S3Key == "bucket/urls.txt" && in.Offset == 0
	})).Return(ReadURLBatchOutput{URLs: refs(3, 0), NextOffset: 30, EOF: true}, nil).Once()

	env.OnActivity(CrawlToWaybackActivity, mock.Anything, mock.Anything).
		Return(CrawlToWaybackOutput{Success: true}, nil).Times(3)

	env.ExecuteWorkflow(ScrapeToWaybackWorkflow, ScrapeToWaybackInput{S3Key: "bucket/urls.txt"})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var got ScrapeCounts
	require.NoError(t, env.GetWorkflowResult(&got))
	require.Equal(t, 3, got.URLs)
	require.Equal(t, 3, got.PDFFound)
	env.AssertExpectations(t)
}

// Failing to find a PDF is an ordinary outcome, recorded by reason and folded
// into the totals rather than failing the run.
func Test_Workflow_CountsFailReasons(t *testing.T) {
	configureViper(2, 1_000_000)

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	env.OnActivity(ReadURLBatch, mock.Anything, mock.Anything).
		Return(ReadURLBatchOutput{URLs: refs(2, 0), NextOffset: 20, EOF: true}, nil).Once()

	env.OnActivity(CrawlToWaybackActivity, mock.Anything, mock.Anything).
		Return(CrawlToWaybackOutput{FailReason: "dead-end"}, nil).Once()
	env.OnActivity(CrawlToWaybackActivity, mock.Anything, mock.Anything).
		Return(CrawlToWaybackOutput{FailReason: "blocklist"}, nil).Once()

	env.ExecuteWorkflow(ScrapeToWaybackWorkflow, ScrapeToWaybackInput{S3Key: "bucket/urls.txt"})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var got ScrapeCounts
	require.NoError(t, env.GetWorkflowResult(&got))
	require.Equal(t, 2, got.URLs)
	require.Equal(t, 0, got.PDFFound)
	require.Equal(t, 1, got.NoPDF)
	require.Equal(t, 1, got.Skipped)
	require.Equal(t, map[string]int{"dead-end": 1, "blocklist": 1}, got.FailReasons)
}

// One URL whose crawl exhausts its retries must not strand the rest of the
// list: it is counted and the run carries on.
func Test_Workflow_AbandonsFailingURLAndContinues(t *testing.T) {
	configureViper(1, 1_000_000)

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	env.OnActivity(ReadURLBatch, mock.Anything, mock.Anything).
		Return(ReadURLBatchOutput{URLs: refs(2, 0), NextOffset: 20, EOF: true}, nil).Once()

	env.OnActivity(CrawlToWaybackActivity, mock.Anything, mock.MatchedBy(func(in CrawlToWaybackInput) bool {
		return in.URL == "http://example.com/0"
	})).Return(CrawlToWaybackOutput{}, errors.New("spn is sad"))
	env.OnActivity(CrawlToWaybackActivity, mock.Anything, mock.MatchedBy(func(in CrawlToWaybackInput) bool {
		return in.URL == "http://example.com/1"
	})).Return(CrawlToWaybackOutput{Success: true}, nil).Once()
	// The abandoned URL still gets a row (see Test_Workflow_LogsAbandonedURLs).
	env.OnActivity(RecordOutcomeActivity, mock.Anything, mock.Anything).Return(nil).Once()

	env.ExecuteWorkflow(ScrapeToWaybackWorkflow, ScrapeToWaybackInput{S3Key: "bucket/urls.txt"})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var got ScrapeCounts
	require.NoError(t, env.GetWorkflowResult(&got))
	require.Equal(t, 2, got.URLs)
	require.Equal(t, 1, got.Errored)
	require.Equal(t, 1, got.PDFFound)
}

// An unbroken streak of failures is systemic, not a bad URL, and stops the run
// rather than burning the whole list against a broken SPN.
func Test_Workflow_AbortsOnConsecutiveErrors(t *testing.T) {
	configureViper(1, 1_000_000)
	viper.Set("scrape.max_consecutive_errors", 3)
	defer viper.Set("scrape.max_consecutive_errors", 50)

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	env.OnActivity(ReadURLBatch, mock.Anything, mock.Anything).
		Return(ReadURLBatchOutput{URLs: refs(10, 0), NextOffset: 100, EOF: true}, nil).Once()
	env.OnActivity(CrawlToWaybackActivity, mock.Anything, mock.Anything).
		Return(CrawlToWaybackOutput{}, errors.New("bad credentials"))
	env.OnActivity(RecordOutcomeActivity, mock.Anything, mock.Anything).Return(nil)

	env.ExecuteWorkflow(ScrapeToWaybackWorkflow, ScrapeToWaybackInput{S3Key: "bucket/urls.txt"})

	require.True(t, env.IsWorkflowCompleted())
	err := env.GetWorkflowError()
	require.Error(t, err)
	require.Contains(t, err.Error(), "consecutive crawl failures")
}

// Hitting urls_per_can hands off to a fresh run rather than growing one run's
// history without bound.
func Test_Workflow_ContinuesAsNewAtThreshold(t *testing.T) {
	configureViper(1, 3)

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	env.OnActivity(ReadURLBatch, mock.Anything, mock.Anything).
		Return(ReadURLBatchOutput{URLs: refs(5, 0), NextOffset: 50, EOF: false}, nil).Once()
	// Only three of the five are dispatched before the handoff.
	env.OnActivity(CrawlToWaybackActivity, mock.Anything, mock.Anything).
		Return(CrawlToWaybackOutput{Success: true}, nil).Times(3)

	env.ExecuteWorkflow(ScrapeToWaybackWorkflow, ScrapeToWaybackInput{S3Key: "bucket/urls.txt"})

	require.True(t, env.IsWorkflowCompleted())
	err := env.GetWorkflowError()
	require.Error(t, err)
	var canErr *workflow.ContinueAsNewError
	require.True(t, errors.As(err, &canErr), "expected ContinueAsNewError, got %v", err)
	env.AssertExpectations(t)
}

// The resume offset is the end of the last URL actually dispatched, so the next
// run neither re-crawls nor skips. refs() spaces URLs 10 bytes apart, so three
// dispatched URLs means resuming at 30 -- not at the batch's NextOffset of 50.
func Test_Workflow_ResumeOffsetIsLastDispatchedURL(t *testing.T) {
	configureViper(1, 3)

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	env.OnActivity(ReadURLBatch, mock.Anything, mock.Anything).
		Return(ReadURLBatchOutput{URLs: refs(5, 0), NextOffset: 50, EOF: false}, nil).Once()
	env.OnActivity(CrawlToWaybackActivity, mock.Anything, mock.Anything).
		Return(CrawlToWaybackOutput{Success: true}, nil).Times(3)

	env.ExecuteWorkflow(ScrapeToWaybackWorkflow, ScrapeToWaybackInput{S3Key: "bucket/urls.txt"})

	var canErr *workflow.ContinueAsNewError
	require.True(t, errors.As(env.GetWorkflowError(), &canErr))

	var next ScrapeToWaybackInput
	require.NoError(t, converter.GetDefaultDataConverter().
		FromPayloads(canErr.Input, &next))
	require.Equal(t, int64(30), next.Offset)
	require.Equal(t, 3, next.Counts.URLs)
}

// A resumed run reuses the carried config and offset instead of re-reading
// viper, which is what keeps a ContinueAsNew chain replay-deterministic.
func Test_Workflow_ResumeUsesCarriedOffset(t *testing.T) {
	configureViper(1, 1_000_000)

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	env.OnActivity(ReadURLBatch, mock.Anything, mock.MatchedBy(func(in ReadURLBatchInput) bool {
		return in.Offset == 500 && in.MaxURLs == 7 && in.ChunkSize == 99
	})).Return(ReadURLBatchOutput{URLs: refs(1, 500), NextOffset: 510, EOF: true}, nil).Once()
	env.OnActivity(CrawlToWaybackActivity, mock.Anything, mock.Anything).
		Return(CrawlToWaybackOutput{Success: true}, nil).Once()

	env.ExecuteWorkflow(ScrapeToWaybackWorkflow, ScrapeToWaybackInput{
		S3Key:                "bucket/urls.txt",
		Offset:               500,
		Concurrency:          1,
		MaxConcurrency:       8,
		URLsPerCAN:           100,
		BatchSize:            7,
		ChunkSize:            99,
		MaxConsecutiveErrors: 5,
		Counts:               ScrapeCounts{URLs: 42, PDFFound: 40},
	})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var got ScrapeCounts
	require.NoError(t, env.GetWorkflowResult(&got))
	require.Equal(t, 43, got.URLs) // 42 carried + 1 new
	require.Equal(t, 41, got.PDFFound)
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

	env.OnActivity(ReadURLBatch, mock.Anything, mock.Anything).
		Return(ReadURLBatchOutput{URLs: refs(2, 0), NextOffset: 20, EOF: true}, nil).Once()
	env.OnActivity(CrawlToWaybackActivity, mock.Anything, mock.Anything).
		Return(CrawlToWaybackOutput{Success: true}, nil).Times(2)

	env.ExecuteWorkflow(ScrapeToWaybackWorkflow, ScrapeToWaybackInput{
		S3Key:       "bucket/urls.txt",
		Concurrency: 10_000,
	})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
}

func Test_WorkflowID(t *testing.T) {
	require.Equal(t, "scrape_to_wayback_bucket_etd_wayback-seeds.txt",
		WorkflowID("bucket/etd/wayback-seeds.txt"))
}

// The sliding window must actually bound in-flight crawls -- that is the whole
// throttle. Mocked returns alone would pass whether the window worked or not,
// so this drives real activity functions that record how many ran at once.
func runWithConcurrency(t *testing.T, concurrency, urlCount int) int32 {
	t.Helper()
	configureViper(concurrency, 1_000_000)

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	var cur, max atomic.Int32
	env.OnActivity(CrawlToWaybackActivity, mock.Anything, mock.Anything).
		Return(func(ctx context.Context, in CrawlToWaybackInput) (CrawlToWaybackOutput, error) {
			n := cur.Add(1)
			for {
				m := max.Load()
				if n <= m || max.CompareAndSwap(m, n) {
					break
				}
			}
			// Long enough that genuinely parallel crawls overlap.
			time.Sleep(50 * time.Millisecond)
			cur.Add(-1)
			return CrawlToWaybackOutput{Success: true}, nil
		})

	env.OnActivity(ReadURLBatch, mock.Anything, mock.Anything).
		Return(ReadURLBatchOutput{
			URLs:       refs(urlCount, 0),
			NextOffset: int64(urlCount) * 10,
			EOF:        true,
		}, nil).Once()

	env.ExecuteWorkflow(ScrapeToWaybackWorkflow, ScrapeToWaybackInput{S3Key: "bucket/urls.txt"})
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var got ScrapeCounts
	require.NoError(t, env.GetWorkflowResult(&got))
	require.Equal(t, urlCount, got.URLs, "every url should still be crawled")
	return max.Load()
}

func Test_Workflow_ConcurrencyIsBounded(t *testing.T) {
	max := runWithConcurrency(t, 3, 9)
	require.LessOrEqual(t, max, int32(3), "window let more than 3 crawls run at once")
	require.Greater(t, max, int32(1), "crawls ran serially; the window is not dispatching")
}

func Test_Workflow_ConcurrencyOneIsSerial(t *testing.T) {
	require.Equal(t, int32(1), runWithConcurrency(t, 1, 4))
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

			env.OnActivity(ReadURLBatch, mock.Anything, mock.Anything).
				Return(ReadURLBatchOutput{URLs: refs(4, 0), NextOffset: 40, EOF: true}, nil).Once()
			env.OnActivity(CrawlToWaybackActivity, mock.Anything, mock.Anything).
				Return(CrawlToWaybackOutput{Success: true}, nil).Times(4)

			env.RegisterDelayedCallback(func() {
				env.SignalWorkflow(SetConcurrencySignal, tc.signal)
			}, 0)

			env.ExecuteWorkflow(ScrapeToWaybackWorkflow, ScrapeToWaybackInput{S3Key: "bucket/urls.txt"})

			require.True(t, env.IsWorkflowCompleted())
			require.NoError(t, env.GetWorkflowError())

			var got ScrapeCounts
			require.NoError(t, env.GetWorkflowResult(&got))
			require.Equal(t, 4, got.URLs, "every url is crawled regardless of the signal")
		})
	}
}

// A URL whose crawl retries run out is the one outcome CrawlToWaybackActivity
// never gets to record, so the workflow must log it or the URL would be missing
// from the log entirely.
func Test_Workflow_LogsAbandonedURLs(t *testing.T) {
	configureViper(1, 1_000_000)

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	env.OnActivity(ReadURLBatch, mock.Anything, mock.Anything).
		Return(ReadURLBatchOutput{URLs: refs(2, 0), NextOffset: 20, EOF: true}, nil).Once()

	env.OnActivity(CrawlToWaybackActivity, mock.Anything, mock.MatchedBy(func(in CrawlToWaybackInput) bool {
		return in.URL == "http://example.com/0"
	})).Return(CrawlToWaybackOutput{}, errors.New("spn is sad"))
	env.OnActivity(CrawlToWaybackActivity, mock.Anything, mock.MatchedBy(func(in CrawlToWaybackInput) bool {
		return in.URL == "http://example.com/1"
	})).Return(CrawlToWaybackOutput{Success: true}, nil).Once()

	// Only the abandoned URL is logged by the workflow; the one that crawled
	// cleanly logged itself inside the activity.
	env.OnActivity(RecordOutcomeActivity, mock.Anything, mock.MatchedBy(func(o Outcome) bool {
		return o.SeedURL == "http://example.com/0" &&
			o.Status == StatusError &&
			o.WaybackURL == "" &&
			strings.Contains(o.Reason, "spn is sad")
	})).Return(nil).Once()

	env.ExecuteWorkflow(ScrapeToWaybackWorkflow, ScrapeToWaybackInput{S3Key: "bucket/urls.txt"})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	env.AssertExpectations(t)
}

// Nothing is abandoned on a clean run, so the workflow must not call the log
// activity at all -- rows for successful crawls come from the crawl activity.
func Test_Workflow_NoLogActivityWhenNothingAbandoned(t *testing.T) {
	configureViper(2, 1_000_000)

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	env.OnActivity(ReadURLBatch, mock.Anything, mock.Anything).
		Return(ReadURLBatchOutput{URLs: refs(3, 0), NextOffset: 30, EOF: true}, nil).Once()
	env.OnActivity(CrawlToWaybackActivity, mock.Anything, mock.Anything).
		Return(CrawlToWaybackOutput{Success: true}, nil).Times(3)

	env.ExecuteWorkflow(ScrapeToWaybackWorkflow, ScrapeToWaybackInput{S3Key: "bucket/urls.txt"})

	require.True(t, env.IsWorkflowCompleted())
	// RecordOutcomeActivity is unmocked, so any call to it would fail the run.
	require.NoError(t, env.GetWorkflowError())
}

// An unwritable log is a broken deployment, not a per-URL hiccup: it stops the
// run rather than quietly dropping the record of what happened.
func Test_Workflow_LogWriteFailureStopsRun(t *testing.T) {
	configureViper(1, 1_000_000)

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	env.OnActivity(ReadURLBatch, mock.Anything, mock.Anything).
		Return(ReadURLBatchOutput{URLs: refs(1, 0), NextOffset: 10, EOF: true}, nil).Once()
	env.OnActivity(CrawlToWaybackActivity, mock.Anything, mock.Anything).
		Return(CrawlToWaybackOutput{}, errors.New("crawl blew up"))
	env.OnActivity(RecordOutcomeActivity, mock.Anything, mock.Anything).
		Return(errors.New("read-only file system"))

	env.ExecuteWorkflow(ScrapeToWaybackWorkflow, ScrapeToWaybackInput{S3Key: "bucket/urls.txt"})

	require.True(t, env.IsWorkflowCompleted())
	require.ErrorContains(t, env.GetWorkflowError(), "read-only file system")
}
