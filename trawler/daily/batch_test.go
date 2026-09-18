package daily

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"git.archive.org/webgroup/scholar/trawler/counts"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/converter"
)

// --- helpers --------------------------------------------------------------

func batchLines(n int) []LineRef {
	out := make([]LineRef, 0, n)
	for i := range n {
		out = append(out, LineRef{
			JSON: fmt.Sprintf(`{"DOI":"10.1234/x%d"}`, i),
			End:  int64(i+1) * 10,
		})
	}
	return out
}

func doi(i int) string { return fmt.Sprintf("doi:10.1234/x%d", i) }

// recorder stands in for the outcome log and the heartbeat.
type recorder struct {
	mu   sync.Mutex
	rows []Outcome
	cps  []batchCheckpoint
	// failRow makes the write of that ext id's row fail, standing in for an
	// unwritable log directory.
	failRow string
}

func (r *recorder) record(o Outcome) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Guarded on failRow being set: plenty of outcomes legitimately carry no
	// ext id, and an unset failRow must not match all of them.
	if r.failRow != "" && o.ExtID == r.failRow {
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

func (r *recorder) extIDs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.rows))
	for _, o := range r.rows {
		out = append(out, o.ExtID)
	}
	return out
}

func (r *recorder) checkpoints() []batchCheckpoint {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]batchCheckpoint(nil), r.cps...)
}

func newRunner(n int, process processFn) (*batchRunner, *recorder) {
	rec := &recorder{}
	return &batchRunner{
		lines:          batchLines(n),
		parallelism:    4,
		attempts:       1,
		maxConsecutive: 50,
		process:        process,
		record:         rec.record,
		checkpoint:     rec.checkpoint,
		sleep:          func(context.Context, time.Duration) error { return nil },
		now:            func() time.Time { return time.Unix(0, 0).UTC() },
	}, rec
}

// lineIndex pulls the record number back out of a line, so a stub can behave
// differently per line without the test having to reconstruct the JSON.
func lineIndex(jsonLine string) int {
	var i int
	fmt.Sscanf(jsonLine, `{"DOI":"10.1234/x%d"}`, &i)
	return i
}

// ingestedLine is the stub for a line that made it all the way through.
func ingestedLine(_ context.Context, jsonLine string) (DailyCounts, Outcome, error) {
	i := lineIndex(jsonLine)
	return DailyCounts{
			Counts: counts.Counts{Releases: counts.ReleaseCounts{
				CrawlWanted: 1, Acquired: 1, Ingested: 1,
			}},
			Lines: 1,
		}, Outcome{
			Upstream:   "crossref",
			ExtID:      doi(i),
			Status:     StatusOK,
			Detail:     "created file, indexed fulltext",
			WaybackURL: "http://web/pdf",
		}, nil
}

// --- the happy path -------------------------------------------------------

func Test_batchRunner_worksEveryLine(t *testing.T) {
	r, rec := newRunner(5, ingestedLine)

	out, err := r.run(t.Context(), batchCheckpoint{})
	require.NoError(t, err)

	require.Equal(t, 5, out.DoneThrough)
	require.Equal(t, 5, out.Counts.Lines)
	require.Equal(t, 5, out.Counts.Releases.Ingested)
	require.False(t, out.Aborted)
	require.Equal(t, int64(50), out.NextOffset)

	// Rows come out in file order even though the lines finish out of order:
	// they are written as the watermark advances, not as each line lands.
	require.Equal(t, []string{doi(0), doi(1), doi(2), doi(3), doi(4)}, rec.extIDs())
}

// Skips are the common case for a day of upstream metadata and are folded in
// like any other verdict rather than treated as failures.
func Test_batchRunner_countsSkipsAndFails(t *testing.T) {
	r, _ := newRunner(3, func(_ context.Context, jsonLine string) (DailyCounts, Outcome, error) {
		switch lineIndex(jsonLine) {
		case 0:
			return DailyCounts{
					Counts: counts.Counts{Releases: counts.ReleaseCounts{Skipped: 1}},
					Lines:  1,
				},
				Outcome{Status: StatusSkip, Detail: "not-paperlike"}, nil
		case 1:
			return DailyCounts{
					Counts: counts.Counts{
						Releases: counts.ReleaseCounts{CrawlWanted: 1},
						Pdfs:     counts.PdfCounts{Failed: 1},
					},
					Lines: 1,
				},
				Outcome{Status: StatusFail, Detail: "no pdf: dead-end"}, nil
		}
		return ingestedLine(t.Context(), jsonLine)
	})

	out, err := r.run(t.Context(), batchCheckpoint{})
	require.NoError(t, err)

	require.Equal(t, 3, out.Counts.Lines)
	require.Zero(t, out.Counts.Errored)
	require.Equal(t, 1, out.Counts.Releases.Skipped)
	require.Equal(t, 1, out.Counts.Pdfs.Failed)
	require.Equal(t, 1, out.Counts.Releases.Ingested)
	require.False(t, out.Aborted, "skips and dead ends are not systemic")
}

// The pool is the throttle now that Temporal is not dispatching lines one at a
// time, so it has to actually bound what hits SPN and GROBID.
func Test_batchRunner_parallelismIsBounded(t *testing.T) {
	for _, parallelism := range []int{1, 3} {
		t.Run(fmt.Sprintf("p=%d", parallelism), func(t *testing.T) {
			var cur, peak atomic.Int32
			r, _ := newRunner(12, func(ctx context.Context, jsonLine string) (DailyCounts, Outcome, error) {
				n := cur.Add(1)
				for {
					m := peak.Load()
					if n <= m || peak.CompareAndSwap(m, n) {
						break
					}
				}
				// Long enough that genuinely parallel lines overlap.
				time.Sleep(20 * time.Millisecond)
				cur.Add(-1)
				return ingestedLine(ctx, jsonLine)
			})
			r.parallelism = parallelism

			out, err := r.run(t.Context(), batchCheckpoint{})
			require.NoError(t, err)
			require.Equal(t, 12, out.Counts.Lines)
			require.LessOrEqual(t, peak.Load(), int32(parallelism))
			if parallelism > 1 {
				require.Greater(t, peak.Load(), int32(1), "the pool never ran anything in parallel")
			}
		})
	}
}

// --- per-line retries -----------------------------------------------------

func Test_batchRunner_retriesThenSucceeds(t *testing.T) {
	var attempts atomic.Int32
	r, rec := newRunner(1, func(ctx context.Context, jsonLine string) (DailyCounts, Outcome, error) {
		if attempts.Add(1) < 3 {
			return DailyCounts{}, Outcome{ExtID: doi(0)}, errors.New("fatcat is sad")
		}
		return ingestedLine(ctx, jsonLine)
	})
	r.attempts = 4

	out, err := r.run(t.Context(), batchCheckpoint{})
	require.NoError(t, err)
	require.Equal(t, 1, out.Counts.Releases.Ingested)
	require.Zero(t, out.Counts.Errored)
	require.Equal(t, int32(3), attempts.Load())
	require.Equal(t, StatusOK, rec.rows[0].Status)
}

// One line whose processing exhausts its retries must not strand the rest of
// the batch: it is counted, logged and stepped over.
func Test_batchRunner_abandonsFailingLineAndContinues(t *testing.T) {
	r, rec := newRunner(3, func(ctx context.Context, jsonLine string) (DailyCounts, Outcome, error) {
		if lineIndex(jsonLine) == 1 {
			// A failed attempt still identifies what it was working on, which
			// is what makes the error row usable.
			return DailyCounts{}, Outcome{Upstream: "crossref", ExtID: doi(1)},
				errors.New("fatcat is sad")
		}
		return ingestedLine(ctx, jsonLine)
	})
	r.attempts = 2

	out, err := r.run(t.Context(), batchCheckpoint{})
	require.NoError(t, err)

	require.Equal(t, 3, out.Counts.Lines)
	require.Equal(t, 2, out.Counts.Releases.Ingested)
	require.Equal(t, 1, out.Counts.Errored)
	require.False(t, out.Aborted)

	// The abandoned line still gets a row -- it is the one outcome that would
	// otherwise be missing from the log entirely.
	require.Len(t, rec.rows, 3)
	require.Equal(t, StatusError, rec.rows[1].Status)
	require.Equal(t, doi(1), rec.rows[1].ExtID)
	require.Contains(t, rec.rows[1].Detail, "fatcat is sad")
}

func Test_lineBackoff_growsAndCaps(t *testing.T) {
	require.Equal(t, lineRetryInitial, lineBackoff(1))
	require.Equal(t, 2*lineRetryInitial, lineBackoff(2))
	require.Equal(t, lineRetryMax, lineBackoff(20))
}

// --- the circuit breaker --------------------------------------------------

// An unbroken streak of failures is systemic, not a bad record, and stops the
// run rather than burning the rest of the day against a broken fatcat.
func Test_batchRunner_abortsOnConsecutiveErrors(t *testing.T) {
	var tried atomic.Int32
	r, _ := newRunner(20, func(context.Context, string) (DailyCounts, Outcome, error) {
		tried.Add(1)
		return DailyCounts{}, Outcome{}, errors.New("connection refused")
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
	require.Less(t, tried.Load(), int32(20), "the breaker did not stop the batch")
}

// A streak that began in an earlier attempt still counts, or a batch that keeps
// being retried could never trip the breaker.
func Test_batchRunner_carriesErrorStreakAcrossResume(t *testing.T) {
	r, _ := newRunner(10, func(context.Context, string) (DailyCounts, Outcome, error) {
		return DailyCounts{}, Outcome{}, errors.New("connection refused")
	})
	r.parallelism = 1
	r.maxConsecutive = 5

	out, err := r.run(t.Context(), batchCheckpoint{
		DoneThrough: 4,
		NextOffset:  40,
		Consecutive: 4,
		Counts:      DailyCounts{Lines: 4, Errored: 4},
	})
	require.NoError(t, err)
	require.True(t, out.Aborted)
	require.Equal(t, 5, out.DoneThrough, "one more failure should have tripped it")
}

// A batch too short to reach the streak threshold can still be saying the same
// thing.
func Test_batchRunner_abortsWhenEveryLineFailed(t *testing.T) {
	r, _ := newRunner(3, func(context.Context, string) (DailyCounts, Outcome, error) {
		return DailyCounts{}, Outcome{}, errors.New("connection refused")
	})
	r.maxConsecutive = 50

	out, err := r.run(t.Context(), batchCheckpoint{})
	require.NoError(t, err)
	require.True(t, out.Aborted)
	require.Contains(t, out.AbortReason, "all 3 lines")
}

// A batch that merely contains bad records is not systemic and must not abort.
func Test_batchRunner_mixedFailuresDoNotAbort(t *testing.T) {
	r, _ := newRunner(4, func(ctx context.Context, jsonLine string) (DailyCounts, Outcome, error) {
		if lineIndex(jsonLine) == 2 {
			return ingestedLine(ctx, jsonLine)
		}
		return DailyCounts{}, Outcome{}, errors.New("malformed record")
	})
	r.maxConsecutive = 50

	out, err := r.run(t.Context(), batchCheckpoint{})
	require.NoError(t, err)
	require.False(t, out.Aborted)
	require.Equal(t, 3, out.Counts.Errored)
}

// --- checkpointing --------------------------------------------------------

// The checkpoint is what a retry resumes from, so it may only ever describe the
// contiguous prefix. A line that finished out of order is deliberately left
// out: counting it would count it twice once the retry re-worked it.
func Test_batchRunner_checkpointOnlyCoversTheContiguousPrefix(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 5)

	r, rec := newRunner(5, func(ctx context.Context, jsonLine string) (DailyCounts, Outcome, error) {
		started <- struct{}{}
		if lineIndex(jsonLine) == 0 {
			<-release
		}
		return ingestedLine(ctx, jsonLine)
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
		require.Zero(t, cp.DoneThrough, "the watermark moved past an unfinished line")
		require.Zero(t, cp.Counts.Lines)
	}

	close(release)
	out := <-done
	require.Equal(t, 5, out.DoneThrough)
	require.Equal(t, 5, out.Counts.Lines)

	// And the rows still came out in file order.
	require.Equal(t, doi(0), rec.extIDs()[0])
}

// Resuming skips what the checkpoint already accounted for and keeps its
// tally, which is what makes a redelivered batch cost only the work it had not
// done.
func Test_batchRunner_resumesFromCheckpoint(t *testing.T) {
	var worked sync.Map
	r, rec := newRunner(5, func(ctx context.Context, jsonLine string) (DailyCounts, Outcome, error) {
		worked.Store(lineIndex(jsonLine), true)
		return ingestedLine(ctx, jsonLine)
	})

	out, err := r.run(t.Context(), batchCheckpoint{
		DoneThrough: 3,
		NextOffset:  30,
		Counts: DailyCounts{
			Counts: counts.Counts{Releases: counts.ReleaseCounts{Ingested: 2, Skipped: 1}},
			Lines:  3,
		},
	})
	require.NoError(t, err)

	require.Equal(t, 5, out.DoneThrough)
	require.Equal(t, 5, out.Counts.Lines, "carried counts plus the two it had left")
	require.Equal(t, 4, out.Counts.Releases.Ingested)
	require.Equal(t, 1, out.Counts.Releases.Skipped)

	require.Equal(t, []string{doi(3), doi(4)}, rec.extIDs())
	_, redone := worked.Load(0)
	require.False(t, redone, "a line below the watermark was worked again")
}

// A checkpoint that already covers the batch means there is nothing to do.
func Test_batchRunner_resumeAtEndDoesNothing(t *testing.T) {
	r, rec := newRunner(3, func(context.Context, string) (DailyCounts, Outcome, error) {
		t.Fatal("worked a line the checkpoint had already accounted for")
		return DailyCounts{}, Outcome{}, nil
	})

	out, err := r.run(t.Context(), batchCheckpoint{
		DoneThrough: 3, NextOffset: 30, Counts: DailyCounts{Lines: 3},
	})
	require.NoError(t, err)
	require.Equal(t, 3, out.DoneThrough)
	require.Equal(t, 3, out.Counts.Lines)
	require.Empty(t, rec.rows)
}

// --- stopping short -------------------------------------------------------

// A batch that did not finish must report an error. Returning successfully
// would tell the workflow the whole range was worked and move the run past
// records nobody looked at.
func Test_batchRunner_workerStopIsAPartialFailure(t *testing.T) {
	stopped := make(chan struct{})
	var seen atomic.Int32

	r, rec := newRunner(10, func(ctx context.Context, jsonLine string) (DailyCounts, Outcome, error) {
		if seen.Add(1) == 2 {
			close(stopped)
		}
		return ingestedLine(ctx, jsonLine)
	})
	r.parallelism = 1
	r.stopped = stopped

	out, err := r.run(t.Context(), batchCheckpoint{})
	require.ErrorContains(t, err, "stopped after")

	// Whatever it did finish is still counted, logged and checkpointed, so the
	// retry picks up there rather than at the top of the batch.
	require.Greater(t, out.DoneThrough, 0)
	require.Less(t, out.DoneThrough, 10)
	require.Equal(t, out.DoneThrough, out.Counts.Lines)
	require.Len(t, rec.rows, out.DoneThrough)
	require.Equal(t, int64(out.DoneThrough*10), out.NextOffset)
}

// A cancelled context is not a verdict on the line being worked: it gets no
// row and no place in the tally, so the retry works it again.
func Test_batchRunner_cancelledLineGetsNoVerdict(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	r, rec := newRunner(3, func(c context.Context, jsonLine string) (DailyCounts, Outcome, error) {
		if lineIndex(jsonLine) == 1 {
			cancel()
			return DailyCounts{}, Outcome{}, errors.New("context canceled")
		}
		return ingestedLine(c, jsonLine)
	})
	r.parallelism = 1

	out, err := r.run(ctx, batchCheckpoint{})
	require.ErrorContains(t, err, "stopped after 1 of 3")
	require.Equal(t, 1, out.DoneThrough)
	require.Equal(t, 1, out.Counts.Lines)
	require.Zero(t, out.Counts.Errored, "a cancelled line was counted as a failure")
	require.Equal(t, []string{doi(0)}, rec.extIDs())
}

// An unwritable log is a broken deployment, not a per-line hiccup: it stops the
// batch rather than quietly dropping the record of what happened. The watermark
// stays below the row that could not be written, so the retry rewrites it
// rather than skipping it.
func Test_batchRunner_logWriteFailureStopsTheBatch(t *testing.T) {
	r, rec := newRunner(5, ingestedLine)
	r.parallelism = 1
	rec.failRow = doi(2)

	out, err := r.run(t.Context(), batchCheckpoint{})
	require.ErrorContains(t, err, "read-only file system")
	require.Equal(t, 2, out.DoneThrough)
	require.Equal(t, int64(20), out.NextOffset)
	require.Equal(t, []string{doi(0), doi(1)}, rec.extIDs())
}

// --- counts ---------------------------------------------------------------

func Test_DailyCountsAdd(t *testing.T) {
	a := DailyCounts{
		Counts: counts.Counts{Releases: counts.ReleaseCounts{Added: 2, Ingested: 1}},
		Lines:  3, Errored: 1,
	}
	b := DailyCounts{
		Counts: counts.Counts{
			Releases: counts.ReleaseCounts{Added: 1},
			Pdfs:     counts.PdfCounts{Failed: 2},
		},
		Lines: 4,
	}
	got := a.Add(b)
	require.Equal(t, 7, got.Lines)
	require.Equal(t, 1, got.Errored)
	require.Equal(t, 3, got.Releases.Added)
	require.Equal(t, 1, got.Releases.Ingested)
	require.Equal(t, 2, got.Pdfs.Failed)

	// Add must not touch either operand: a batch snapshots its running counts
	// into a heartbeat and keeps counting.
	require.Equal(t, 3, a.Lines)
	require.Equal(t, 2, a.Releases.Added)
}

// The checkpoint only does its job if it survives the trip to Temporal and
// back: it is recorded as a heartbeat detail and read again by whichever worker
// picks the batch up next, possibly on another host. An unexported type with an
// embedded struct in it is exactly the shape that quietly stops round-tripping.
func Test_batchCheckpoint_survivesTheDataConverter(t *testing.T) {
	want := batchCheckpoint{
		DoneThrough: 137,
		NextOffset:  4096,
		Consecutive: 3,
		Counts: DailyCounts{
			Counts: counts.Counts{
				Releases:   counts.ReleaseCounts{Skipped: 90, CrawlWanted: 44, Acquired: 20, Ingested: 18},
				Containers: counts.ContainerCounts{Added: 2},
				Pdfs:       counts.PdfCounts{Failed: 4},
			},
			Lines:   137,
			Errored: 3,
		},
	}

	dc := converter.GetDefaultDataConverter()
	payloads, err := dc.ToPayloads(want)
	require.NoError(t, err)

	var got batchCheckpoint
	require.NoError(t, dc.FromPayloads(payloads, &got))
	require.Equal(t, want, got)
}

// A heartbeat published from inside a crawl carries a Phase the checkpoint type
// knows nothing about. A resume decodes it as a plain checkpoint, so the extra
// field has to be ignored rather than break the decode -- otherwise every
// mid-crawl beat would be an unreadable checkpoint and every retry would redo
// its batch from the top.
func Test_batchHeartbeat_decodesAsACheckpoint(t *testing.T) {
	beat := batchHeartbeat{
		batchCheckpoint: batchCheckpoint{DoneThrough: 12, NextOffset: 120, Counts: DailyCounts{Lines: 12}},
		Phase:           "crawl http://example.org/paper.pdf",
	}

	dc := converter.GetDefaultDataConverter()
	payloads, err := dc.ToPayloads(beat)
	require.NoError(t, err)

	var got batchCheckpoint
	require.NoError(t, dc.FromPayloads(payloads, &got))
	require.Equal(t, beat.batchCheckpoint, got)
}

// --- upstream dispatch ----------------------------------------------------

// A typo in --upstream must fail the batch outright rather than panic a worker
// mid-run, which is what the old switch's default did.
func Test_upstreamProcessor(t *testing.T) {
	for _, name := range []string{"arxiv", "crossref", "datacite", "doaj", "pubmed"} {
		fn, err := upstreamProcessor(name)
		require.NoError(t, err)
		require.NotNil(t, fn)
	}
	_, err := upstreamProcessor("crossrefff")
	require.ErrorContains(t, err, "unknown upstream")
}
