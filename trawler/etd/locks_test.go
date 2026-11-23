package etd

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKeyedLocks_SerializesSameKey(t *testing.T) {
	k := newKeyedLocks()
	var inside, maxInside atomic.Int64
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Overlapping key sets in different orders must neither race nor
			// deadlock.
			keys := []string{"id:doi:x", fmt.Sprintf("sha1:%d", i%3)}
			if i%2 == 0 {
				keys = []string{keys[1], keys[0]}
			}
			unlock := k.lock(keys...)
			n := inside.Add(1)
			for {
				m := maxInside.Load()
				if n <= m || maxInside.CompareAndSwap(m, n) {
					break
				}
			}
			time.Sleep(time.Millisecond)
			inside.Add(-1)
			unlock()
		}()
	}
	wg.Wait()
	assert.EqualValues(t, 1, maxInside.Load())
	assert.Empty(t, k.m, "released keys are forgotten")
}

func TestKeyedLocks_NilAndDuplicates(t *testing.T) {
	var k *keyedLocks
	k.lock("a")()
	k = newKeyedLocks()
	k.lock("a", "a", "b")() // a duplicate key must not deadlock on itself
}

// Different lines describing the same thesis, worked on at once, must end up
// as one release and one file rather than one each.
func TestRun_ConcurrentLinesSharingIDsAndPDF(t *testing.T) {
	recs, raw := sampleRecords(t)
	var lines [][]byte
	for i := range 20 {
		// Same DOI key and PDF, different bytes (so different line hashes).
		lines = append(lines, bytes.Replace(raw[0], []byte(`"title":"`), []byte(fmt.Sprintf(`"title":"%d `, i)), 1))
	}
	dir := t.TempDir()
	input := filepath.Join(dir, "in.ndjson")
	require.NoError(t, os.WriteFile(input, bytes.Join(lines, []byte("\n")), 0o644))

	fc := newFakeFatcat()
	fe := &fakeFetcher{pdfs: map[string]Checksums{recs[0].Fulltext[0].FetchURL: sums("a", "")}}
	d := testDeps(fc, fe)
	d.Locks = newKeyedLocks()
	cfg := Config{Path: input, Concurrency: 8, LineAttempts: 1}
	c := &counters{}
	require.NoError(t, runMetadata(context.Background(), cfg, d, openTestState(t), nil, c, make(chan struct{}, 1)))

	assert.Len(t, fc.releases, 1)
	assert.Len(t, fc.files, 1)
	assert.EqualValues(t, 1, c.created.Load())
	assert.EqualValues(t, 19, c.matchedID.Load())
}
