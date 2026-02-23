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
	"testing"
	"time"

	"github.com/internetarchive/scholar/scholkit"
)

// mockHTML is a simple representation of the PubMed files HTML listing
const mockHTML = `<!DOCTYPE HTML PUBLIC "-//W3C//DTD HTML 3.2 Final//EN">
<html>
 <head>
   <title>Index of /pubmed/updatefiles</title>
    </head>
	 <body>
	 <h1>Index of /pubmed/updatefiles</h1>
	 <pre>Name                     Last modified      Size  <hr><a href="/pubmed/">Parent Directory</a>                              -
	 <a href="README.txt">README.txt</a>               2025-01-10 10:29  4.5K
	 <a href="pubmed25n1275.xml.gz">pubmed25n1275.xml.gz</a>     2025-01-10 14:05   83M
	 <a href="pubmed25n1275.xml.gz.md5">pubmed25n1275.xml.gz.md5</a> 2025-01-10 14:05   60
	 <a href="pubmed25n1275_stats.html">pubmed25n1275_stats.html</a> 2025-01-10 14:05  585
	 <a href="pubmed25n1276.xml.gz">pubmed25n1276.xml.gz</a>     2025-01-15 14:05   19M
	 </pre>
	 </body>
	 </html>
`

// setupTestServer creates a test HTTP server that serves the mock HTML
func setupTestServer() *httptest.Server {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, mockHTML)
	}))
	return server
}

func TestNewPubMedFetcher(t *testing.T) {
	baseURL := "https://example.com/pubmed/updatefiles/"
	fetcher, err := NewPubMedFetcher(baseURL)
	if err != nil {
		t.Fatalf("failed to create fetcher: %v", err)
	}
	if fetcher.BaseURL != baseURL {
		t.Errorf("got %s, want %s", fetcher.BaseURL, baseURL)
	}
	if fetcher.CacheTTL != DefaultCacheTTL {
		t.Errorf("got %v, want %v", fetcher.CacheTTL, DefaultCacheTTL)
	}
	if _, err := os.Stat(fetcher.CacheDir); os.IsNotExist(err) {
		t.Errorf("cache dir not created: %v", err)
	}
	if !strings.Contains(fetcher.CacheDir, scholkit.AppName) {
		t.Errorf("cache dir does not contain app name, got %s", fetcher.CacheDir)
	}
}

func TestFetchIndex(t *testing.T) {
	server := setupTestServer()
	defer server.Close()
	cacheDir := t.TempDir()
	fetcher := &PubMedFetcher{
		BaseURL:  server.URL + "/",
		CacheTTL: DefaultCacheTTL,
		CacheDir: cacheDir,
	}
	content, err := fetcher.fetchIndex()
	if err != nil {
		t.Fatalf("failed to fetch index: %v", err)
	}
	if !strings.Contains(string(content), "pubmed25n1275.xml.gz") {
		t.Errorf("content does not include expected file")
	}
	cacheFile := filepath.Join(cacheDir, "pubmed_index.html")
	if _, err := os.Stat(cacheFile); os.IsNotExist(err) {
		t.Errorf("cached file was not cached")
	}
	cached, err := fetcher.fetchIndex()
	if err != nil {
		t.Fatalf("failed to fetch from cache: %v", err)
	}
	if string(cached) != string(content) {
		t.Errorf("cache and content differ, cache: %v, content: %v", cached, content)
	}
}

func TestFetchFiles(t *testing.T) {
	server := setupTestServer()
	defer server.Close()
	cacheDir := t.TempDir()
	fetcher := &PubMedFetcher{
		BaseURL:  server.URL + "/",
		CacheTTL: DefaultCacheTTL,
		CacheDir: cacheDir,
	}
	files, err := fetcher.FetchFiles()
	if err != nil {
		t.Fatalf("failed to fetch files: %v", err)
	}
	expectedCount := 2 // in mock
	if len(files) != expectedCount {
		t.Errorf("got %d files, want %d", len(files), expectedCount)
	}
	if len(files) > 0 {
		expected := PubMedFile{
			Filename: "pubmed25n1275.xml.gz",
			URL:      server.URL + "/pubmed25n1275.xml.gz",
			Size:     "83M",
		}
		expectedTime, _ := parseLastModified("2025-01-10 14:05")
		if files[0].Filename != expected.Filename {
			t.Errorf("filename, got %s, want %s", files[0].Filename, expected.Filename)
		}
		if files[0].URL != expected.URL {
			t.Errorf("URL, got %s, got %s", files[0].URL, expected.URL)
		}
		if files[0].Size != expected.Size {
			t.Errorf("size, got %s, want %s", files[0].Size, expected.Size)
		}
		if !files[0].LastModified.Equal(expectedTime) {
			t.Errorf("last modified got %v, want %v", files[0].LastModified, expectedTime)
		}
	}
}

func TestFilterPubmedFiles(t *testing.T) {
	files := []PubMedFile{
		{Filename: "pubmed25n1275.xml.gz", Size: "83M"},
		{Filename: "pubmed25n1275.xml.gz.md5", Size: "60"},
		{Filename: "pubmed25n1275_stats.html", Size: "585"},
		{Filename: "pubmed25n1276.xml.gz", Size: "19M"},
	}

	xmlFilter := func(file PubMedFile) bool {
		return strings.HasSuffix(file.Filename, ".xml.gz")
	}
	xmlFiles := FilterPubmedFiles(files, xmlFilter)
	if len(xmlFiles) != 2 {
		t.Errorf("expected 2 XML files, got %d", len(xmlFiles))
	}
	md5Filter := func(file PubMedFile) bool {
		return strings.HasSuffix(file.Filename, ".md5")
	}
	md5Files := FilterPubmedFiles(files, md5Filter)
	if len(md5Files) != 1 {
		t.Errorf("expected 1 MD5 file, got %d", len(md5Files))
	}
	sizeFilter := func(file PubMedFile) bool {
		return strings.HasSuffix(file.Size, "M") && file.Size > "50M"
	}
	largeFiles := FilterPubmedFiles(files, sizeFilter)
	if len(largeFiles) != 1 {
		t.Errorf("expected 1 large file, got %d", len(largeFiles))
	}
}

// TestCacheExpiration tests that expired cache is refreshed
func TestCacheExpiration(t *testing.T) {
	server := setupTestServer()
	defer server.Close()
	cacheDir := t.TempDir()
	fetcher := &PubMedFetcher{
		BaseURL:  server.URL + "/",
		CacheTTL: 10 * time.Millisecond, // short TTL for testing
		CacheDir: cacheDir,
	}
	_, err := fetcher.fetchIndex()
	if err != nil {
		t.Fatalf("failed to fetch index: %v", err)
	}
	cacheFile := filepath.Join(cacheDir, "pubmed_index.html")
	if _, err := os.Stat(cacheFile); os.IsNotExist(err) {
		t.Errorf("cache file was not created")
	}
	time.Sleep(20 * time.Millisecond)
	_, err = fetcher.fetchIndex()
	if err != nil {
		t.Fatalf("failed to fetch index after cache expiration: %v", err)
	}
	info, err := os.Stat(cacheFile)
	if err != nil {
		t.Fatalf("failed to stat cache file: %v", err)
	}
	if time.Since(info.ModTime()) > 15*time.Millisecond {
		t.Errorf("cache file was not updated after expiration")
	}
}

// --- PubMedHarvester tests ---

// mockPubMedXML is a minimal valid PubMed XML document usable as a mock efetch response.
const mockPubMedXML = `<?xml version="1.0" encoding="UTF-8"?>
<PubmedArticleSet>
<PubmedArticle>
  <MedlineCitation Status="MEDLINE" Owner="NLM">
    <PMID Version="1">12345678</PMID>
    <Article PubModel="Print">
      <Journal>
        <JournalIssue CitedMedium="Internet">
          <PubDate><Year>2025</Year></PubDate>
        </JournalIssue>
        <Title>Test Journal</Title>
      </Journal>
      <ArticleTitle>Test Article Title</ArticleTitle>
    </Article>
    <MedlineJournalInfo></MedlineJournalInfo>
  </MedlineCitation>
</PubmedArticle>
</PubmedArticleSet>`

// esearchJSON returns a minimal NCBI esearch JSON response with the given total count.
func esearchJSON(count string) string {
	return `{"esearchresult":{"count":"` + count + `","retmax":"0","retstart":"0","idlist":[],"webenv":"WEBENV","querykey":"1"}}`
}

// httpResp builds a minimal *http.Response for use in mock Doer implementations.
func httpResp(code int, body string) *http.Response {
	return &http.Response{
		StatusCode: code,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

// dispatchDoer routes requests to handlers by matching a substring of the URL.
type dispatchDoer struct {
	routes []dispatchRoute
}

type dispatchRoute struct {
	match   string
	handler func(*http.Request) (*http.Response, error)
}

func (d *dispatchDoer) Do(req *http.Request) (*http.Response, error) {
	for _, r := range d.routes {
		if strings.Contains(req.URL.String(), r.match) {
			return r.handler(req)
		}
	}
	return &http.Response{
		StatusCode: 404,
		Body:       io.NopCloser(strings.NewReader("not found")),
		Header:     make(http.Header),
	}, nil
}

// newTestHarvester creates a PubMedHarvester wired to doer with rate-limiting suppressed.
func newTestHarvester(doer Doer) *PubMedHarvester {
	h := &PubMedHarvester{
		Client:    doer,
		ApiBase:   "https://eutils.example.com/entrez/eutils",
		ApiKey:    "testkey", // 110 ms rate interval (faster than the keyless 370 ms)
		BatchSize: 200,
	}
	h.lastReq = time.Now().Add(-time.Hour) // prevent sleeping on the first request
	return h
}

func TestPubMedHarvesterRateInterval(t *testing.T) {
	h := &PubMedHarvester{}
	if got := h.rateInterval(); got != 370*time.Millisecond {
		t.Errorf("without key: got %v, want %v", got, 370*time.Millisecond)
	}
	h.ApiKey = "somekey"
	if got := h.rateInterval(); got != 110*time.Millisecond {
		t.Errorf("with key: got %v, want %v", got, 110*time.Millisecond)
	}
}

func TestPubMedHarvesterBase(t *testing.T) {
	h := &PubMedHarvester{}
	const want = "https://eutils.ncbi.nlm.nih.gov/entrez/eutils"
	if got := h.base(); got != want {
		t.Errorf("default base: got %q, want %q", got, want)
	}
	h.ApiBase = "https://custom.example.com/eutils"
	if got := h.base(); got != h.ApiBase {
		t.Errorf("custom base: got %q, want %q", got, h.ApiBase)
	}
}

func TestPubMedHarvesterDaySliceKey(t *testing.T) {
	h := &PubMedHarvester{}
	ts := time.Date(2025, 3, 15, 12, 30, 0, 0, time.UTC)

	key, start, end := h.DaySliceKey(ts, "")

	wantKey := "mdat-2025-03-15-2025-03-15.json.zst"
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

	keyPfx, _, _ := h.DaySliceKey(ts, "harvest-")
	if !strings.HasPrefix(keyPfx, "harvest-") {
		t.Errorf("key with prefix: got %q", keyPfx)
	}
}

func TestPubMedHarvesterWriteSliceEmpty(t *testing.T) {
	doer := &dispatchDoer{routes: []dispatchRoute{
		{"esearch", func(r *http.Request) (*http.Response, error) {
			return httpResp(200, esearchJSON("0")), nil
		}},
	}}
	h := newTestHarvester(doer)
	var buf bytes.Buffer
	if err := h.WriteSlice(&buf, time.Now().Add(-24*time.Hour), time.Now()); err != nil {
		t.Fatalf("WriteSlice: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("expected empty output for 0 results, got %d bytes", buf.Len())
	}
}

func TestPubMedHarvesterWriteSlice(t *testing.T) {
	doer := &dispatchDoer{routes: []dispatchRoute{
		{"esearch", func(r *http.Request) (*http.Response, error) {
			return httpResp(200, esearchJSON("1")), nil
		}},
		{"efetch", func(r *http.Request) (*http.Response, error) {
			return httpResp(200, mockPubMedXML), nil
		}},
	}}
	h := newTestHarvester(doer)
	var buf bytes.Buffer
	if err := h.WriteSlice(&buf, time.Now().Add(-24*time.Hour), time.Now()); err != nil {
		t.Fatalf("WriteSlice: %v", err)
	}
	if buf.Len() == 0 {
		t.Fatal("expected non-empty output")
	}
	var rec map[string]interface{}
	if err := json.NewDecoder(&buf).Decode(&rec); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if rec["type"] != "article" {
		t.Errorf(`type: got %v, want "article"`, rec["type"])
	}
}

func TestPubMedHarvesterWriteSliceMultiBatch(t *testing.T) {
	efetchCalls := 0
	doer := &dispatchDoer{routes: []dispatchRoute{
		{"esearch", func(r *http.Request) (*http.Response, error) {
			return httpResp(200, esearchJSON("5")), nil
		}},
		{"efetch", func(r *http.Request) (*http.Response, error) {
			efetchCalls++
			return httpResp(200, mockPubMedXML), nil
		}},
	}}
	h := newTestHarvester(doer)
	h.BatchSize = 2 // ceil(5/2) = 3 batches
	var buf bytes.Buffer
	if err := h.WriteSlice(&buf, time.Now().Add(-24*time.Hour), time.Now()); err != nil {
		t.Fatalf("WriteSlice: %v", err)
	}
	if efetchCalls != 3 {
		t.Errorf("expected 3 efetch calls for 5 records with batchSize=2, got %d", efetchCalls)
	}
}

func TestPubMedHarvesterWriteSliceEsearchError(t *testing.T) {
	doer := &dispatchDoer{routes: []dispatchRoute{
		{"esearch", func(r *http.Request) (*http.Response, error) {
			return httpResp(429, "rate limited"), nil
		}},
	}}
	h := newTestHarvester(doer)
	var buf bytes.Buffer
	err := h.WriteSlice(&buf, time.Now().Add(-24*time.Hour), time.Now())
	if err == nil {
		t.Fatal("expected error for HTTP 429, got nil")
	}
	if !strings.Contains(err.Error(), "429") {
		t.Errorf("error should mention status code, got: %v", err)
	}
}

func TestPubMedHarvesterWriteSliceEfetchError(t *testing.T) {
	doer := &dispatchDoer{routes: []dispatchRoute{
		{"esearch", func(r *http.Request) (*http.Response, error) {
			return httpResp(200, esearchJSON("1")), nil
		}},
		{"efetch", func(r *http.Request) (*http.Response, error) {
			return httpResp(400, "bad request"), nil
		}},
	}}
	h := newTestHarvester(doer)
	var buf bytes.Buffer
	err := h.WriteSlice(&buf, time.Now().Add(-24*time.Hour), time.Now())
	if err == nil {
		t.Fatal("expected error for HTTP 400, got nil")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("error should mention status code, got: %v", err)
	}
}

func TestPubMedHarvesterWriteSliceExceedsRetStartLimit(t *testing.T) {
	// When total > pubmedMaxRetStart the harvester should cap retrieval and
	// not attempt a retstart >= pubmedMaxRetStart.
	efetchCalls := 0
	doer := &dispatchDoer{routes: []dispatchRoute{
		{"esearch", func(r *http.Request) (*http.Response, error) {
			return httpResp(200, esearchJSON("15000")), nil // well above 9999
		}},
		{"efetch", func(r *http.Request) (*http.Response, error) {
			efetchCalls++
			return httpResp(200, mockPubMedXML), nil
		}},
	}}
	h := newTestHarvester(doer)
	h.BatchSize = pubmedMaxRetStart // one big batch covers the entire fetchable window
	var buf bytes.Buffer
	if err := h.WriteSlice(&buf, time.Now().Add(-24*time.Hour), time.Now()); err != nil {
		t.Fatalf("WriteSlice: %v", err)
	}
	if efetchCalls != 1 {
		t.Errorf("expected 1 efetch call when capped at retstart limit, got %d", efetchCalls)
	}
}

func TestPubMedHarvesterWriteSliceApiKeyInRequest(t *testing.T) {
	var capturedURL string
	doer := &dispatchDoer{routes: []dispatchRoute{
		{"esearch", func(r *http.Request) (*http.Response, error) {
			capturedURL = r.URL.String()
			return httpResp(200, esearchJSON("0")), nil
		}},
	}}
	h := newTestHarvester(doer)
	h.ApiKey = "myapikey123"
	var buf bytes.Buffer
	h.WriteSlice(&buf, time.Now().Add(-24*time.Hour), time.Now()) //nolint:errcheck
	if !strings.Contains(capturedURL, "api_key=myapikey123") {
		t.Errorf("expected api_key in esearch URL, got: %s", capturedURL)
	}
}

func TestPubMedHarvesterWriteDaySliceIdempotent(t *testing.T) {
	dir := t.TempDir()
	h := &PubMedHarvester{}
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

func TestPubMedHarvesterWriteDaySlice(t *testing.T) {
	doer := &dispatchDoer{routes: []dispatchRoute{
		{"esearch", func(r *http.Request) (*http.Response, error) {
			return httpResp(200, esearchJSON("1")), nil
		}},
		{"efetch", func(r *http.Request) (*http.Response, error) {
			return httpResp(200, mockPubMedXML), nil
		}},
	}}
	h := newTestHarvester(doer)
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
}
