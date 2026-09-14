package crawling

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	spn "git.archive.org/webgroup/scholar/trawler/spn/spnclient"
	"github.com/spf13/viper"
)

// fakeSPN answers /status/user with whatever the test wants and counts both
// the status queries and the save attempts, which is how these tests tell
// "deferred before asking SPN" apart from "asked SPN and was turned away".
type fakeSPN struct {
	available   atomic.Int32
	statusErr   error
	statusCalls atomic.Int32
	saves       atomic.Int32
	// saveMessage is what /save answers with; the empty string means a normal
	// response carrying a job id.
	saveMessage string
	// noJobID makes /save answer without a job id, the malformed response
	// spnToCdx turns into an SPNError carrying no status_ext at all.
	noJobID bool
	// jobStatus is what /status/<job id> answers with once a save is accepted.
	jobStatus spn.JobStatus
	// block, when non-nil, holds StatusUser until it is closed, so a test can
	// have a second caller arrive mid-refresh.
	block chan struct{}
}

func (f *fakeSPN) StatusUser(context.Context) (spn.UserStatus, error) {
	f.statusCalls.Add(1)
	if f.block != nil {
		<-f.block
	}
	if f.statusErr != nil {
		return spn.UserStatus{}, f.statusErr
	}
	return spn.UserStatus{Available: int(f.available.Load())}, nil
}

func (f *fakeSPN) Save(context.Context, spn.SaveRequest) (spn.SaveResult, error) {
	f.saves.Add(1)
	if f.saveMessage != "" {
		return spn.SaveResult{Message: f.saveMessage}, nil
	}
	if f.noJobID {
		return spn.SaveResult{}, nil
	}
	return spn.SaveResult{JobID: "job-1"}, nil
}

func (f *fakeSPN) StatusSystem(context.Context) (spn.SystemStatus, error) {
	return spn.SystemStatus{}, nil
}

func (f *fakeSPN) StatusJob(context.Context, string) (spn.JobStatus, error) {
	return f.jobStatus, nil
}

const testTTL = time.Minute

func Test_spnSlots_spendsDownACachedReading(t *testing.T) {
	s := &spnSlots{}
	f := &fakeSPN{}
	f.available.Store(2)
	now := time.Now()

	// Two slots, three crawls: the third is the one that has to wait.
	for i, want := range []bool{true, true, false} {
		if got := s.Claim(context.Background(), f, testTTL, now); got != want {
			t.Errorf("claim %d: got %v, want %v", i+1, got, want)
		}
	}

	// The whole point is that the three of them shared one query.
	if got := f.statusCalls.Load(); got != 1 {
		t.Errorf("expected a single status query, got %d", got)
	}
}

func Test_spnSlots_refreshesAStaleReading(t *testing.T) {
	s := &spnSlots{}
	f := &fakeSPN{}
	f.available.Store(1)
	start := time.Now()

	if !s.Claim(context.Background(), f, testTTL, start) {
		t.Fatal("first claim was refused")
	}
	// Spent, and the reading is still fresh.
	if s.Claim(context.Background(), f, testTTL, start) {
		t.Error("a spent reading still allowed a claim")
	}

	// Past the TTL the count is re-read, and SPN has freed a session since.
	f.available.Store(3)
	if !s.Claim(context.Background(), f, testTTL, start.Add(testTTL)) {
		t.Error("a refreshed reading refused a claim")
	}
	if got := f.statusCalls.Load(); got != 2 {
		t.Errorf("expected exactly one query per TTL, got %d", got)
	}
}

func Test_spnSlots_markBusyStopsTheRest(t *testing.T) {
	s := &spnSlots{}
	f := &fakeSPN{}
	f.available.Store(10)
	now := time.Now()

	if !s.Claim(context.Background(), f, testTTL, now) {
		t.Fatal("claim refused while slots were free")
	}
	// The crawl that got through is told by SPN that the pool is empty after
	// all. Everyone behind it should believe that over the cached count.
	s.MarkBusy(now)
	if s.Claim(context.Background(), f, testTTL, now) {
		t.Error("a claim got through after SPN said it was full")
	}
}

func Test_spnSlots_allowsClaimsWhenItCannotSee(t *testing.T) {
	s := &spnSlots{}
	f := &fakeSPN{statusErr: errors.New("status/user is down")}
	now := time.Now()

	// A gate that cannot reach SPN must not become a second outage: the crawl
	// goes ahead and finds out from /save, which is the old behaviour.
	if !s.Claim(context.Background(), f, testTTL, now) {
		t.Error("a failed status query blocked a crawl")
	}
}

func Test_spnSlots_concurrentClaimsQueryOnce(t *testing.T) {
	s := &spnSlots{}
	f := &fakeSPN{block: make(chan struct{})}
	f.available.Store(0)
	now := time.Now()

	// One caller is inside StatusUser and holding it open.
	var first bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		first = s.Claim(context.Background(), f, testTTL, now)
	}()

	// Give the refresher time to be the one in flight, then send a crowd in
	// behind it. They must not pile onto the status endpoint, and they must
	// not block waiting for it either.
	waitFor(t, func() bool { return f.statusCalls.Load() == 1 })

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if !s.Claim(context.Background(), f, testTTL, now) {
				t.Error("a crawl was refused on a reading nobody had yet")
			}
		}()
	}
	wg.Wait()

	close(f.block)
	<-done

	if first {
		t.Error("the refresher claimed a slot SPN said it did not have")
	}
	if got := f.statusCalls.Load(); got != 1 {
		t.Errorf("expected one status query for the whole burst, got %d", got)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition never became true")
}

func Test_spnToCdx_defersWithoutAskingWhenSPNIsKnownFull(t *testing.T) {
	t.Cleanup(spnSlotCache.reset)
	spnSlotCache.reset()

	f := &fakeSPN{}
	f.available.Store(0)

	c := quietCrawler()
	c.SPNClient = f
	c.DeferWhenSPNBusy = true

	_, err := c.spnToCdx(context.Background(), "https://example.com/paper.pdf", false)

	var busy *SPNBusyError
	if !errors.As(err, &busy) {
		t.Fatalf("expected an SPNBusyError, got %v", err)
	}
	if busy.Reason != "no-sessions" {
		t.Errorf("expected reason no-sessions, got %q", busy.Reason)
	}
	// Not spending a POST to be told what /status/user already said is the
	// whole reason the gate is there.
	if got := f.saves.Load(); got != 0 {
		t.Errorf("expected no save attempt, got %d", got)
	}
}

func Test_spnToCdx_defersOnceSPNTurnsItAway(t *testing.T) {
	viper.Set("crawling.spn_slot_poll_interval", "10ms")
	t.Cleanup(func() { viper.Set("crawling.spn_slot_poll_interval", nil) })
	t.Cleanup(spnSlotCache.reset)
	spnSlotCache.reset()

	// Slots looked free, so the crawl gets as far as POSTing; SPN then says no.
	f := &fakeSPN{saveMessage: "you have reached the limit of active sessions"}
	f.available.Store(4)

	c := quietCrawler()
	c.SPNClient = f
	c.DeferWhenSPNBusy = true

	done := make(chan error, 1)
	go func() {
		_, err := c.spnToCdx(context.Background(), "https://example.com/paper.pdf", false)
		done <- err
	}()

	select {
	case err := <-done:
		var busy *SPNBusyError
		if !errors.As(err, &busy) {
			t.Fatalf("expected an SPNBusyError, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("spnToCdx polled instead of deferring")
	}

	if got := f.saves.Load(); got != 1 {
		t.Errorf("expected exactly one save attempt before deferring, got %d", got)
	}
	// The rejection is shared, so the next crawl in this worker defers without
	// a POST of its own.
	if spnSlotCache.Claim(context.Background(), f, testTTL, time.Now()) {
		t.Error("the rejection was not recorded for the other crawls")
	}
}

func Test_spnToCdx_stillPollsWhenDeferralIsOff(t *testing.T) {
	viper.Set("crawling.spn_slot_poll_interval", "10ms")
	t.Cleanup(func() { viper.Set("crawling.spn_slot_poll_interval", nil) })
	t.Cleanup(spnSlotCache.reset)
	spnSlotCache.reset()

	// The daily crawl's behaviour: no deferral, so a full SPN is waited out
	// rather than handed back. Cancellation is what ends it.
	f := &fakeSPN{saveMessage: "you have reached the limit of active sessions"}
	f.available.Store(0)

	c := quietCrawler()
	c.SPNClient = f

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)

	_, err := c.spnToCdx(ctx, "https://example.com/paper.pdf", false)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation to end the poll, got %v", err)
	}
	var busy *SPNBusyError
	if errors.As(err, &busy) {
		t.Error("a crawler with deferral off returned an SPNBusyError")
	}
	if got := f.saves.Load(); got < 2 {
		t.Errorf("expected it to keep polling, got %d save attempts", got)
	}
	// And it never consulted the gate, so the daily crawl pays nothing for it.
	if got := f.statusCalls.Load(); got != 0 {
		t.Errorf("expected no status query with deferral off, got %d", got)
	}
}
