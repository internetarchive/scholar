package feeds

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

// dataciteListRecords builds a minimal Datacite /dois API response containing
// the given raw JSON data items and an optional next-page link.
func dataciteListRecords(items []string, total int, nextLink string) string {
	var sb strings.Builder
	sb.WriteString(`{"data":[`)
	for i, item := range items {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(item)
	}
	sb.WriteString(fmt.Sprintf(`],"meta":{"total":%d},"links":{"next":%q}}`, total, nextLink))
	return sb.String()
}

// mockDataciteDoc is a minimal Datacite document in the format returned by the
// /dois endpoint (matches schema/datacite.Document). Must be compact (no
// embedded newlines) so NDJSON line-counting in tests works correctly.
const mockDataciteDoc = `{"id":"10.1234/test","type":"dois","attributes":{"doi":"10.1234/test","titles":[{"title":"Test Dataset"}],"creators":[{"givenName":"Jane","familyName":"Smith"}],"contributors":[],"published":"2023-01-15T00:00:00Z","publisher":"Test Publisher","publicationYear":2023},"relationships":{"client":{"data":{"id":"test.repo","type":"clients"}}}}`

// -- DataciteHarvester.DaySliceKey --

func TestDataciteHarvesterDaySliceKey(t *testing.T) {
	h := &DataciteHarvester{}
	ts := time.Date(2023, 1, 15, 12, 30, 0, 0, time.UTC)

	key, start, end := h.DaySliceKey(ts, "")

	wantKey := "datacite-2023-01-15-2023-01-15.json.zst"
	if key != wantKey {
		t.Errorf("key: got %q, want %q", key, wantKey)
	}
	wantStart := time.Date(2023, 1, 15, 0, 0, 0, 0, time.UTC)
	if !start.Equal(wantStart) {
		t.Errorf("start: got %v, want %v", start, wantStart)
	}
	wantEnd := time.Date(2023, 1, 15, 23, 59, 59, 0, time.UTC)
	if !end.Equal(wantEnd) {
		t.Errorf("end: got %v, want %v", end, wantEnd)
	}
}

func TestDataciteHarvesterDaySliceKeyPrefix(t *testing.T) {
	h := &DataciteHarvester{}
	ts := time.Date(2023, 1, 15, 0, 0, 0, 0, time.UTC)
	key, _, _ := h.DaySliceKey(ts, "feed-0-")
	if !strings.HasPrefix(key, "feed-0-") {
		t.Errorf("key missing prefix: got %q", key)
	}
}

// -- DataciteHarvester.endpoint / pageSize --

func TestDataciteHarvesterDefaults(t *testing.T) {
	h := &DataciteHarvester{}
	if got := h.endpoint(); got != dataciteDefaultEndpoint {
		t.Errorf("endpoint: got %q, want %q", got, dataciteDefaultEndpoint)
	}
	if got := h.pageSize(); got != dataciteDefaultPageSize {
		t.Errorf("pageSize: got %d, want %d", got, dataciteDefaultPageSize)
	}
}

func TestDataciteHarvesterCustomEndpointAndPageSize(t *testing.T) {
	h := &DataciteHarvester{ApiEndpoint: "https://custom.example.com/dois", PageSize: 50}
	if got := h.endpoint(); got != "https://custom.example.com/dois" {
		t.Errorf("endpoint: got %q", got)
	}
	if got := h.pageSize(); got != 50 {
		t.Errorf("pageSize: got %d, want 50", got)
	}
}

// -- DataciteHarvester.WriteSlice --

func TestDataciteHarvesterWriteSliceEmpty(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, dataciteListRecords(nil, 0, ""))
	}))
	defer server.Close()

	h := &DataciteHarvester{ApiEndpoint: server.URL, Client: http.DefaultClient}
	var out strings.Builder
	from := time.Date(2023, 1, 15, 0, 0, 0, 0, time.UTC)
	until := time.Date(2023, 1, 15, 23, 59, 59, 0, time.UTC)
	if err := h.WriteSlice(&out, from, until); err != nil {
		t.Fatalf("WriteSlice: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("expected empty output for zero records, got %d bytes", out.Len())
	}
}

func TestDataciteHarvesterWriteSliceSinglePage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, dataciteListRecords([]string{mockDataciteDoc, mockDataciteDoc}, 2, ""))
	}))
	defer server.Close()

	h := &DataciteHarvester{ApiEndpoint: server.URL, Client: http.DefaultClient}
	var buf strings.Builder
	from := time.Date(2023, 1, 15, 0, 0, 0, 0, time.UTC)
	until := time.Date(2023, 1, 15, 23, 59, 59, 0, time.UTC)
	if err := h.WriteSlice(&buf, from, until); err != nil {
		t.Fatalf("WriteSlice: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}
	// Each line should be valid JSON with at least an "id" field.
	for i, line := range lines {
		var obj map[string]interface{}
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			t.Errorf("line %d is not valid JSON: %v", i, err)
		}
		if obj["id"] == nil {
			t.Errorf("line %d has no id field", i)
		}
	}
}

func TestDataciteHarvesterWriteSlicePagination(t *testing.T) {
	var callCount int32
	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&callCount, 1)
		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			next := serverURL + "?page[cursor]=abc123"
			io.WriteString(w, dataciteListRecords([]string{mockDataciteDoc}, 2, next))
		} else {
			io.WriteString(w, dataciteListRecords([]string{mockDataciteDoc}, 2, ""))
		}
	}))
	defer server.Close()
	serverURL = server.URL

	h := &DataciteHarvester{ApiEndpoint: server.URL, Client: http.DefaultClient}
	var buf strings.Builder
	from := time.Date(2023, 1, 15, 0, 0, 0, 0, time.UTC)
	until := time.Date(2023, 1, 15, 23, 59, 59, 0, time.UTC)
	if err := h.WriteSlice(&buf, from, until); err != nil {
		t.Fatalf("WriteSlice: %v", err)
	}
	if got := atomic.LoadInt32(&callCount); got != 2 {
		t.Errorf("expected 2 API requests (pagination), got %d", got)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Errorf("expected 2 records total, got %d", len(lines))
	}
}

func TestDataciteHarvesterWriteSliceHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}))
	defer server.Close()

	h := &DataciteHarvester{ApiEndpoint: server.URL, Client: http.DefaultClient}
	var buf strings.Builder
	from := time.Date(2023, 1, 15, 0, 0, 0, 0, time.UTC)
	until := time.Date(2023, 1, 15, 23, 59, 59, 0, time.UTC)
	err := h.WriteSlice(&buf, from, until)
	if err == nil {
		t.Fatal("expected error for HTTP 500, got nil")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error should mention status code, got: %v", err)
	}
}

func TestDataciteHarvesterWriteSliceQueryParams(t *testing.T) {
	var capturedURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedURL = r.URL.String()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, dataciteListRecords(nil, 0, ""))
	}))
	defer server.Close()

	h := &DataciteHarvester{ApiEndpoint: server.URL, Client: http.DefaultClient}
	from := time.Date(2023, 3, 10, 0, 0, 0, 0, time.UTC)
	until := time.Date(2023, 3, 10, 23, 59, 59, 0, time.UTC)
	h.WriteSlice(io.Discard, from, until) //nolint:errcheck

	if !strings.Contains(capturedURL, "updated") {
		t.Errorf("expected 'updated' in query, got: %s", capturedURL)
	}
	if !strings.Contains(capturedURL, "2023-03-10") {
		t.Errorf("expected date in query, got: %s", capturedURL)
	}
	if !strings.Contains(capturedURL, "page%5Bcursor%5D=1") || strings.Contains(capturedURL, "cursor%5D=1") {
		// Either encoded form is fine; just check cursor param is present.
		if !strings.Contains(capturedURL, "cursor") {
			t.Errorf("expected cursor pagination in query, got: %s", capturedURL)
		}
	}
}

// -- DataciteHarvester.WriteDaySlice --

func TestDataciteHarvesterWriteDaySliceIdempotent(t *testing.T) {
	dir := t.TempDir()
	h := &DataciteHarvester{}
	ts := time.Date(2023, 1, 15, 0, 0, 0, 0, time.UTC)
	key, _, _ := h.DaySliceKey(ts, "")
	dst := filepath.Join(dir, key)

	want := []byte("already done")
	if err := os.WriteFile(dst, want, 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if err := h.WriteDaySlice(ts, dir, ""); err != nil {
		t.Fatalf("WriteDaySlice: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("file was overwritten; got %q, want %q", got, want)
	}
}

func TestDataciteHarvesterWriteDaySlice(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, dataciteListRecords([]string{mockDataciteDoc}, 1, ""))
	}))
	defer server.Close()

	h := &DataciteHarvester{ApiEndpoint: server.URL, Client: http.DefaultClient}
	dir := t.TempDir()
	ts := time.Date(2023, 1, 15, 0, 0, 0, 0, time.UTC)

	if err := h.WriteDaySlice(ts, dir, ""); err != nil {
		t.Fatalf("WriteDaySlice: %v", err)
	}
	key, _, _ := h.DaySliceKey(ts, "")
	dst := filepath.Join(dir, key)
	if _, err := os.Stat(dst); os.IsNotExist(err) {
		t.Fatalf("expected output file %s to exist", dst)
	}

	// Verify valid zstd-compressed NDJSON.
	f, err := os.Open(dst)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	zr, err := zstd.NewReader(f)
	if err != nil {
		t.Fatalf("zstd.NewReader: %v", err)
	}
	defer zr.Close()
	var obj map[string]interface{}
	if err := json.NewDecoder(zr).Decode(&obj); err != nil {
		t.Fatalf("output is not valid JSON inside zstd: %v", err)
	}
	if obj["id"] == nil {
		t.Errorf("expected non-empty id in output record")
	}
}
