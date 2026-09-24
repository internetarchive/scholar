package feeds

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// crossrefWorks builds a minimal /works response with the given number of
// items and an optional next cursor.
func crossrefWorks(numItems, total int, nextCursor string) string {
	items := make([]string, numItems)
	for i := range items {
		items[i] = fmt.Sprintf(`{"DOI":"10.1234/test.%d"}`, i)
	}
	return fmt.Sprintf(`{"status":"ok","message-type":"work-list","message":{"items":[%s],"items-per-page":%d,"next-cursor":%q,"total-results":%d}}`,
		strings.Join(items, ","), numItems, nextCursor, total)
}

func testCrossrefHarvester(endpoint string) *CrossrefHarvester {
	return &CrossrefHarvester{
		Client:      http.DefaultClient,
		ApiEndpoint: endpoint,
		ApiFilter:   "index",
		Rows:        1000,
		MaxRetries:  3,
		RetryDelay:  time.Millisecond,
	}
}

var (
	crossrefTestFrom  = time.Date(2023, 1, 15, 0, 0, 0, 0, time.UTC)
	crossrefTestUntil = time.Date(2023, 1, 15, 23, 59, 59, 0, time.UTC)
)

// The cursor only encodes a position in the result set, not the query, so
// filter and rows have to be resent with every paged request. Dropping them
// silently paginates over all of crossref instead of the requested slice.
func TestCrossrefHarvesterWriteSliceKeepsQueryParams(t *testing.T) {
	var calls int32
	var queries []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		if atomic.AddInt32(&calls, 1) == 1 {
			io.WriteString(w, crossrefWorks(1, 2, "next-cursor-value"))
		} else {
			io.WriteString(w, crossrefWorks(1, 2, ""))
		}
	}))
	defer server.Close()

	c := testCrossrefHarvester(server.URL)
	var buf strings.Builder
	if err := c.WriteSlice(&buf, crossrefTestFrom, crossrefTestUntil); err != nil {
		t.Fatalf("WriteSlice: %v", err)
	}
	if len(queries) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(queries))
	}
	for i, q := range queries {
		vs, err := url.ParseQuery(q)
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		if got, want := vs.Get("filter"), "from-index-date:2023-01-15,until-index-date:2023-01-15"; got != want {
			t.Errorf("request %d filter: got %q, want %q", i, got, want)
		}
		if got, want := vs.Get("rows"), "1000"; got != want {
			t.Errorf("request %d rows: got %q, want %q", i, got, want)
		}
	}
	if got, want := queries[0], "cursor=%2A"; !strings.Contains(got, want) {
		t.Errorf("first request should start a new cursor, got %q", got)
	}
	if got, want := queries[1], "cursor=next-cursor-value"; !strings.Contains(got, want) {
		t.Errorf("second request should follow the cursor, got %q", got)
	}
}

// The API intermittently returns 404 for a valid deep paging cursor.
func TestCrossrefHarvesterWriteSliceRetriesTransientStatus(t *testing.T) {
	for _, code := range []int{http.StatusNotFound, http.StatusTooManyRequests, http.StatusBadGateway} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			var calls int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Fail the second page twice, then serve it.
				if strings.Contains(r.URL.RawQuery, "cursor=next") && atomic.AddInt32(&calls, 1) <= 2 {
					w.WriteHeader(code)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if !strings.Contains(r.URL.RawQuery, "cursor=next") {
					io.WriteString(w, crossrefWorks(1, 2, "next"))
				} else {
					io.WriteString(w, crossrefWorks(1, 2, ""))
				}
			}))
			defer server.Close()

			c := testCrossrefHarvester(server.URL)
			var buf strings.Builder
			if err := c.WriteSlice(&buf, crossrefTestFrom, crossrefTestUntil); err != nil {
				t.Fatalf("WriteSlice: %v", err)
			}
			if got, want := strings.Count(strings.TrimSpace(buf.String()), "\n")+1, 2; got != want {
				t.Errorf("got %d records, want %d", got, want)
			}
		})
	}
}

func TestCrossrefHarvesterWriteSliceRetriesExhausted(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := testCrossrefHarvester(server.URL)
	var buf strings.Builder
	err := c.WriteSlice(&buf, crossrefTestFrom, crossrefTestUntil)
	if err == nil {
		t.Fatal("expected an error after retries are exhausted")
	}
	if !strings.Contains(err.Error(), "HTTP 404") {
		t.Errorf("error should mention the status: %v", err)
	}
	if got, want := int(calls), c.MaxRetries+1; got != want {
		t.Errorf("got %d requests, want %d", got, want)
	}
}

// A 400 means the request itself is wrong; repeating it will not help.
func TestCrossrefHarvesterWriteSliceDoesNotRetryPermanentStatus(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()

	c := testCrossrefHarvester(server.URL)
	var buf strings.Builder
	if err := c.WriteSlice(&buf, crossrefTestFrom, crossrefTestUntil); err == nil {
		t.Fatal("expected an error")
	}
	if got := int(calls); got != 1 {
		t.Errorf("got %d requests, want 1", got)
	}
}

func TestCrossrefHarvesterWriteSliceRetriesDecodeFailure(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if atomic.AddInt32(&calls, 1) == 1 {
			io.WriteString(w, `{"status":"ok","message":{"items":[`) // truncated
			return
		}
		io.WriteString(w, crossrefWorks(1, 1, ""))
	}))
	defer server.Close()

	c := testCrossrefHarvester(server.URL)
	var buf strings.Builder
	if err := c.WriteSlice(&buf, crossrefTestFrom, crossrefTestUntil); err != nil {
		t.Fatalf("WriteSlice: %v", err)
	}
	if got := strings.TrimSpace(buf.String()); got == "" {
		t.Error("expected the retried page to be written")
	}
}

func TestCrossrefHarvesterRetryDelayDefault(t *testing.T) {
	c := &CrossrefHarvester{}
	if got := c.retryDelay(); got != crossrefDefaultRetryDelay {
		t.Errorf("retryDelay: got %v, want %v", got, crossrefDefaultRetryDelay)
	}
	c.RetryDelay = time.Second
	if got := c.retryDelay(); got != time.Second {
		t.Errorf("retryDelay: got %v, want %v", got, time.Second)
	}
}
