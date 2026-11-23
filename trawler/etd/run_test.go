package etd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func openTestState(t *testing.T) *StateDB {
	t.Helper()
	s, err := OpenStateDB(filepath.Join(t.TempDir(), "state.db"))
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	return s
}

func TestStateDB_Lifecycle(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := OpenStateDB(path)
	require.NoError(t, err)

	st, err := s.LineState(ctx, "h1")
	require.NoError(t, err)
	assert.Equal(t, "", st)

	job := IndexJob{LineHash: "h1", ReleaseID: "r", FileIDs: []string{"f1", "f2"}, BestFileID: "f1", BestFetchURL: "u"}
	require.NoError(t, s.FinishMetadata(ctx, "k1", "created", job))
	st, _ = s.LineState(ctx, "h1")
	assert.Equal(t, LineIndexing, st)

	claimed, err := s.ClaimJobs(ctx, 10)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	assert.Equal(t, job, claimed[0])
	again, _ := s.ClaimJobs(ctx, 10)
	assert.Empty(t, again, "a claimed job is not handed out twice")

	// A run that dies holding a claim gives it back on the next open.
	require.NoError(t, s.Close())
	s, err = OpenStateDB(path)
	require.NoError(t, err)
	defer s.Close()
	claimed, _ = s.ClaimJobs(ctx, 10)
	require.Len(t, claimed, 1)

	// Failures back off, then park.
	parked, err := s.FailJob(ctx, "h1", 1, 2, 0, errors.New("es down"))
	require.NoError(t, err)
	assert.False(t, parked)
	claimed, _ = s.ClaimJobs(ctx, 10)
	require.Len(t, claimed, 1)
	assert.Equal(t, 1, claimed[0].Attempts)
	parked, _ = s.FailJob(ctx, "h1", 2, 2, 0, errors.New("es down"))
	assert.True(t, parked)
	claimed, _ = s.ClaimJobs(ctx, 10)
	assert.Empty(t, claimed)

	n, err := s.RequeueFailedJobs(ctx)
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)
	claimed, _ = s.ClaimJobs(ctx, 10)
	require.Len(t, claimed, 1)
	assert.Equal(t, 0, claimed[0].Attempts)

	require.NoError(t, s.FinishJob(ctx, "h1"))
	st, _ = s.LineState(ctx, "h1")
	assert.Equal(t, LineDone, st)
	q, _ := s.QueueCounts(ctx)
	assert.Equal(t, map[string]int{jobDone: 1}, q)
}

func TestStateDB_BackoffDelaysClaim(t *testing.T) {
	ctx := context.Background()
	s := openTestState(t)
	require.NoError(t, s.FinishMetadata(ctx, "k", "", IndexJob{LineHash: "h", ReleaseID: "r", FileIDs: []string{"f"}, BestFileID: "f", BestFetchURL: "u"}))
	s.ClaimJobs(ctx, 1)
	_, err := s.FailJob(ctx, "h", 1, 3, time.Hour, errors.New("x"))
	require.NoError(t, err)
	claimed, _ := s.ClaimJobs(ctx, 1)
	assert.Empty(t, claimed)
}

func TestLineIsSettled(t *testing.T) {
	for st, want := range map[string]bool{
		"": false, LineFailed: false, LineSkipped: false,
		LineIndexing: true, LineDone: true, LineRejected: true,
	} {
		assert.Equal(t, want, lineIsSettled(st), st)
	}
}

// gateIndexer blocks every Index call until open is closed.
type gateIndexer struct {
	open    chan struct{}
	started atomic.Int64
	mu      sync.Mutex
	seen    []string
}

func (g *gateIndexer) Index(ctx context.Context, j IndexJob) (string, error) {
	g.started.Add(1)
	select {
	case <-g.open:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	g.mu.Lock()
	g.seen = append(g.seen, j.LineHash)
	g.mu.Unlock()
	return "ok", nil
}

// The design requirement: indexing must not block metadata creation. With
// every index worker stuck, the metadata side still has to finish the file.
func TestRun_IndexingDoesNotBlockMetadata(t *testing.T) {
	recs, raw := sampleRecords(t)
	dir := t.TempDir()
	input := filepath.Join(dir, "in.ndjson")
	// Lines 0 and 2 ingest, 3 is withdrawn; line 0 twice is one line.
	require.NoError(t, os.WriteFile(input, bytes.Join([][]byte{raw[0], raw[2], raw[3], raw[0], {}}, []byte("\n")), 0o644))

	fc := newFakeFatcat()
	fe := &fakeFetcher{pdfs: map[string]Checksums{
		recs[0].Fulltext[0].FetchURL: sums("a", ""),
		recs[2].Fulltext[2].FetchURL: sums("b", ""),
	}}
	state := openTestState(t)
	outlog, err := OpenOutcomeLog(dir, "outcomes")
	require.NoError(t, err)
	defer outlog.Close()

	cfg := Config{Path: input, Concurrency: 2, IndexConcurrency: 1, LineAttempts: 1, IndexAttempts: 3}
	c := &counters{}
	ctx := context.Background()
	jobQueued := make(chan struct{}, 1)
	var metadataDone atomic.Bool
	ix := &gateIndexer{open: make(chan struct{})}

	indexErr := make(chan error, 1)
	go func() { indexErr <- runIndex(ctx, cfg, ix, state, outlog, c, &metadataDone, jobQueued) }()

	// Metadata completes while the indexer is wedged.
	metaErr := make(chan error, 1)
	go func() {
		err := runMetadata(ctx, cfg, testDeps(fc, fe), state, outlog, c, jobQueued)
		metadataDone.Store(true)
		metaErr <- err
	}()
	select {
	case err := <-metaErr:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("metadata side blocked behind a stuck indexer")
	}
	assert.EqualValues(t, 2, c.created.Load())
	assert.EqualValues(t, 1, c.rejected.Load())

	q, _ := state.QueueCounts(ctx)
	assert.Equal(t, 2, q[jobPending]+q[jobClaimed])
	assert.LessOrEqual(t, ix.started.Load(), int64(1), "never more claims than index workers")

	close(ix.open)
	select {
	case err := <-indexErr:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("index side did not drain")
	}
	assert.EqualValues(t, 2, c.indexed.Load())
	q, _ = state.QueueCounts(ctx)
	assert.Equal(t, map[string]int{jobDone: 2}, q)

	// A rerun skips every line: done, done, rejected, and the duplicate.
	c2 := &counters{}
	require.NoError(t, runMetadata(ctx, cfg, testDeps(fc, fe), state, outlog, c2, jobQueued))
	assert.EqualValues(t, 4, c2.read.Load())
	assert.EqualValues(t, 4, c2.settled.Load())
	assert.Len(t, fc.releases, 2, "nothing created twice")

	log, err := os.ReadFile(filepath.Join(dir, "outcomes.tsv"))
	require.NoError(t, err)
	assert.Contains(t, string(log), "\tindexed\t")
	assert.Contains(t, string(log), "withdrawn")
}

func TestRun_FailedLinesAreRetriedNextRunAndBreakerTrips(t *testing.T) {
	recs, raw := sampleRecords(t)
	dir := t.TempDir()
	input := filepath.Join(dir, "in.ndjson")
	require.NoError(t, os.WriteFile(input, bytes.Join([][]byte{raw[0], raw[2]}, []byte("\n")), 0o644))

	fe := &fakeFetcher{errs: map[string]error{
		recs[0].Fulltext[0].FetchURL: errors.New("503"),
		recs[2].Fulltext[2].FetchURL: errors.New("503"),
	}, pdfs: map[string]Checksums{}}
	state := openTestState(t)
	cfg := Config{Path: input, Concurrency: 1, LineAttempts: 1, MaxConsecutiveFailures: 2}
	c := &counters{}
	err := runMetadata(context.Background(), cfg, testDeps(newFakeFatcat(), fe), state, nil, c, make(chan struct{}, 1))
	assert.ErrorContains(t, err, "2 lines in a row failed")

	// Failed is not settled: wayback recovering means the next run ingests.
	fe.errs = nil
	fe.pdfs[recs[0].Fulltext[0].FetchURL] = sums("a", "")
	fe.pdfs[recs[2].Fulltext[2].FetchURL] = sums("b", "")
	c = &counters{}
	cfg.MaxConsecutiveFailures = 0
	require.NoError(t, runMetadata(context.Background(), cfg, testDeps(newFakeFatcat(), fe), state, nil, c, make(chan struct{}, 1)))
	assert.EqualValues(t, 2, c.created.Load())
}

func TestRun_Limit(t *testing.T) {
	_, raw := sampleRecords(t)
	dir := t.TempDir()
	input := filepath.Join(dir, "in.ndjson")
	require.NoError(t, os.WriteFile(input, bytes.Join(raw, []byte("\n")), 0o644))
	cfg := Config{Path: input, Concurrency: 1, LineAttempts: 1, Limit: 2}
	c := &counters{}
	require.NoError(t, runMetadata(context.Background(), cfg, testDeps(newFakeFatcat(), &fakeFetcher{}), openTestState(t), nil, c, make(chan struct{}, 1)))
	assert.EqualValues(t, 2, c.read.Load())
}
