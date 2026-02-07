package warcutil

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Helper function to create a minimal WARC-formatted record
// This creates a basic WARC record that the parser can read
func createMockWARCRecord(uri, contentType, body string) []byte {
	// Create HTTP response
	httpResp := fmt.Sprintf("HTTP/1.1 200 OK\r\n"+
		"Content-Type: %s\r\n"+
		"Content-Length: %d\r\n"+
		"\r\n"+
		"%s", contentType, len(body), body)

	// Create WARC record in text format
	warc := fmt.Sprintf("WARC/1.0\r\n"+
		"WARC-Type: response\r\n"+
		"WARC-Target-URI: %s\r\n"+
		"WARC-Record-ID: <urn:uuid:12345678-1234-1234-1234-123456789012>\r\n"+
		"WARC-Date: 2024-01-01T00:00:00Z\r\n"+
		"Content-Type: application/http; msgtype=response\r\n"+
		"Content-Length: %d\r\n"+
		"\r\n"+
		"%s"+
		"\r\n\r\n", uri, len(httpResp), httpResp)

	return []byte(warc)
}

// TestExtractorBasic tests basic extraction without filters
func TestExtractorBasic(t *testing.T) {
	warcData := createMockWARCRecord(
		"http://example.com/test.pdf",
		"application/pdf",
		"%PDF-1.4 test content",
	)

	var processed []*Extracted
	processor := FuncProcessor(func(ex *Extracted) error {
		// Copy content to avoid reader issues
		content, _ := io.ReadAll(ex.Content)
		ex.Content = io.NopCloser(bytes.NewReader(content))
		processed = append(processed, ex)
		return nil
	})

	extractor := &Extractor{
		Processors: []Processor{processor},
	}

	err := extractor.Extract(bytes.NewReader(warcData))
	if err != nil {
		t.Fatalf("Extract failed: %v", err)
	}

	if len(processed) != 1 {
		t.Fatalf("Expected 1 processed record, got %d", len(processed))
	}

	if processed[0].URI != "http://example.com/test.pdf" {
		t.Errorf("Expected URI http://example.com/test.pdf, got %s", processed[0].URI)
	}

	if processed[0].ContentType != "application/pdf" {
		t.Errorf("Expected ContentType application/pdf, got %s", processed[0].ContentType)
	}
}

// TestExtractorWithPDFFilter tests PDF filtering
func TestExtractorWithPDFFilter(t *testing.T) {
	// Create two records: one PDF, one HTML
	pdfData := createMockWARCRecord(
		"http://example.com/doc.pdf",
		"application/pdf",
		"%PDF-1.4 content",
	)
	htmlData := createMockWARCRecord(
		"http://example.com/page.html",
		"text/html",
		"<html><body>test</body></html>",
	)

	// Combine WARC records
	combined := append(pdfData, htmlData...)

	var processed []string
	processor := FuncProcessor(func(ex *Extracted) error {
		processed = append(processed, ex.URI)
		return nil
	})

	extractor := &Extractor{
		Filters:    []ResponseFilter{PDFResponseFilter},
		Processors: []Processor{processor},
	}

	err := extractor.Extract(bytes.NewReader(combined))
	if err != nil {
		t.Fatalf("Extract failed: %v", err)
	}

	// Should only process PDF
	if len(processed) != 1 {
		t.Fatalf("Expected 1 processed record, got %d", len(processed))
	}

	if !strings.Contains(processed[0], "doc.pdf") {
		t.Errorf("Expected PDF file to be processed, got %s", processed[0])
	}
}

// TestExtractorMultipleFilters tests multiple filters
func TestExtractorMultipleFilters(t *testing.T) {
	warcData := createMockWARCRecord(
		"http://example.com/test.pdf",
		"application/pdf",
		"%PDF-1.4 content",
	)

	filterCalled := 0
	filter1 := func(resp *http.Response) bool {
		filterCalled++
		return true
	}
	filter2 := func(resp *http.Response) bool {
		filterCalled++
		return false // This should prevent processing
	}

	var processed int
	processor := FuncProcessor(func(ex *Extracted) error {
		processed++
		return nil
	})

	extractor := &Extractor{
		Filters:    []ResponseFilter{filter1, filter2},
		Processors: []Processor{processor},
	}

	err := extractor.Extract(bytes.NewReader(warcData))
	if err != nil {
		t.Fatalf("Extract failed: %v", err)
	}

	if filterCalled != 2 {
		t.Errorf("Expected 2 filter calls, got %d", filterCalled)
	}

	if processed != 0 {
		t.Errorf("Expected 0 processed records (filtered out), got %d", processed)
	}
}

// TestDirProcessor tests writing files to a directory
func TestDirProcessor(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "warcutil-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	processor := &DirProcessor{
		Dir:       tempDir,
		Prefix:    "test-",
		Extension: ".pdf",
	}

	testContent := "%PDF-1.4 test content"
	extracted := &Extracted{
		URI:         "http://example.com/test.pdf",
		ContentType: "application/pdf",
		Content:     io.NopCloser(strings.NewReader(testContent)),
		Size:        int64(len(testContent)),
	}

	err = processor.Process(extracted)
	if err != nil {
		t.Fatalf("DirProcessor.Process failed: %v", err)
	}

	// Check that a file was created
	files, err := os.ReadDir(tempDir)
	if err != nil {
		t.Fatalf("Failed to read temp dir: %v", err)
	}

	if len(files) != 1 {
		t.Fatalf("Expected 1 file, got %d", len(files))
	}

	filename := files[0].Name()
	if !strings.HasPrefix(filename, "test-") {
		t.Errorf("Expected filename to have prefix 'test-', got %s", filename)
	}

	if !strings.HasSuffix(filename, ".pdf") {
		t.Errorf("Expected filename to have suffix '.pdf', got %s", filename)
	}

	// Verify content
	content, err := os.ReadFile(filepath.Join(tempDir, filename))
	if err != nil {
		t.Fatalf("Failed to read created file: %v", err)
	}

	if string(content) != testContent {
		t.Errorf("Expected content %q, got %q", testContent, string(content))
	}
}

// MockHTTPClient implements Doer interface for testing
type MockHTTPClient struct {
	DoFunc func(*http.Request) (*http.Response, error)
}

func (m *MockHTTPClient) Do(req *http.Request) (*http.Response, error) {
	if m.DoFunc != nil {
		return m.DoFunc(req)
	}
	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader("OK")),
	}, nil
}

// TestHttpPostProcessor tests HTTP POST processing
func TestHttpPostProcessor(t *testing.T) {
	var capturedRequest *http.Request
	var capturedBody []byte

	mockClient := &MockHTTPClient{
		DoFunc: func(req *http.Request) (*http.Response, error) {
			capturedRequest = req
			capturedBody, _ = io.ReadAll(req.Body)
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(strings.NewReader("OK")),
			}, nil
		},
	}

	processor := &HttpPostProcessor{
		URL:    "http://localhost:8080/process",
		Client: mockClient,
	}

	testContent := "test PDF content"
	extracted := &Extracted{
		URI:         "http://example.com/doc.pdf",
		ContentType: "application/pdf",
		Content:     io.NopCloser(strings.NewReader(testContent)),
		Size:        int64(len(testContent)),
	}

	err := processor.Process(extracted)
	if err != nil {
		t.Fatalf("HttpPostProcessor.Process failed: %v", err)
	}

	if capturedRequest == nil {
		t.Fatal("Expected request to be captured")
	}

	if capturedRequest.Method != "POST" {
		t.Errorf("Expected POST method, got %s", capturedRequest.Method)
	}

	if capturedRequest.Header.Get("Content-Type") != "application/pdf" {
		t.Errorf("Expected Content-Type application/pdf, got %s",
			capturedRequest.Header.Get("Content-Type"))
	}

	if capturedRequest.Header.Get("X-BLOBPROC-URL") != "http://example.com/doc.pdf" {
		t.Errorf("Expected X-BLOBPROC-URL header, got %s",
			capturedRequest.Header.Get("X-BLOBPROC-URL"))
	}

	if string(capturedBody) != testContent {
		t.Errorf("Expected body %q, got %q", testContent, string(capturedBody))
	}
}

// TestHttpPostProcessorError tests error handling
func TestHttpPostProcessorError(t *testing.T) {
	mockClient := &MockHTTPClient{
		DoFunc: func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: 500,
				Body:       io.NopCloser(strings.NewReader("Internal Server Error")),
			}, nil
		},
	}

	processor := &HttpPostProcessor{
		URL:    "http://localhost:8080/process",
		Client: mockClient,
	}

	extracted := &Extracted{
		URI:         "http://example.com/doc.pdf",
		ContentType: "application/pdf",
		Content:     io.NopCloser(strings.NewReader("test")),
		Size:        4,
	}

	err := processor.Process(extracted)
	if err == nil {
		t.Fatal("Expected error for 500 status code")
	}

	if !strings.Contains(err.Error(), "500") {
		t.Errorf("Expected error to mention 500, got: %v", err)
	}
}

// TestHttpPostProcessorDefaultClient tests default client usage
func TestHttpPostProcessorDefaultClient(t *testing.T) {
	// Create a test server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("Expected POST, got %s", r.Method)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	processor := &HttpPostProcessor{
		URL: ts.URL,
		// Client is nil, should use default
	}

	extracted := &Extracted{
		URI:         "http://example.com/doc.pdf",
		ContentType: "application/pdf",
		Content:     io.NopCloser(strings.NewReader("test")),
		Size:        4,
	}

	err := processor.Process(extracted)
	if err != nil {
		t.Fatalf("HttpPostProcessor.Process failed: %v", err)
	}
}

// TestPDFResponseFilter tests the PDF filter function
func TestPDFResponseFilter(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		expected    bool
	}{
		{"PDF exact", "application/pdf", true},
		{"PDF with charset", "application/pdf; charset=utf-8", true},
		{"HTML", "text/html", false},
		{"JSON", "application/json", false},
		{"Empty", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &http.Response{
				Header: http.Header{},
			}
			resp.Header.Set("Content-Type", tt.contentType)

			result := PDFResponseFilter(resp)
			if result != tt.expected {
				t.Errorf("PDFResponseFilter(%q) = %v, expected %v",
					tt.contentType, result, tt.expected)
			}
		})
	}
}

// TestExtractorProcessorError tests error handling in processors
func TestExtractorProcessorError(t *testing.T) {
	warcData := createMockWARCRecord(
		"http://example.com/test.pdf",
		"application/pdf",
		"test content",
	)

	expectedErr := fmt.Errorf("processor error")
	processor := FuncProcessor(func(ex *Extracted) error {
		return expectedErr
	})

	extractor := &Extractor{
		Processors: []Processor{processor},
	}

	err := extractor.Extract(bytes.NewReader(warcData))
	if err == nil {
		t.Fatal("Expected error from processor")
	}

	if err != expectedErr {
		t.Errorf("Expected error %v, got %v", expectedErr, err)
	}
}

// TestExtractorEmptyWARC tests handling of empty WARC files
func TestExtractorEmptyWARC(t *testing.T) {
	processed := 0
	processor := FuncProcessor(func(ex *Extracted) error {
		processed++
		return nil
	})

	extractor := &Extractor{
		Processors: []Processor{processor},
	}

	// Create an empty reader
	err := extractor.Extract(bytes.NewReader([]byte{}))
	// Empty WARC should either succeed with no records or return an error
	if err != nil && err != io.EOF {
		// Some WARC parsers may return an error for empty input, which is acceptable
		t.Logf("Extract returned error for empty input: %v (acceptable)", err)
	}

	if processed != 0 {
		t.Errorf("Expected 0 processed records, got %d", processed)
	}
}

// TestDebugProcessor tests the debug processor
func TestDebugProcessor(t *testing.T) {
	extracted := &Extracted{
		URI:         "http://example.com/test.pdf",
		ContentType: "application/pdf",
		Content:     io.NopCloser(strings.NewReader("test")),
		Size:        4,
	}

	// DebugProcessor should not return an error
	err := DebugProcessor.Process(extracted)
	if err != nil {
		t.Errorf("DebugProcessor returned error: %v", err)
	}
}
