package crawling

import (
	"context"
	"sync"
	"time"

	spn "git.archive.org/webgroup/scholar/trawler/spn/spnclient"
)

// defaultSPNSlotStatusTTL is used when crawling.spn_slot_status_ttl is unset.
const defaultSPNSlotStatusTTL = 5 * time.Second

// spnSlotStatusTimeout bounds the status query itself. The gate is advisory --
// a crawl that cannot get an answer proceeds and finds out from /save -- so
// there is no reason to let it inherit the transport's 120s patience and sit
// in front of every capture. Short enough that failing is cheaper than
// waiting.
const spnSlotStatusTimeout = 10 * time.Second

// spnSlots caches what SPN's /status/user endpoint says about how many capture
// sessions the account has free.
//
// It exists because that number is a property of the account, not of a crawl:
// every crawl in a worker draws on the same pool, and the daily crawl draws on
// it too. A hundred crawls deciding whether to call SPN should cost one status
// query between them rather than a hundred, so a reading is reused for a TTL
// and shared process-wide.
//
// Between refreshes the count is spent down as crawls claim slots, which is
// what stops one cached positive reading from waving through a stampede much
// larger than the pool. There is no matching release: a claim is not a lease,
// and it is the next refresh -- not a finishing crawl -- that restores the
// count. That leaves the gate approximate in both directions, which is the
// right trade for something purely advisory. Claiming a slot that has since
// been taken costs one rejected POST, which is what we do today anyway;
// missing one that has since freed costs a TTL's delay.
type spnSlots struct {
	mu sync.Mutex
	// available is the free-session count as of at, less any claims since.
	available int
	at        time.Time
	// refreshing keeps a burst of crawls from all querying at once when the
	// reading goes stale. The ones that lose the race proceed on the stale
	// count rather than waiting for the winner.
	refreshing bool
}

// spnSlotCache is the gate every crawl in the process shares. SPN credentials
// are per-process, so one cache covers the whole worker.
var spnSlotCache = &spnSlots{}

// Claim reports whether the caller should go ahead and ask SPN for a capture,
// refreshing the cached reading first if it has gone stale.
//
// False means SPN had no session free as of the last reading, and the caller
// should defer rather than spend a POST learning that. True is permission to
// try, not a guarantee -- the pool is shared with every other crawl on these
// credentials, here and in the daily workflow.
func (s *spnSlots) Claim(ctx context.Context, c spn.Client, ttl time.Duration, now time.Time) bool {
	if s.beginRefresh(now, ttl) {
		qctx, cancel := context.WithTimeout(ctx, spnSlotStatusTimeout)
		st, err := c.StatusUser(qctx)
		cancel()
		s.endRefresh(st.Available, now, err == nil)
	}
	return s.take(now, ttl)
}

// MarkBusy records that SPN has just turned a capture away for want of a
// session. It is how the crawl that hit the wall spares the others: until the
// reading goes stale they defer without each spending a POST to be told the
// same thing.
func (s *spnSlots) MarkBusy(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.available = 0
	s.at = now
}

// beginRefresh reports whether this caller is the one that should re-query
// SPN: the reading is stale and nobody else is already asking. The query
// itself happens with the lock released, since it is a network call.
func (s *spnSlots) beginRefresh(now time.Time, ttl time.Duration) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.refreshing || s.fresh(now, ttl) {
		return false
	}
	s.refreshing = true
	return true
}

// endRefresh stores the result of a query. A failed query leaves the previous
// reading in place to go stale on its own; guessing in either direction from a
// status endpoint we could not reach would be worse than saying nothing.
func (s *spnSlots) endRefresh(available int, now time.Time, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshing = false
	if ok {
		s.available = available
		s.at = now
	}
}

// take spends one slot from the cached reading, if there is a reading and a
// slot to spend.
func (s *spnSlots) take(now time.Time, ttl time.Duration) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	// No usable reading: the query failed, or another crawl is still making
	// it. Say yes. A gate that blocks when it cannot see is worse than no gate
	// at all, and the POST it would have saved is exactly what the crawler did
	// before this cache existed.
	if !s.fresh(now, ttl) {
		return true
	}

	if s.available <= 0 {
		return false
	}
	s.available--
	return true
}

func (s *spnSlots) fresh(now time.Time, ttl time.Duration) bool {
	return !s.at.IsZero() && now.Sub(s.at) < ttl
}

// reset drops the cached reading. Tests use it to keep one case out of the
// next; nothing in the crawler needs it.
func (s *spnSlots) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.available = 0
	s.at = time.Time{}
	s.refreshing = false
}
