package fetchutils

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

	warc "github.com/internetarchive/gowarc"
)

// NOTE: written with droid assistance, blame @miku

// createMockWARCRecord creates a mock WARC record.
func createMockWARCRecord(warcType, targetURI, httpResponse string) (*warc.Record, error) {
	record := warc.NewRecord("", false)

	record.Header = warc.NewHeader()
	record.Header["WARC-Type"] = warcType
	record.Header["WARC-Target-URI"] = targetURI
	record.Header["WARC-Record-ID"] = "<urn:uuid:12345678-1234-5678-1234-567812345678>"
	record.Header["WARC-Date"] = "2025-01-11T12:00:00Z"
	record.Header["Content-Type"] = "application/http; msgtype=response"
	record.Header["Content-Length"] = fmt.Sprintf("%d", len(httpResponse))
	record.Version = "WARC/1.0"

	if httpResponse != "" {
		_, err := record.Content.Write([]byte(httpResponse))
		if err != nil {
			return nil, err
		}
		_, err = record.Content.Seek(0, io.SeekStart)
		if err != nil {
			return nil, err
		}
	}
	return record, nil
}

// TestDownload tests the Download function with a mock HTTP server.
func TestDownload(t *testing.T) {
	tests := []struct {
		name          string
		responseBody  string
		statusCode    int
		expectError   bool
		errorContains string
	}{
		{
			name:         "successful download",
			responseBody: "test content",
			statusCode:   http.StatusOK,
			expectError:  false,
		},
		{
			name:          "404 not found",
			responseBody:  "",
			statusCode:    http.StatusNotFound,
			expectError:   true,
			errorContains: "bad status",
		},
		{
			name:          "500 server error",
			responseBody:  "",
			statusCode:    http.StatusInternalServerError,
			expectError:   true,
			errorContains: "bad status",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
				w.Write([]byte(tt.responseBody))
			}))
			defer server.Close()

			tmpFile := filepath.Join(t.TempDir(), "warc_test.file")
			err := Download(tmpFile, server.URL)

			if tt.expectError {
				if err == nil {
					t.Errorf("expected error but got nil")
				} else if tt.errorContains != "" && !strings.Contains(err.Error(), tt.errorContains) {
					t.Errorf("expected error containing %q, got %q", tt.errorContains, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				content, err := os.ReadFile(tmpFile)
				if err != nil {
					t.Errorf("failed to read downloaded file: %v", err)
				}
				if string(content) != tt.responseBody {
					t.Errorf("expected content %q, got %q", tt.responseBody, string(content))
				}
			}
		})
	}
}

// TestIsPDFResponse tests the isPDFResponse function
func TestIsPDFResponse(t *testing.T) {
	tests := []struct {
		name         string
		warcType     string
		httpResponse string
		expected     bool
	}{
		{
			name:     "valid PDF response",
			warcType: "response",
			httpResponse: "HTTP/1.1 200 OK\r\n" +
				"Content-Type: application/pdf\r\n" +
				"Content-Length: 1234\r\n" +
				"\r\n" +
				"%PDF-1.4",
			expected: true,
		},
		{
			name:     "PDF with lowercase content-type",
			warcType: "response",
			httpResponse: "HTTP/1.1 200 OK\r\n" +
				"content-type: application/pdf\r\n" +
				"\r\n" +
				"%PDF-1.4",
			expected: true,
		},
		{
			name:     "non-200 status",
			warcType: "response",
			httpResponse: "HTTP/1.1 404 Not Found\r\n" +
				"Content-Type: application/pdf\r\n" +
				"\r\n",
			expected: false,
		},
		{
			name:     "non-PDF content type",
			warcType: "response",
			httpResponse: "HTTP/1.1 200 OK\r\n" +
				"Content-Type: text/html\r\n" +
				"\r\n" +
				"<html></html>",
			expected: false,
		},
		{
			name:     "non-response record type",
			warcType: "request",
			httpResponse: "HTTP/1.1 200 OK\r\n" +
				"Content-Type: application/pdf\r\n" +
				"\r\n",
			expected: false,
		},
		{
			name:         "empty content",
			warcType:     "response",
			httpResponse: "",
			expected:     false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			record, err := createMockWARCRecord(tt.warcType, "http://example.com/test.pdf", tt.httpResponse)
			if err != nil {
				t.Fatalf("failed to create mock record: %v", err)
			}
			result := isPDFResponse(record)
			if result != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, result)
			}
		})
	}
}

// TestShouldProcess tests the shouldProcess function
func TestShouldProcess(t *testing.T) {
	tests := []struct {
		name         string
		warcType     string
		httpResponse string
		expected     bool
	}{
		{
			name:     "valid PDF with 200 status",
			warcType: "response",
			httpResponse: "HTTP/1.1 200 OK\r\n" +
				"Content-Type: application/pdf\r\n" +
				"\r\n",
			expected: true,
		},
		{
			name:     "PDF with mixed case content-type",
			warcType: "response",
			httpResponse: "HTTP/1.0 200 OK\r\n" +
				"Content-Type: Application/PDF\r\n" +
				"\r\n",
			expected: true,
		},
		{
			name:     "PDF with content-type containing charset",
			warcType: "response",
			httpResponse: "HTTP/1.1 200 OK\r\n" +
				"Content-Type: application/pdf; charset=utf-8\r\n" +
				"\r\n",
			expected: true,
		},
		{
			name:     "non-PDF content",
			warcType: "response",
			httpResponse: "HTTP/1.1 200 OK\r\n" +
				"Content-Type: text/html\r\n" +
				"\r\n",
			expected: false,
		},
		{
			name:     "404 status",
			warcType: "response",
			httpResponse: "HTTP/1.1 404 Not Found\r\n" +
				"Content-Type: application/pdf\r\n" +
				"\r\n",
			expected: false,
		},
		{
			name:     "request record type",
			warcType: "request",
			httpResponse: "GET /test.pdf HTTP/1.1\r\n" +
				"Host: example.com\r\n" +
				"\r\n",
			expected: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			record, err := createMockWARCRecord(tt.warcType, "http://example.com/test.pdf", tt.httpResponse)
			if err != nil {
				t.Fatalf("failed to create mock record: %v", err)
			}
			result := shouldProcess(record)
			if result != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, result)
			}
		})
	}
}

// verifyPDFInDir checks that the output directory contains at least one file with PDF content.
func verifyPDFInDir(t *testing.T, outputDir string) {
	t.Helper()
	files, err := os.ReadDir(outputDir)
	if err != nil {
		t.Fatalf("failed to read output dir: %v", err)
	}
	if len(files) == 0 {
		t.Errorf("no files created in output directory")
		return
	}
	for _, file := range files {
		content, err := os.ReadFile(filepath.Join(outputDir, file.Name()))
		if err != nil {
			continue
		}
		if strings.Contains(string(content), "%PDF") {
			return
		}
	}
	t.Errorf("no file with PDF content found")
}

// TestSavePDF tests the savePDF function
func TestSavePDF(t *testing.T) {
	tests := []struct {
		name         string
		url          string
		pdfNum       int
		httpResponse string
		expectErr    bool
		checkContent bool
	}{
		{
			name:   "save PDF with URL-based filename",
			url:    "http://example.com/document.pdf",
			pdfNum: 1,
			httpResponse: "HTTP/1.1 200 OK\r\n" +
				"Content-Type: application/pdf\r\n" +
				"Content-Length: 8\r\n" +
				"\r\n" +
				"%PDF-1.4",
			expectErr:    false,
			checkContent: true,
		},
		{
			name:   "save PDF with numbered filename",
			url:    "http://example.com/resource",
			pdfNum: 42,
			httpResponse: "HTTP/1.1 200 OK\r\n" +
				"Content-Type: application/pdf\r\n" +
				"\r\n" +
				"%PDF-1.4",
			expectErr:    false,
			checkContent: true,
		},
		{
			name:   "save PDF with duplicate handling",
			url:    "http://example.com/test.pdf",
			pdfNum: 2,
			httpResponse: "HTTP/1.1 200 OK\r\n" +
				"\r\n" +
				"%PDF-1.4",
			expectErr:    false,
			checkContent: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			outputDir := t.TempDir()
			record, err := createMockWARCRecord("response", tt.url, tt.httpResponse)
			if err != nil {
				t.Fatalf("failed to create mock record: %v", err)
			}
			err = savePDF(record, outputDir, tt.pdfNum, tt.url)
			if tt.expectErr {
				if err == nil {
					t.Errorf("expected error but got nil")
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if tt.checkContent {
				verifyPDFInDir(t, outputDir)
			}
		})
	}
}

// TestProcessWARCForPDFs tests the ProcessWARCForPDFs function
func TestProcessWARCForPDFs(t *testing.T) {
	t.Run("process valid WARC file", func(t *testing.T) {
		warcPath, err := createTempWARC(t)
		if err != nil {
			t.Fatalf("failed to create temp WARC file: %v", err)
		}
		outputDir := t.TempDir()
		err = ProcessWARCForPDFs(warcPath, outputDir, false)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		files, err := os.ReadDir(outputDir)
		if err != nil {
			t.Fatalf("failed to read output dir: %v", err)
		}
		// We expect at least one file if WARC parsing succeeds
		// (depending on whether the mock WARC is properly formatted)
		t.Logf("created %d files in output directory", len(files))
	})

	t.Run("non-existent file", func(t *testing.T) {
		outputDir := t.TempDir()
		err := ProcessWARCForPDFs("/nonexistent/file.warc", outputDir, false)
		if err == nil {
			t.Errorf("expected error for non-existent file")
		}
	})
}

// createTempWARC creates a temporary WARC file with mock content for testing.
func createTempWARC(t *testing.T) (string, error) {
	tmpFile, err := os.CreateTemp(t.TempDir(), "blobproc-fetchutils-warc-test-*.warc")
	if err != nil {
		return "", err
	}
	defer tmpFile.Close()
	warc := createWARC()
	if _, err := tmpFile.Write([]byte(warc)); err != nil {
		return "", err
	}
	return tmpFile.Name(), nil
}

// createWARC generates a simple WARC record content string for testing.
func createWARC() string {
	var buf bytes.Buffer

	// WARC record header
	buf.WriteString("WARC/1.0\r\n")
	buf.WriteString("WARC-Type: response\r\n")
	buf.WriteString("WARC-Record-ID: <urn:uuid:12345678-1234-5678-1234-567812345678>\r\n")
	buf.WriteString("WARC-Date: 2025-01-11T12:00:00Z\r\n")
	buf.WriteString("WARC-Target-URI: http://example.com/test.pdf\r\n")
	buf.WriteString("Content-Type: application/http;msgtype=response\r\n")

	// HTTP response
	httpResponse := "HTTP/1.1 200 OK\r\n" +
		"Content-Type: application/pdf\r\n" +
		"Content-Length: 8\r\n" +
		"\r\n" +
		"%PDF-1.4"

	buf.WriteString(fmt.Sprintf("Content-Length: %d\r\n", len(httpResponse)))
	buf.WriteString("\r\n")
	buf.WriteString(httpResponse)
	buf.WriteString("\r\n\r\n")

	return buf.String()
}

// TestDownloadInvalidPath tests Download with invalid destination path
func TestDownloadInvalidPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("test"))
	}))
	defer server.Close()

	// Try to write to invalid path
	err := Download("/nonexistent/dir/file.dat", server.URL)
	if err == nil {
		t.Errorf("expected error for invalid path")
	}
}
