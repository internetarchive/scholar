package etd

import (
	"slices"
	"sync"
)

// keyedLocks serializes work on the same key across metadata workers.
//
// Lookups and creates are separate fc2 calls, so two lines that share a DOI or a
// PDF and run at the same moment would both miss the lookup and both create.
// Lines lock the external IDs they will look up, then the sha1s they will look
// up, and hold them until their writes are done.
//
// Deadlock-free as used: a line takes all its ID keys, then all its sha1 keys,
// each batch in sorted order and every ID key ordered before every sha1 key
// (prefixes "id:" < "sha1:"), so locks are always acquired in one global order.
type keyedLocks struct {
	mu sync.Mutex
	m  map[string]*keyedLock
}

type keyedLock struct {
	ch   chan struct{}
	refs int
}

func newKeyedLocks() *keyedLocks {
	return &keyedLocks{m: map[string]*keyedLock{}}
}

// lock acquires every key (duplicates are fine) and returns the function that
// releases them. A nil *keyedLocks locks nothing.
func (k *keyedLocks) lock(keys ...string) (unlock func()) {
	if k == nil || len(keys) == 0 {
		return func() {}
	}
	keys = slices.Clone(keys)
	slices.Sort(keys)
	keys = slices.Compact(keys)

	held := make([]*keyedLock, 0, len(keys))
	for _, key := range keys {
		k.mu.Lock()
		l, ok := k.m[key]
		if !ok {
			l = &keyedLock{ch: make(chan struct{}, 1)}
			k.m[key] = l
		}
		l.refs++
		k.mu.Unlock()

		l.ch <- struct{}{}
		held = append(held, l)
	}
	return func() {
		for i := len(held) - 1; i >= 0; i-- {
			<-held[i].ch
			k.mu.Lock()
			held[i].refs--
			if held[i].refs == 0 {
				delete(k.m, keys[i])
			}
			k.mu.Unlock()
		}
	}
}
