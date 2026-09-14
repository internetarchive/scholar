package scrape

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"git.archive.org/webgroup/scholar/trawler/crawling"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/converter"
)

// --- helpers --------------------------------------------------------------

func batchURLs(n int) []URLRef {
	out := make([]URLRef, 0, n)
	for i := range n {
		out = append(out, URLRef{
			URL: fmt.Sprintf("http://example.com/%d", i),
			End: int64(i+1) * 10,
		})
	}
	return out
}

// recorder stands in for the outcome log and the heartbeat.
type recorder struct {
	mu   sync.Mutex
	rows []Outcome
	cps  []batchCheckpoint
	// failRow makes the write of that seed URL's row fail, standing in for an
	// unwritable log directory.
	failRow string
}

func (r *recorder) record(o Outcome) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if o.SeedURL == r.failRow {
		return errors.New("read-only file system")
	}
	r.rows = append(r.rows, o)
	return nil
}

func (r *recorder) checkpoint(cp batchCheckpoint) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cps = append(r.cps, cp)
}

func (r *recorder) seeds() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.rows))
	for _, o := range r.rows {
		out = append(out, o.SeedURL)
	}
	return out
}

func (r *recorder) checkpoints() []batchCheckpoint {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]batchCheckpoint(nil), r.cps...)
}

func newRunner(n int, crawl crawlFn) (*batchRunner, *recorder) {
	rec := &recorder{}
	return &batchRunner{
		urls:           batchURLs(n),
		parallelism:    4,
		attempts:       1,
		maxConsecutive: 50,
		crawl:          crawl,
		record:         rec.record,
		checkpoint:     rec.checkpoint,
		sleep:          func(context.Context, time.Duration) error { return nil },
		now:            func() time.Time { return time.Unix(0, 0).UTC() },
	}, rec
}

func crawledPDF(context.Context, string) (crawling.CrawlResult, error) {
	return crawling.CrawlResult{Success: true, SnapshotUrl: "http://web/pdf"}, nil
}

// --- the happy path -------------------------------------------------------

func Test_batchRunner_crawlsEveryURL(t *testing.T) {
	r, rec := newRunner(5, crawledPDF)

	out, err := r.run(t.Context(), batchCheckpoint{})
	require.NoError(t, err)

	require.Equal(t, 5, out.DoneThrough)
	require.Equal(t, 5, out.Counts.URLs)
	require.Equal(t, 5, out.Counts.PDFFound)
	require.False(t, out.Aborted)
	require.Equal(t, int64(50), out.NextOffset)

	// Rows come out in list order even though the crawls finish out of order:
	// they are written as the watermark advances, not as each crawl lands.
	require.Equal(t, []string{
		"http://example.com/0", "http://example.com/1", "http://example.com/2",
		"http://example.com/3", "http://example.com/4",
	}, rec.seeds())
}

// Failing to find a PDF is an ordinary outcome, recorded by reason and folded
// into the totals rather than failing anything.
func Test_batchRunner_countsFailReasons(t *testing.T) {
	reasons := map[string]string{
		"http://example.com/0": "dead-end",
		"http://example.com/1": "blocklist",
		"http://example.com/2": "",
	}
	r, _ := newRunner(3, func(_ context.Context, u string) (crawling.CrawlResult, error) {
		return crawling.CrawlResult{FailReason: reasons[u]}, nil
	})

	out, err := r.run(t.Context(), batchCheckpoint{})
	require.NoError(t, err)

	require.Equal(t, 3, out.Counts.URLs)
	require.Equal(t, 0, out.Counts.PDFFound)
	require.Equal(t, 2, out.Counts.NoPDF) // dead-end and unknown
	require.Equal(t, 1, out.Counts.Skipped)
	require.Equal(t, map[string]int{"dead-end": 1, "blocklist": 1, "unknown": 1},
		out.Counts.FailReasons)
}

// The pool is the throttle now that Temporal is not dispatching URLs one at a
// time, so it has to actually bound what hits SPN.
func Test_batchRunner_parallelismIsBounded(t *testing.T) {
	for _, parallelism := range []int{1, 3} {
		t.Run(fmt.Sprintf("p=%d", parallelism), func(t *testing.T) {
			var cur, peak atomic.Int32
			r, _ := newRunner(12, func(context.Context, string) (crawling.CrawlResult, error) {
				n := cur.Add(1)
				for {
					m := peak.Load()
					if n <= m || peak.CompareAndSwap(m, n) {
						break
					}
				}
				// Long enough that genuinely parallel crawls overlap.
				time.Sleep(20 * time.Millisecond)
				cur.Add(-1)
				return crawling.CrawlResult{Success: true}, nil
			})
			r.parallelism = parallelism

			out, err := r.run(t.Context(), batchCheckpoint{})
			require.NoError(t, err)
			require.Equal(t, 12, out.Counts.URLs)
			require.LessOrEqual(t, peak.Load(), int32(parallelism))
			if parallelism > 1 {
				require.Greater(t, peak.Load(), int32(1), "the pool never ran anything in parallel")
			}
		})
	}
}

// --- per-URL retries ------------------------------------------------------

func Test_batchRunner_retriesThenSucceeds(t *testing.T) {
	var attempts atomic.Int32
	r, rec := newRunner(1, func(context.Context, string) (crawling.CrawlResult, error) {
		if attempts.Add(1) < 3 {
			return crawling.CrawlResult{}, errors.New("spn is sad")
		}
		return crawling.CrawlResult{Success: true}, nil
	})
	r.attempts = 4

	out, err := r.run(t.Context(), batchCheckpoint{})
	require.NoError(t, err)
	require.Equal(t, 1, out.Counts.PDFFound)
	require.Zero(t, out.Counts.Errored)
	require.Equal(t, int32(3), attempts.Load())
	require.Equal(t, StatusOK, rec.rows[0].Status)
}

// One URL whose crawl exhausts its retries must not strand the rest of the
// batch: it is counted, logged and stepped over.
func Test_batchRunner_abandonsFailingURLAndContinues(t *testing.T) {
	r, rec := newRunner(3, func(_ context.Context, u string) (crawling.CrawlResult, error) {
		if u == "http://example.com/1" {
			return crawling.CrawlResult{}, errors.New("spn is sad")
		}
		return crawling.CrawlResult{Success: true}, nil
	})
	r.attempts = 2

	out, err := r.run(t.Context(), batchCheckpoint{})
	require.NoError(t, err)

	require.Equal(t, 3, out.Counts.URLs)
	require.Equal(t, 2, out.Counts.PDFFound)
	require.Equal(t, 1, out.Counts.Errored)
	require.False(t, out.Aborted)

	// The abandoned URL still gets a row -- it is the one outcome that would
	// otherwise be missing from the log entirely.
	require.Len(t, rec.rows, 3)
	require.Equal(t, StatusError, rec.rows[1].Status)
	require.Contains(t, rec.rows[1].Reason, "spn is sad")
	require.Empty(t, rec.rows[1].WaybackURL)
}

func Test_crawlBackoff_growsAndCaps(t *testing.T) {
	require.Equal(t, urlRetryInitial, crawlBackoff(1))
	require.Equal(t, 2*urlRetryInitial, crawlBackoff(2))
	require.Equal(t, urlRetryMax, crawlBackoff(20))
}

// --- the circuit breaker --------------------------------------------------

// An unbroken streak of failures is systemic, not a bad URL, and stops the run
// rather than burning the rest of the list against a broken SPN.
func Test_batchRunner_abortsOnConsecutiveErrors(t *testing.T) {
	var crawls atomic.Int32
	r, _ := newRunner(20, func(context.Context, string) (crawling.CrawlResult, error) {
		crawls.Add(1)
		return crawling.CrawlResult{}, errors.New("bad credentials")
	})
	r.parallelism = 1
	r.maxConsecutive = 3

	out, err := r.run(t.Context(), batchCheckpoint{})
	// Aborting is a verdict, not a failure: everything the batch did finish is
	// reported, and the workflow is the one that stops the run.
	require.NoError(t, err)
	require.True(t, out.Aborted)
	require.Contains(t, out.AbortReason, "3 consecutive")
	require.Equal(t, 3, out.DoneThrough)
	require.Equal(t, int64(30), out.NextOffset, "an aborted batch says where to resume")
	require.Less(t, crawls.Load(), int32(20), "the breaker did not stop the batch")
}

// A streak that began in an earlier attempt still counts, or a batch that keeps
// being retried could never trip the breaker.
func Test_batchRunner_carriesErrorStreakAcrossResume(t *testing.T) {
	r, _ := newRunner(10, func(context.Context, string) (crawling.CrawlResult, error) {
		return crawling.CrawlResult{}, errors.New("bad credentials")
	})
	r.parallelism = 1
	r.maxConsecutive = 5

	out, err := r.run(t.Context(), batchCheckpoint{
		DoneThrough: 4,
		NextOffset:  40,
		Consecutive: 4,
		Counts:      ScrapeCounts{URLs: 4, Errored: 4},
	})
	require.NoError(t, err)
	require.True(t, out.Aborted)
	require.Equal(t, 5, out.DoneThrough, "one more failure should have tripped it")
}

// A batch too short to reach the streak threshold can still be saying the same
// thing.
func Test_batchRunner_abortsWhenEveryURLFailed(t *testing.T) {
	r, _ := newRunner(3, func(context.Context, string) (crawling.CrawlResult, error) {
		return crawling.CrawlResult{}, errors.New("bad credentials")
	})
	r.maxConsecutive = 50

	out, err := r.run(t.Context(), batchCheckpoint{})
	require.NoError(t, err)
	require.True(t, out.Aborted)
	require.Contains(t, out.AbortReason, "all 3 urls")
}

// A batch that merely contains dead URLs is not systemic and must not abort.
func Test_batchRunner_mixedFailuresDoNotAbort(t *testing.T) {
	r, _ := newRunner(4, func(_ context.Context, u string) (crawling.CrawlResult, error) {
		if u == "http://example.com/2" {
			return crawling.CrawlResult{Success: true}, nil
		}
		return crawling.CrawlResult{}, errors.New("dead host")
	})
	r.maxConsecutive = 50

	out, err := r.run(t.Context(), batchCheckpoint{})
	require.NoError(t, err)
	require.False(t, out.Aborted)
	require.Equal(t, 3, out.Counts.Errored)
}

// --- checkpointing --------------------------------------------------------

// The checkpoint is what a retry resumes from, so it may only ever describe the
// contiguous prefix. A URL that finished out of order is deliberately left out:
// counting it would count it twice once the retry re-crawled it.
func Test_batchRunner_checkpointOnlyCoversTheContiguousPrefix(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 5)

	r, rec := newRunner(5, func(_ context.Context, u string) (crawling.CrawlResult, error) {
		started <- struct{}{}
		if u == "http://example.com/0" {
			<-release
		}
		return crawling.CrawlResult{Success: true}, nil
	})
	r.parallelism = 5

	done := make(chan CrawlBatchOutput, 1)
	go func() {
		out, err := r.run(t.Context(), batchCheckpoint{})
		require.NoError(t, err)
		done <- out
	}()

	// Wait for all five to be in flight, then let the four behind the blocked
	// one land.
	for range 5 {
		<-started
	}
	require.Eventually(t, func() bool {
		return len(rec.checkpoints()) >= 4
	}, 2*time.Second, 5*time.Millisecond)

	for _, cp := range rec.checkpoints() {
		require.Zero(t, cp.DoneThrough, "the watermark moved past an unfinished url")
		require.Zero(t, cp.Counts.URLs)
	}

	close(release)
	out := <-done
	require.Equal(t, 5, out.DoneThrough)
	require.Equal(t, 5, out.Counts.URLs)

	// And the rows still came out in list order.
	require.Equal(t, "http://example.com/0", rec.seeds()[0])
}

// Resuming skips what the checkpoint already accounted for and keeps its tally,
// which is what makes a redelivered batch cost only the work it had not done.
func Test_batchRunner_resumesFromCheckpoint(t *testing.T) {
	var crawled sync.Map
	r, rec := newRunner(5, func(_ context.Context, u string) (crawling.CrawlResult, error) {
		crawled.Store(u, true)
		return crawling.CrawlResult{Success: true}, nil
	})

	out, err := r.run(t.Context(), batchCheckpoint{
		DoneThrough: 3,
		NextOffset:  30,
		Counts:      ScrapeCounts{URLs: 3, PDFFound: 2, NoPDF: 1},
	})
	require.NoError(t, err)

	require.Equal(t, 5, out.DoneThrough)
	require.Equal(t, 5, out.Counts.URLs, "carried counts plus the two it had left")
	require.Equal(t, 4, out.Counts.PDFFound)
	require.Equal(t, 1, out.Counts.NoPDF)

	require.Equal(t, []string{"http://example.com/3", "http://example.com/4"}, rec.seeds())
	_, redone := crawled.Load("http://example.com/0")
	require.False(t, redone, "a url below the watermark was crawled again")
}

// A checkpoint that already covers the batch means there is nothing to do.
func Test_batchRunner_resumeAtEndDoesNothing(t *testing.T) {
	r, rec := newRunner(3, func(context.Context, string) (crawling.CrawlResult, error) {
		t.Fatal("crawled a url the checkpoint had already accounted for")
		return crawling.CrawlResult{}, nil
	})

	out, err := r.run(t.Context(), batchCheckpoint{
		DoneThrough: 3, NextOffset: 30, Counts: ScrapeCounts{URLs: 3, PDFFound: 3},
	})
	require.NoError(t, err)
	require.Equal(t, 3, out.DoneThrough)
	require.Equal(t, 3, out.Counts.PDFFound)
	require.Empty(t, rec.rows)
}

// --- stopping short -------------------------------------------------------

// A batch that did not finish must report an error. Returning successfully
// would tell the workflow the whole range was crawled and move the run past
// URLs nobody looked at.
func Test_batchRunner_workerStopIsAPartialFailure(t *testing.T) {
	stopped := make(chan struct{})
	var crawls atomic.Int32

	r, rec := newRunner(10, func(context.Context, string) (crawling.CrawlResult, error) {
		if crawls.Add(1) == 2 {
			close(stopped)
		}
		return crawling.CrawlResult{Success: true}, nil
	})
	r.parallelism = 1
	r.stopped = stopped

	out, err := r.run(t.Context(), batchCheckpoint{})
	require.ErrorContains(t, err, "stopped after")

	// Whatever it did finish is still counted, logged and checkpointed, so the
	// retry picks up there rather than at the top of the batch.
	require.Greater(t, out.DoneThrough, 0)
	require.Less(t, out.DoneThrough, 10)
	require.Equal(t, out.DoneThrough, out.Counts.URLs)
	require.Len(t, rec.rows, out.DoneThrough)
	require.Equal(t, int64(out.DoneThrough*10), out.NextOffset)
}

// A cancelled context is not a verdict on the URL being crawled: it gets no row
// and no place in the tally, so the retry crawls it again.
func Test_batchRunner_cancelledURLGetsNoVerdict(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	r, rec := newRunner(3, func(_ context.Context, u string) (crawling.CrawlResult, error) {
		if u == "http://example.com/1" {
			cancel()
			return crawling.CrawlResult{}, errors.New("context canceled")
		}
		return crawling.CrawlResult{Success: true}, nil
	})
	r.parallelism = 1

	out, err := r.run(ctx, batchCheckpoint{})
	require.ErrorContains(t, err, "stopped after 1 of 3")
	require.Equal(t, 1, out.DoneThrough)
	require.Equal(t, 1, out.Counts.URLs)
	require.Zero(t, out.Counts.Errored, "a cancelled url was counted as a failure")
	require.Equal(t, []string{"http://example.com/0"}, rec.seeds())
}

// An unwritable log is a broken deployment, not a per-URL hiccup: it stops the
// batch rather than quietly dropping the record of what happened. The watermark
// stays below the row that could not be written, so the retry rewrites it
// rather than skipping it.
func Test_batchRunner_logWriteFailureStopsTheBatch(t *testing.T) {
	r, rec := newRunner(5, crawledPDF)
	r.parallelism = 1
	rec.failRow = "http://example.com/2"

	out, err := r.run(t.Context(), batchCheckpoint{})
	require.ErrorContains(t, err, "read-only file system")
	require.Equal(t, 2, out.DoneThrough)
	require.Equal(t, int64(20), out.NextOffset)
	require.Equal(t, []string{"http://example.com/0", "http://example.com/1"}, rec.seeds())
}

// --- counts ---------------------------------------------------------------

func Test_countsFor(t *testing.T) {
	pdf := countsFor(crawling.CrawlResult{Success: true})
	require.Equal(t, 1, pdf.URLs)
	require.Equal(t, 1, pdf.PDFFound)
	require.Empty(t, pdf.FailReasons)

	// A dead end still means SPN captured a page, so it is NoPDF, not Skipped.
	deadEnd := countsFor(crawling.CrawlResult{FailReason: "dead-end"})
	require.Equal(t, 1, deadEnd.NoPDF)
	require.Equal(t, 0, deadEnd.Skipped)
	require.Equal(t, map[string]int{"dead-end": 1}, deadEnd.FailReasons)

	// Blocklisted URLs never reached the network at all.
	blocked := countsFor(crawling.CrawlResult{FailReason: "blocklist"})
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

	// Add must not touch either operand: a batch snapshots its running counts
	// into a heartbeat and keeps counting, and a shared map would let a later
	// URL mutate an already-published checkpoint.
	require.Equal(t, map[string]int{"dead-end": 1}, a.FailReasons)
	require.Equal(t, 2, a.URLs)

	// Adding empties must not allocate a map that then serialises as {}.
	require.Nil(t, ScrapeCounts{}.Add(ScrapeCounts{}).FailReasons)
}

// The checkpoint only does its job if it survives the trip to Temporal and
// back: it is recorded as a heartbeat detail and read again by whichever worker
// picks the batch up next, possibly on another host. An unexported type with a
// map in it is exactly the shape that quietly stops round-tripping.
func Test_batchCheckpoint_survivesTheDataConverter(t *testing.T) {
	want := batchCheckpoint{
		DoneThrough: 137,
		NextOffset:  4096,
		Consecutive: 3,
		Counts: ScrapeCounts{
			URLs: 137, PDFFound: 90, NoPDF: 40, Skipped: 4, Errored: 3,
			FailReasons: map[string]int{"dead-end": 40, "blocklist": 4},
		},
	}

	dc := converter.GetDefaultDataConverter()
	payloads, err := dc.ToPayloads(want)
	require.NoError(t, err)

	var got batchCheckpoint
	require.NoError(t, dc.FromPayloads(payloads, &got))
	require.Equal(t, want, got)
}
