package feeds

import (
	"bytes"
	"encoding/json"
	"fmt"
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

// mockDOAJJournal is a minimal DOAJ journal search result object.
const mockDOAJJournal = `{
  "id": "abc123def456",
  "last_updated": "2023-01-15T10:00:00Z",
  "created_date": "2020-06-01T12:00:00Z",
  "admin": {"ticked": true},
  "bibjson": {
    "title": "Test Journal of Science",
    "eissn": "1234-5678",
    "publisher": {"name": "Test Publisher", "country": "US"},
    "language": ["EN"],
    "keywords": ["science", "testing"],
    "subject": [{"scheme": "LCC", "code": "Q", "term": "Science"}],
    "license": [{"type": "CC BY", "url": "https://creativecommons.org/licenses/by/4.0/"}],
    "apc": {"has_apc": false},
    "preservation": {"has_preservation": true, "service": ["CLOCKSS"]}
  }
}`

// doajContainerResponse builds a JSON response envelope for the DOAJ journal
// search API. If next is non-empty it is included as the pagination link.
func doajContainerResponse(journals []string, total int64, page int, next string) string {
	results := "[" + strings.Join(journals, ",") + "]"
	n := "null"
	if next != "" {
		n = fmt.Sprintf("%q", next)
	}
	return fmt.Sprintf(`{"total":%d,"page":%d,"pageSize":%d,"results":%s,"next":%s}`,
		total, page, len(journals), results, n)
}

// -- DaySliceKey --

func TestDOAJContainerDaySliceKey(t *testing.T) {
	h := &DOAJContainerHarvester{}
	ts := time.Date(2023, 1, 15, 12, 30, 0, 0, time.UTC)

	key, start, end := h.DaySliceKey(ts, "")

	wantKey := "doaj-container-2023-01-15-2023-01-15.json.zst"
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

func TestDOAJContainerDaySliceKeyPrefix(t *testing.T) {
	h := &DOAJContainerHarvester{}
	ts := time.Date(2023, 1, 15, 0, 0, 0, 0, time.UTC)
	key, _, _ := h.DaySliceKey(ts, "harvest-")
	if !strings.HasPrefix(key, "harvest-") {
		t.Errorf("key with prefix: got %q, missing prefix", key)
	}
}

// -- WriteSlice --

func TestDOAJContainerWriteSliceEmpty(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, doajContainerResponse(nil, 0, 1, ""))
	}))
	defer server.Close()

	h := &DOAJContainerHarvester{
		ApiEndpoint:  server.URL,
		Client:       http.DefaultClient,
		RequestDelay: time.Millisecond,
	}
	var buf bytes.Buffer
	from := time.Date(2023, 1, 15, 0, 0, 0, 0, time.UTC)
	until := time.Date(2023, 1, 15, 23, 59, 59, 0, time.UTC)
	if err := h.WriteSlice(&buf, from, until); err != nil {
		t.Fatalf("WriteSlice: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("expected empty output for 0 results, got %d bytes", buf.Len())
	}
}

func TestDOAJContainerWriteSliceSinglePage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, doajContainerResponse([]string{mockDOAJJournal}, 1, 1, ""))
	}))
	defer server.Close()

	h := &DOAJContainerHarvester{
		ApiEndpoint:  server.URL,
		Client:       http.DefaultClient,
		RequestDelay: time.Millisecond,
	}
	var buf bytes.Buffer
	from := time.Date(2023, 1, 15, 0, 0, 0, 0, time.UTC)
	until := time.Date(2023, 1, 15, 23, 59, 59, 0, time.UTC)
	if err := h.WriteSlice(&buf, from, until); err != nil {
		t.Fatalf("WriteSlice: %v", err)
	}
	if buf.Len() == 0 {
		t.Fatal("expected non-empty output")
	}
	var obj map[string]any
	if err := json.NewDecoder(&buf).Decode(&obj); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if obj["id"] != "abc123def456" {
		t.Errorf("id: got %q, want abc123def456", obj["id"])
	}
	bibjson, ok := obj["bibjson"].(map[string]any)
	if !ok {
		t.Fatal("expected bibjson object")
	}
	if bibjson["title"] != "Test Journal of Science" {
		t.Errorf("title: got %q", bibjson["title"])
	}
}

func TestDOAJContainerWriteSlicePagination(t *testing.T) {
	var callCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&callCount, 1)
		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			// First page: include a next link pointing back to this server.
			next := fmt.Sprintf("http://%s/api/search/journals/query?page=2&pageSize=1", r.Host)
			fmt.Fprint(w, doajContainerResponse([]string{mockDOAJJournal}, 2, 1, next))
		} else {
			// Second page: no next link.
			fmt.Fprint(w, doajContainerResponse([]string{mockDOAJJournal}, 2, 2, ""))
		}
	}))
	defer server.Close()

	h := &DOAJContainerHarvester{
		ApiEndpoint:  server.URL,
		Client:       http.DefaultClient,
		PageSize:     1,
		RequestDelay: time.Millisecond,
	}
	var buf bytes.Buffer
	from := time.Date(2023, 1, 15, 0, 0, 0, 0, time.UTC)
	until := time.Date(2023, 1, 15, 23, 59, 59, 0, time.UTC)
	if err := h.WriteSlice(&buf, from, until); err != nil {
		t.Fatalf("WriteSlice: %v", err)
	}
	if got := atomic.LoadInt32(&callCount); got != 2 {
		t.Errorf("expected 2 API requests (pagination), got %d", got)
	}
	dec := json.NewDecoder(&buf)
	var count int
	for {
		var obj map[string]any
		if err := dec.Decode(&obj); err != nil {
			break
		}
		count++
	}
	if count != 2 {
		t.Errorf("expected 2 records in output, got %d", count)
	}
}

func TestDOAJContainerWriteSliceMaxRecords(t *testing.T) {
	var callCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&callCount, 1)
		w.Header().Set("Content-Type", "application/json")
		next := fmt.Sprintf("http://%s/api/search/journals/query?page=%d&pageSize=1", r.Host, n+1)
		fmt.Fprint(w, doajContainerResponse([]string{mockDOAJJournal}, 100, int(n), next))
	}))
	defer server.Close()

	h := &DOAJContainerHarvester{
		ApiEndpoint:  server.URL,
		Client:       http.DefaultClient,
		PageSize:     1,
		MaxRecords:   1,
		RequestDelay: time.Millisecond,
	}
	var buf bytes.Buffer
	from := time.Date(2023, 1, 15, 0, 0, 0, 0, time.UTC)
	until := time.Date(2023, 1, 15, 23, 59, 59, 0, time.UTC)
	if err := h.WriteSlice(&buf, from, until); err != nil {
		t.Fatalf("WriteSlice: %v", err)
	}
	if got := atomic.LoadInt32(&callCount); got != 1 {
		t.Errorf("expected 1 API request (sync limit), got %d", got)
	}
}

func TestDOAJContainerWriteSliceHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad request", http.StatusBadRequest)
	}))
	defer server.Close()

	h := &DOAJContainerHarvester{
		ApiEndpoint:  server.URL,
		Client:       http.DefaultClient,
		RequestDelay: time.Millisecond,
	}
	var buf bytes.Buffer
	from := time.Date(2023, 1, 15, 0, 0, 0, 0, time.UTC)
	until := time.Date(2023, 1, 15, 23, 59, 59, 0, time.UTC)
	err := h.WriteSlice(&buf, from, until)
	if err == nil {
		t.Fatal("expected error for HTTP 400, got nil")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("error should mention status code, got: %v", err)
	}
}

// -- WriteDaySlice --

func TestDOAJContainerWriteDaySliceIdempotent(t *testing.T) {
	dir := t.TempDir()
	h := &DOAJContainerHarvester{}
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

func TestDOAJContainerWriteDaySlice(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, doajContainerResponse([]string{mockDOAJJournal}, 1, 1, ""))
	}))
	defer server.Close()

	h := &DOAJContainerHarvester{
		ApiEndpoint:  server.URL,
		Client:       http.DefaultClient,
		RequestDelay: time.Millisecond,
	}
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

	// Verify the file is valid zstd-compressed NDJSON.
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
	var obj map[string]any
	if err := json.NewDecoder(zr).Decode(&obj); err != nil {
		t.Fatalf("output is not valid JSON inside zstd: %v", err)
	}
	if obj["id"] != "abc123def456" {
		t.Errorf("expected id abc123def456, got %q", obj["id"])
	}
}
