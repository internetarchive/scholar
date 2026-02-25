package feeds

import (
	"bytes"
	"encoding/json"
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
	"github.com/miku/metha"
)

// -- OAI-PMH XML helpers --

const mockArxivRecord = `<record>
  <header>
    <identifier>oai:arXiv.org:2501.00001</identifier>
    <datestamp>2025-01-01</datestamp>
    <setSpec>cs</setSpec>
  </header>
  <metadata>
    <arXiv xmlns="http://arxiv.org/OAI/arXiv/">
      <id>2501.00001</id>
      <created>2025-01-01</created>
      <authors><author><keyname>Smith</keyname><forenames>John</forenames></author></authors>
      <title>Test Paper</title>
      <abstract>An abstract.</abstract>
    </arXiv>
  </metadata>
</record>`

func oaiListRecords(records []string, resumptionToken string) string {
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	sb.WriteString(`<OAI-PMH xmlns="http://www.openarchives.org/OAI/2.0/">`)
	sb.WriteString(`<responseDate>2025-01-01T00:00:00Z</responseDate>`)
	sb.WriteString(`<request verb="ListRecords" metadataPrefix="arXiv">http://example.com/oai2</request>`)
	sb.WriteString(`<ListRecords>`)
	for _, r := range records {
		sb.WriteString(r)
	}
	if resumptionToken != "" {
		sb.WriteString(`<resumptionToken>`)
		sb.WriteString(resumptionToken)
		sb.WriteString(`</resumptionToken>`)
	}
	sb.WriteString(`</ListRecords>`)
	sb.WriteString(`</OAI-PMH>`)
	return sb.String()
}

func oaiError(code, msg string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>` +
		`<OAI-PMH xmlns="http://www.openarchives.org/OAI/2.0/">` +
		`<responseDate>2025-01-01T00:00:00Z</responseDate>` +
		`<request verb="ListRecords">http://example.com/oai2</request>` +
		`<error code="` + code + `">` + msg + `</error>` +
		`</OAI-PMH>`
}

// newTestArxivClient returns a metha.Client backed by http.DefaultClient.
func newTestArxivClient() *metha.Client {
	return &metha.Client{Doer: http.DefaultClient}
}

// -- Unit tests for accessor helpers --

func TestArxivHarvesterBaseURL(t *testing.T) {
	h := &ArxivHarvester{}
	if got := h.baseURL(); got != arxivDefaultBaseURL {
		t.Errorf("default baseURL: got %q, want %q", got, arxivDefaultBaseURL)
	}
	h.BaseURL = "https://custom.example.com/oai2"
	if got := h.baseURL(); got != h.BaseURL {
		t.Errorf("custom baseURL: got %q, want %q", got, h.BaseURL)
	}
}

func TestArxivHarvesterMetadataPrefix(t *testing.T) {
	h := &ArxivHarvester{}
	if got := h.metadataPrefix(); got != arxivDefaultMetadataPrefix {
		t.Errorf("default metadataPrefix: got %q, want %q", got, arxivDefaultMetadataPrefix)
	}
	h.MetadataPrefix = "oai_dc"
	if got := h.metadataPrefix(); got != "oai_dc" {
		t.Errorf("custom metadataPrefix: got %q, want %q", got, "oai_dc")
	}
}

// -- DaySliceKey --

func TestArxivHarvesterDaySliceKey(t *testing.T) {
	h := &ArxivHarvester{}
	ts := time.Date(2025, 3, 15, 12, 30, 0, 0, time.UTC)

	key, start, end := h.DaySliceKey(ts, "")

	wantKey := "arxiv-arXiv-2025-03-15-2025-03-15.json.zst"
	if key != wantKey {
		t.Errorf("key: got %q, want %q", key, wantKey)
	}
	wantStart := time.Date(2025, 3, 15, 0, 0, 0, 0, time.UTC)
	if !start.Equal(wantStart) {
		t.Errorf("start: got %v, want %v", start, wantStart)
	}
	wantEnd := time.Date(2025, 3, 15, 23, 59, 59, 0, time.UTC)
	if !end.Equal(wantEnd) {
		t.Errorf("end: got %v, want %v", end, wantEnd)
	}
}

func TestArxivHarvesterDaySliceKeyPrefix(t *testing.T) {
	h := &ArxivHarvester{}
	ts := time.Date(2025, 3, 15, 0, 0, 0, 0, time.UTC)
	key, _, _ := h.DaySliceKey(ts, "harvest-")
	if !strings.HasPrefix(key, "harvest-") {
		t.Errorf("key with prefix: got %q, missing prefix", key)
	}
}

func TestArxivHarvesterDaySliceKeyCustomPrefix(t *testing.T) {
	h := &ArxivHarvester{MetadataPrefix: "oai_dc"}
	ts := time.Date(2025, 3, 15, 0, 0, 0, 0, time.UTC)
	key, _, _ := h.DaySliceKey(ts, "")
	if !strings.Contains(key, "oai_dc") {
		t.Errorf("key should contain metadata prefix: got %q", key)
	}
}

// -- WriteSlice tests --

func TestArxivHarvesterWriteSliceEmpty(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/xml")
		io.WriteString(w, oaiError("noRecordsMatch", "No records match"))
	}))
	defer server.Close()

	h := &ArxivHarvester{
		BaseURL: server.URL,
		Client:  newTestArxivClient(),
	}
	var buf bytes.Buffer
	from := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2025, 1, 1, 23, 59, 59, 0, time.UTC)
	if err := h.WriteSlice(&buf, from, until); err != nil {
		t.Fatalf("WriteSlice: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("expected empty output for noRecordsMatch, got %d bytes", buf.Len())
	}
}

func TestArxivHarvesterWriteSliceSingleRecord(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/xml")
		io.WriteString(w, oaiListRecords([]string{mockArxivRecord}, ""))
	}))
	defer server.Close()

	h := &ArxivHarvester{
		BaseURL: server.URL,
		Client:  newTestArxivClient(),
	}
	var buf bytes.Buffer
	from := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2025, 1, 1, 23, 59, 59, 0, time.UTC)
	if err := h.WriteSlice(&buf, from, until); err != nil {
		t.Fatalf("WriteSlice: %v", err)
	}
	if buf.Len() == 0 {
		t.Fatal("expected non-empty output")
	}
	var rec map[string]interface{}
	if err := json.NewDecoder(&buf).Decode(&rec); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	header, ok := rec["header"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected header in output, got: %v", rec)
	}
	if got := header["identifier"]; got != "oai:arXiv.org:2501.00001" {
		t.Errorf("header.identifier: got %v, want oai:arXiv.org:2501.00001", got)
	}
}

func TestArxivHarvesterWriteSliceResumption(t *testing.T) {
	const token = "RESUME_TOKEN_123"
	var callCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&callCount, 1)
		w.Header().Set("Content-Type", "text/xml")
		if n == 1 {
			// first page: return one record with a resumption token
			io.WriteString(w, oaiListRecords([]string{mockArxivRecord}, token))
		} else {
			// second page: return one record, no resumption token
			io.WriteString(w, oaiListRecords([]string{mockArxivRecord}, ""))
		}
	}))
	defer server.Close()

	h := &ArxivHarvester{
		BaseURL: server.URL,
		Client:  newTestArxivClient(),
	}
	var buf bytes.Buffer
	from := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2025, 1, 1, 23, 59, 59, 0, time.UTC)
	if err := h.WriteSlice(&buf, from, until); err != nil {
		t.Fatalf("WriteSlice: %v", err)
	}
	if got := atomic.LoadInt32(&callCount); got != 2 {
		t.Errorf("expected 2 OAI requests (pagination), got %d", got)
	}
	// Two records should have been written.
	dec := json.NewDecoder(&buf)
	var count int
	for {
		var rec map[string]interface{}
		if err := dec.Decode(&rec); err != nil {
			break
		}
		count++
	}
	if count != 2 {
		t.Errorf("expected 2 records in output, got %d", count)
	}
}

func TestArxivHarvesterWriteSliceOAIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/xml")
		io.WriteString(w, oaiError("badArgument", "The request includes illegal arguments"))
	}))
	defer server.Close()

	h := &ArxivHarvester{
		BaseURL: server.URL,
		Client:  newTestArxivClient(),
	}
	var buf bytes.Buffer
	from := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2025, 1, 1, 23, 59, 59, 0, time.UTC)
	err := h.WriteSlice(&buf, from, until)
	if err == nil {
		t.Fatal("expected error for OAI badArgument, got nil")
	}
	if !strings.Contains(err.Error(), "badArgument") {
		t.Errorf("error should mention OAI error code, got: %v", err)
	}
}

func TestArxivHarvesterWriteSliceSetPassedToRequest(t *testing.T) {
	var capturedURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedURL = r.URL.String()
		w.Header().Set("Content-Type", "text/xml")
		io.WriteString(w, oaiError("noRecordsMatch", "No records match"))
	}))
	defer server.Close()

	h := &ArxivHarvester{
		BaseURL: server.URL,
		Set:     "cs",
		Client:  newTestArxivClient(),
	}
	var buf bytes.Buffer
	from := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2025, 1, 1, 23, 59, 59, 0, time.UTC)
	h.WriteSlice(&buf, from, until) //nolint:errcheck
	if !strings.Contains(capturedURL, "set=cs") {
		t.Errorf("expected set=cs in OAI request URL, got: %s", capturedURL)
	}
}

func TestArxivHarvesterWriteSliceFromUntilPassedToRequest(t *testing.T) {
	var capturedURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedURL = r.URL.String()
		w.Header().Set("Content-Type", "text/xml")
		io.WriteString(w, oaiError("noRecordsMatch", "No records match"))
	}))
	defer server.Close()

	h := &ArxivHarvester{
		BaseURL: server.URL,
		Client:  newTestArxivClient(),
	}
	var buf bytes.Buffer
	from := time.Date(2025, 3, 10, 0, 0, 0, 0, time.UTC)
	until := time.Date(2025, 3, 10, 23, 59, 59, 0, time.UTC)
	h.WriteSlice(&buf, from, until) //nolint:errcheck
	if !strings.Contains(capturedURL, "from=2025-03-10") {
		t.Errorf("expected from=2025-03-10 in URL, got: %s", capturedURL)
	}
	if !strings.Contains(capturedURL, "until=2025-03-10") {
		t.Errorf("expected until=2025-03-10 in URL, got: %s", capturedURL)
	}
}

// -- WriteDaySlice tests --

func TestArxivHarvesterWriteDaySliceIdempotent(t *testing.T) {
	dir := t.TempDir()
	h := &ArxivHarvester{}
	ts := time.Date(2025, 3, 15, 0, 0, 0, 0, time.UTC)
	key, _, _ := h.DaySliceKey(ts, "")
	dst := filepath.Join(dir, key)

	// Pre-create the output file to simulate a previously harvested day.
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

func TestArxivHarvesterWriteDaySlice(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/xml")
		io.WriteString(w, oaiListRecords([]string{mockArxivRecord}, ""))
	}))
	defer server.Close()

	h := &ArxivHarvester{
		BaseURL: server.URL,
		Client:  newTestArxivClient(),
	}
	dir := t.TempDir()
	ts := time.Date(2025, 3, 15, 0, 0, 0, 0, time.UTC)

	if err := h.WriteDaySlice(ts, dir, ""); err != nil {
		t.Fatalf("WriteDaySlice: %v", err)
	}
	key, _, _ := h.DaySliceKey(ts, "")
	dst := filepath.Join(dir, key)
	if _, err := os.Stat(dst); os.IsNotExist(err) {
		t.Fatalf("expected output file %s to exist", dst)
	}

	// Verify the file contains valid zstd-compressed NDJSON.
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
	var rec map[string]interface{}
	if err := json.NewDecoder(zr).Decode(&rec); err != nil {
		t.Fatalf("output is not valid JSON inside zstd: %v", err)
	}
	if _, ok := rec["header"]; !ok {
		t.Errorf("expected header key in output record, got: %v", rec)
	}
}
