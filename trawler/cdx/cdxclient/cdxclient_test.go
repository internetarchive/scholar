package cdxclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// cdxServer answers with each of the given status codes in turn, repeating the
// last one once the list runs out, and counts the requests it saw.
func cdxServer(t *testing.T, codes ...int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		i := int(hits.Add(1)) - 1
		if i >= len(codes) {
			i = len(codes) - 1
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(codes[i])
		// A body on every response, including the 504s, so that failing to
		// drain one is a leak the test could in principle notice.
		_, _ = w.Write([]byte(`[]`))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func testClient(t *testing.T, srv *httptest.Server, cfg Config) Client {
	t.Helper()
	cfg.Auth = "test"
	cfg.Endpoint = srv.URL
	cfg.HTTPClient = srv.Client()
	return NewClient(cfg)
}

// The 504 retry exists because CDX hands out gateway timeouts under load. The
// loop used to break on any response at all, so it never ran.
func Test_Query_retriesA504(t *testing.T) {
	srv, hits := cdxServer(t, 504, 504, 200)
	c := testClient(t, srv, Config{Retries: 4, Backoff: time.Millisecond})

	if _, err := c.Query(context.Background(), QueryParams{URL: "https://example.com/a"}); err != nil {
		t.Fatalf("expected the retry to reach the 200, got %v", err)
	}
	if got := hits.Load(); got != 3 {
		t.Errorf("expected 3 requests (two 504s then a 200), got %d", got)
	}
}

func Test_Query_givesUpAfterRetries(t *testing.T) {
	srv, hits := cdxServer(t, 504)
	c := testClient(t, srv, Config{Retries: 3, Backoff: time.Millisecond})

	_, err := c.Query(context.Background(), QueryParams{URL: "https://example.com/a"})
	if err == nil {
		t.Fatal("expected an error once the retries ran out")
	}
	if got := hits.Load(); got != 3 {
		t.Errorf("expected exactly Retries requests, got %d", got)
	}
}

// An exhausted query used to sleep one final backoff before coming back round
// to notice it was out of attempts. At the deployed 10s backoff and 4 retries
// that is 40 seconds of waiting to give up.
func Test_Query_doesNotBackOffAfterTheLastAttempt(t *testing.T) {
	srv, _ := cdxServer(t, 504)
	backoff := 40 * time.Millisecond
	c := testClient(t, srv, Config{Retries: 3, Backoff: backoff})

	start := time.Now()
	if _, err := c.Query(context.Background(), QueryParams{URL: "https://example.com/a"}); err == nil {
		t.Fatal("expected an error")
	}
	elapsed := time.Since(start)

	// Backoffs scale with the attempt number, so three attempts should cost
	// two of them -- 1x + 2x -- and the old trailing nap added a third at 3x.
	want := backoff * (1 + 2)
	buggy := want + backoff*3
	if ceiling := (want + buggy) / 2; elapsed >= ceiling {
		t.Errorf("took %s; expected about %s, and %s would mean it napped after the final attempt",
			elapsed, want, buggy)
	}
}

// Callers under a liveness deadline need to say they are still there between
// attempts; a silent retrying query is what times a Temporal activity out.
func Test_Query_callsOnRetryBetweenAttempts(t *testing.T) {
	srv, _ := cdxServer(t, 504, 504, 200)
	var beats []int
	c := testClient(t, srv, Config{
		Retries: 4,
		Backoff: time.Millisecond,
		OnRetry: func(attempt int) { beats = append(beats, attempt) },
	})

	if _, err := c.Query(context.Background(), QueryParams{URL: "https://example.com/a"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Once after each 504, and not after the attempt that succeeded.
	if len(beats) != 2 {
		t.Errorf("expected 2 retry notifications, got %d (%v)", len(beats), beats)
	}
}

func Test_Query_cancellationEndsTheBackoff(t *testing.T) {
	srv, _ := cdxServer(t, 504)
	c := testClient(t, srv, Config{Retries: 5, Backoff: 10 * time.Second})

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)

	start := time.Now()
	_, err := c.Query(ctx, QueryParams{URL: "https://example.com/a"})
	if err == nil {
		t.Fatal("expected cancellation to surface")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("cancellation took %s to take effect", elapsed)
	}
}
