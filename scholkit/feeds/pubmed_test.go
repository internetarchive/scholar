package feeds

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/miku/scholkit"
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
