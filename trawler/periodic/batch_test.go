package periodic

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// --- helpers --------------------------------------------------------------

// batchLines builds n rows ten bytes apart, so the byte arithmetic in the
// assertions stays readable. The JSON is opaque to the runner -- parsing it is
// ingestLine's job -- so it only has to be distinguishable.
func batchLines(n int) []LineRef {
	out := make([]LineRef, 0, n)
	for i := range n {
		out = append(out, LineRef{
			JSON: fmt.Sprintf("row%06d", i),
			End:  int64(i+1) * 10,
		})
	}
	return out
}

// recorder stands in for the outcome log and the heartbeat.
type recorder struct {
	mu   sync.Mutex
	rows []Outcome
	cps  []batchCheckpoint
	// failRow makes the write of that row's outcome fail, standing in for an
	// unwritable log directory.
	failRow string
}

func (r *recorder) record(o Outcome) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if o.URL == r.failRow {
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

func (r *recorder) logged() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.rows))
	for _, o := range r.rows {
		out = append(out, o.URL)
	}
	return out
}

func (r *recorder) checkpoints() []batchCheckpoint {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]batchCheckpoint(nil), r.cps...)
}

func newRunner(n int, ingest ingestFn) (*batchRunner, *recorder) {
	rec := &recorder{}
	return &batchRunner{
		lines:          batchLines(n),
		parallelism:    4,
		attempts:       1,
		maxConsecutive: 50,
		ingest:         ingest,
		record:         rec.record,
		checkpoint:     rec.checkpoint,
		sleep:          func(context.Context, time.Duration) error { return nil },
		now:            time.Now,
		log:            slog.Default(),
	}, rec
}

// skipAll is the common case in real life: a row we look at and pass on.
func skipAll(_ context.Context, line string) (PeriodicCounts, Outcome, error) {
	return PeriodicCounts{Lines: 1, PdfsSkipped: 1},
		Outcome{URL: line, Status: StatusSkip, Detail: "already in fatcat and both indices"},
		nil
}

func lineNames(n int) []string {
	out := make([]string, 0, n)
	for i := range n {
		out = append(out, fmt.Sprintf("row%06d", i))
	}
	return out
}

// --- ordering and counts --------------------------------------------------

func Test_batchRunner_happyPath(t *testing.T) {
	b, rec := newRunner(20, func(_ context.Context, line string) (PeriodicCounts, Outcome, error) {
		return PeriodicCounts{Lines: 1, PdfsAddedToDB: 1, PdfsAddedToES: 1, FulltextAddedToES: 1},
			Outcome{URL: line, Status: StatusOK, Detail: "created file"},
			nil
	})

	out, err := b.run(context.Background(), batchCheckpoint{})
	require.NoError(t, err)
	require.False(t, out.Aborted)
	require.Equal(t, 20, out.DoneThrough)
	require.Equal(t, int64(200), out.NextOffset)
	require.Equal(t, 20, out.Counts.Lines)
	require.Equal(t, 20, out.Counts.PdfsAddedToDB)
	require.Equal(t, 20, out.Counts.FulltextAddedToES)
	require.Equal(t, lineNames(20), rec.logged())
}

// Rows finish out of order -- a PDF that needs GROBID takes a minute, one
// already in the index takes a lookup -- but the log must still come out in CDX
// order, because that is what makes the file line up with the staged list.
func Test_batchRunner_logsInCDXOrderDespiteOutOfOrderCompletion(t *testing.T) {
	b, rec := newRunner(12, func(_ context.Context, line string) (PeriodicCounts, Outcome, error) {
		// The later a row is in the batch, the faster it finishes.
		var n int
		fmt.Sscanf(line, "row%06d", &n)
		time.Sleep(time.Duration(12-n) * 2 * time.Millisecond)
		return skipAll(context.Background(), line)
	})

	out, err := b.run(context.Background(), batchCheckpoint{})
	require.NoError(t, err)
	require.Equal(t, lineNames(12), rec.logged())
	require.Equal(t, 12, out.DoneThrough)
}

// The checkpoint is the contiguous prefix and nothing else: a resume starts at
// DoneThrough, so counting a row finished past the watermark would count it
// twice.
func Test_batchRunner_checkpointIsTheContiguousPrefix(t *testing.T) {
	b, rec := newRunner(8, skipAll)
	b.parallelism = 4

	out, err := b.run(context.Background(), batchCheckpoint{})
	require.NoError(t, err)

	for _, cp := range rec.checkpoints() {
		require.Equal(t, cp.Counts.Lines, cp.DoneThrough,
			"a checkpoint counted rows the watermark had not reached")
		require.Equal(t, int64(cp.DoneThrough)*10, cp.NextOffset)
	}
	require.Equal(t, 8, out.DoneThrough)
}

// --- resume ---------------------------------------------------------------

func Test_batchRunner_resumesFromCheckpoint(t *testing.T) {
	var seen []string
	var mu sync.Mutex
	b, rec := newRunner(10, func(_ context.Context, line string) (PeriodicCounts, Outcome, error) {
		mu.Lock()
		seen = append(seen, line)
		mu.Unlock()
		return skipAll(context.Background(), line)
	})
	b.parallelism = 1

	out, err := b.run(context.Background(), batchCheckpoint{
		DoneThrough: 6,
		NextOffset:  60,
		Counts:      PeriodicCounts{Lines: 6, PdfsSkipped: 6},
	})
	require.NoError(t, err)

	require.Equal(t, lineNames(10)[6:], seen, "rows below the watermark were redone")
	require.Equal(t, 10, out.DoneThrough)
	// The carried counts survive: the first six are in the total exactly once.
	require.Equal(t, 10, out.Counts.Lines)
	require.Equal(t, 10, out.Counts.PdfsSkipped)
	require.Equal(t, lineNames(10)[6:], rec.logged())
}

// A checkpoint that already covers the batch means there is nothing to do, not
// a batch to redo.
func Test_batchRunner_finishedCheckpointIsANoop(t *testing.T) {
	b, rec := newRunner(5, func(context.Context, string) (PeriodicCounts, Outcome, error) {
		t.Fatal("no row should have been ingested")
		return PeriodicCounts{}, Outcome{}, nil
	})

	out, err := b.run(context.Background(), batchCheckpoint{
		DoneThrough: 5,
		NextOffset:  50,
		Counts:      PeriodicCounts{Lines: 5},
	})
	require.NoError(t, err)
	require.Equal(t, 5, out.DoneThrough)
	require.Equal(t, int64(50), out.NextOffset)
	require.Empty(t, rec.logged())
}

// --- retries --------------------------------------------------------------

// A lookup that fails once is retried in place rather than handed back to
// Temporal, which is the whole reason a batch is worth more than an activity
// per row.
func Test_batchRunner_retriesInPlace(t *testing.T) {
	var attempts atomic.Int32
	b, rec := newRunner(1, func(_ context.Context, line string) (PeriodicCounts, Outcome, error) {
		if attempts.Add(1) < 3 {
			return PeriodicCounts{Lines: 1}, Outcome{URL: line}, errors.New("fatcat 503")
		}
		return skipAll(context.Background(), line)
	})
	b.attempts = 3

	out, err := b.run(context.Background(), batchCheckpoint{})
	require.NoError(t, err)
	require.Equal(t, int32(3), attempts.Load())
	require.Equal(t, 1, out.Counts.PdfsSkipped)
	require.Equal(t, 0, out.Counts.PdfsErrored)
	require.Len(t, rec.rows, 1)
	require.Equal(t, StatusSkip, rec.rows[0].Status)
}

// A row that exhausts its retries is abandoned and logged, not fatal: a
// collection of millions will always contain PDFs petabox will not serve.
func Test_batchRunner_abandonsARowAfterItsAttempts(t *testing.T) {
	b, rec := newRunner(4, func(_ context.Context, line string) (PeriodicCounts, Outcome, error) {
		if line == "row000001" {
			return PeriodicCounts{Lines: 1}, Outcome{URL: line, Sha1B32: "BAD"},
				errors.New("range read failed: 404")
		}
		return skipAll(context.Background(), line)
	})
	b.attempts = 2

	out, err := b.run(context.Background(), batchCheckpoint{})
	require.NoError(t, err)
	require.False(t, out.Aborted)
	require.Equal(t, 4, out.DoneThrough)
	require.Equal(t, 4, out.Counts.Lines)
	require.Equal(t, 1, out.Counts.PdfsErrored)
	require.Equal(t, 3, out.Counts.PdfsSkipped)

	// The error row still names the PDF, which is the only reason the log is
	// worth reading after a batch has gone wrong.
	require.Equal(t, lineNames(4), rec.logged())
	require.Equal(t, StatusError, rec.rows[1].Status)
	require.Equal(t, "BAD", rec.rows[1].Sha1B32)
	require.Contains(t, rec.rows[1].Detail, "404")
}

// --- circuit breaker ------------------------------------------------------

// A streak of errors means something systemic -- GROBID down, fatcat
// unreachable -- and is not worth burning the rest of the collection against.
func Test_batchRunner_breakerTripsOnConsecutiveErrors(t *testing.T) {
	b, _ := newRunner(50, func(_ context.Context, line string) (PeriodicCounts, Outcome, error) {
		return PeriodicCounts{Lines: 1}, Outcome{URL: line}, errors.New("grobid connection refused")
	})
	b.parallelism = 1
	b.maxConsecutive = 5

	out, err := b.run(context.Background(), batchCheckpoint{})
	require.NoError(t, err, "an aborted batch is a result, not a failure")
	require.True(t, out.Aborted)
	require.Contains(t, out.AbortReason, "5 consecutive ingest failures")
	// It stops where the streak ended, and says where to resume.
	require.Equal(t, 5, out.DoneThrough)
	require.Equal(t, int64(50), out.NextOffset)
	require.Equal(t, 5, out.Counts.PdfsErrored)
}

// A successful row in between resets the streak, so a scattering of dead PDFs
// never trips the breaker.
func Test_batchRunner_successResetsTheStreak(t *testing.T) {
	b, _ := newRunner(30, func(_ context.Context, line string) (PeriodicCounts, Outcome, error) {
		var n int
		fmt.Sscanf(line, "row%06d", &n)
		if n%3 != 0 {
			return PeriodicCounts{Lines: 1}, Outcome{URL: line}, errors.New("nope")
		}
		return skipAll(context.Background(), line)
	})
	b.parallelism = 1
	b.maxConsecutive = 3

	out, err := b.run(context.Background(), batchCheckpoint{})
	require.NoError(t, err)
	require.False(t, out.Aborted)
	require.Equal(t, 30, out.DoneThrough)
	require.Equal(t, 20, out.Counts.PdfsErrored)
}

// A batch too short to reach the streak length can still be telling us the same
// thing the streak would have.
func Test_batchRunner_allRowsErroredAborts(t *testing.T) {
	b, _ := newRunner(3, func(_ context.Context, line string) (PeriodicCounts, Outcome, error) {
		return PeriodicCounts{Lines: 1}, Outcome{URL: line}, errors.New("es refused the write")
	})
	b.maxConsecutive = 50

	out, err := b.run(context.Background(), batchCheckpoint{})
	require.NoError(t, err)
	require.True(t, out.Aborted)
	require.Contains(t, out.AbortReason, "all 3 rows in the batch exhausted their retries")
}

// A streak carried in on the checkpoint counts: a batch retried after tripping
// its breaker must not start the count over.
func Test_batchRunner_carriesTheStreakAcrossAResume(t *testing.T) {
	b, _ := newRunner(10, func(_ context.Context, line string) (PeriodicCounts, Outcome, error) {
		return PeriodicCounts{Lines: 1}, Outcome{URL: line}, errors.New("still down")
	})
	b.parallelism = 1
	b.maxConsecutive = 4

	out, err := b.run(context.Background(), batchCheckpoint{
		DoneThrough: 2,
		NextOffset:  20,
		Consecutive: 3,
		Counts:      PeriodicCounts{Lines: 2, PdfsErrored: 2},
	})
	require.NoError(t, err)
	require.True(t, out.Aborted)
	require.Equal(t, 3, out.DoneThrough, "one more error was enough to trip it")
}

// --- failure modes --------------------------------------------------------

// An unwritable log is a broken deployment, not a per-row hiccup: it stops the
// batch rather than quietly dropping the record of what happened.
func Test_batchRunner_logFailureStopsTheBatch(t *testing.T) {
	b, rec := newRunner(10, skipAll)
	b.parallelism = 1
	rec.failRow = "row000004"

	out, err := b.run(context.Background(), batchCheckpoint{})
	require.ErrorContains(t, err, "read-only file system")
	// The checkpoint only ever advanced over rows that were written, so the
	// retry redoes row four and loses nothing.
	require.Equal(t, 4, out.DoneThrough)
	require.Equal(t, lineNames(4), rec.logged())
}

// Stopping before every row has a verdict has to be an error: returning
// successfully would tell the workflow the whole range was ingested and move
// the run past PDFs nobody looked at.
func Test_batchRunner_workerShutdownIsAnError(t *testing.T) {
	stopped := make(chan struct{})
	var done atomic.Int32

	b, rec := newRunner(50, func(_ context.Context, line string) (PeriodicCounts, Outcome, error) {
		if done.Add(1) == 3 {
			close(stopped)
		}
		return skipAll(context.Background(), line)
	})
	b.parallelism = 1
	b.stopped = stopped

	out, err := b.run(context.Background(), batchCheckpoint{})
	require.ErrorContains(t, err, "worker is shutting down")
	require.Less(t, out.DoneThrough, 50)
	// Whatever did finish is accounted for, so the retry resumes rather than
	// redoing the batch.
	require.Equal(t, out.DoneThrough, len(rec.logged()))
	require.Equal(t, int64(out.DoneThrough)*10, out.NextOffset)
}

// A cancelled context is not a verdict on the row it interrupted, so nothing is
// recorded for it.
func Test_batchRunner_cancelledContextRecordsNothingExtra(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var done atomic.Int32

	b, rec := newRunner(50, func(_ context.Context, line string) (PeriodicCounts, Outcome, error) {
		if done.Add(1) == 5 {
			cancel()
		}
		return skipAll(context.Background(), line)
	})
	b.parallelism = 1

	out, err := b.run(ctx, batchCheckpoint{})
	require.Error(t, err)
	require.Equal(t, out.DoneThrough, len(rec.logged()))
	cancel()
}

// --- backoff --------------------------------------------------------------

func Test_ingestBackoff(t *testing.T) {
	require.Equal(t, lineRetryInitial, ingestBackoff(1))
	require.Equal(t, 30*time.Second, ingestBackoff(2))
	require.Equal(t, time.Minute, ingestBackoff(3))
	require.Equal(t, lineRetryMax, ingestBackoff(4))
	require.Equal(t, lineRetryMax, ingestBackoff(20))
}
