package fetchutils

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"

	warc "github.com/internetarchive/gowarc"
)

type WarcFilterFunc func(r *warc.Record) bool

type WarcFetch struct {
	Location string
	Filter   WarcFilterFunc
}

func (wf *WarcFetch) Run() error {
	if wf.Location == "" {
		return nil
	}
	var r io.Reader
	switch {
	case strings.HasPrefix(wf.Location, "http"):
		f, err := os.CreateTemp("", "blobfetch-warc-temp-*")
		if err != nil {
			return err
		}
		if err := Download(f.Name(), wf.Location); err != nil {
			return err
		}
		log.Printf("fetched file: %v", f.Name())
		r = f
	default:
		f, err := os.Open(wf.Location)
		if err != nil {
			return err
		}
		defer f.Close()
		r = f
	}
	reader, err := warc.NewReader(r)
	if err != nil {
		return err
	}
	for {
		record, err := reader.ReadRecord()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if record.Header.Get("WARC-Type") != "response" {
			continue
		}
		if shouldProcess(record) {
			fmt.Print("✅ ")
		} else {
			fmt.Print("   ")
		}
		fmt.Printf("[%s]: %s\n", record.Header.Get("WARC-Type"), record.Header.Get("WARC-Target-URI"))
	}
	return nil
}

func shouldProcess(record *warc.Record) bool {
	if record.Header.Get("WARC-Type") != "response" {
		return false
	}
	content := record.Content
	if content == nil {
		return false
	}
	if seeker, ok := content.(io.Seeker); ok {
		seeker.Seek(0, io.SeekStart)
	}
	scanner := bufio.NewScanner(content)
	if !scanner.Scan() {
		return false
	}
	statusLine := scanner.Text()
	if !strings.Contains(statusLine, "200") && !strings.Contains(statusLine, "HTTP/1.1 200") && !strings.Contains(statusLine, "HTTP/1.0 200") {
		return false
	}
	contentType := ""
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || line == "\r" {
			// End of headers
			break
		}
		if strings.HasPrefix(strings.ToLower(line), "content-type:") {
			contentType = strings.ToLower(strings.TrimSpace(line[13:]))
			break
		}
	}
	return strings.Contains(contentType, "application/pdf") ||
		strings.Contains(contentType, "pdf")
}

func Download(dst string, url string) (err error) {
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bad status: %s", resp.Status)
	}
	_, err = io.Copy(out, resp.Body)
	if err != nil {
		return err
	}
	return nil
}

// ....

func ProcessWARCForPDFs(filename, outputDir string, verbose bool) error {
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return err
	}

	file, err := os.Open(filename)
	if err != nil {
		return fmt.Errorf("open file: %w", err)
	}
	defer file.Close()

	reader, err := warc.NewReader(file)
	if err != nil {
		return fmt.Errorf("create reader: %w", err)
	}
	defer reader.Close()

	recordCount := 0
	pdfCount := 0

	for {
		record, err := reader.ReadRecord()
		if err == io.EOF {
			break
		}
		if err != nil {
			if verbose {
				log.Printf("Error reading record %d: %v", recordCount, err)
			}
			continue
		}

		recordCount++
		if verbose && recordCount%1000 == 0 {
			log.Printf("Processed %d records, found %d PDFs", recordCount, pdfCount)
		}

		if isPDFResponse(record) {
			pdfCount++
			url := record.Header.Get("WARC-Target-URI")

			if verbose {
				log.Printf("Found PDF #%d: %s", pdfCount, url)
			}

			if err := savePDF(record, outputDir, pdfCount, url); err != nil {
				if verbose {
					log.Printf("Failed to save PDF: %v", err)
				}
			}
		}
	}

	log.Printf("Finished processing %s: %d records, %d PDFs saved to %s",
		filename, recordCount, pdfCount, outputDir)
	return nil
}

func isPDFResponse(record *warc.Record) bool {
	// Only process response records
	if record.Header.Get("WARC-Type") != "response" {
		return false
	}

	if record.Content == nil {
		return false
	}

	// Read first part of content to check HTTP response
	buf := make([]byte, 2048)
	n, _ := record.Content.Read(buf)
	if n == 0 {
		return false
	}

	response := strings.ToLower(string(buf[:n]))

	// Check for 200 status and PDF content type
	return strings.Contains(response, " 200 ") &&
		strings.Contains(response, "application/pdf")
}

func savePDF(record *warc.Record, outputDir string, pdfNum int, url string) error {
	// Generate filename from URL or use number
	filename := fmt.Sprintf("pdf_%04d.pdf", pdfNum)
	if url != "" {
		parts := strings.Split(url, "/")
		if len(parts) > 0 {
			lastPart := parts[len(parts)-1]
			if strings.HasSuffix(strings.ToLower(lastPart), ".pdf") {
				filename = lastPart
			}
		}
	}

	filepath := fmt.Sprintf("%s/%s", outputDir, filename)

	// Handle duplicate filenames
	if _, err := os.Stat(filepath); err == nil {
		filepath = fmt.Sprintf("%s/pdf_%04d.pdf", outputDir, pdfNum)
	}

	outFile, err := os.Create(filepath)
	if err != nil {
		return err
	}
	defer outFile.Close()

	// Read all remaining content
	allContent, err := io.ReadAll(record.Content)
	if err != nil {
		return err
	}

	// Find end of HTTP headers
	contentStr := string(allContent)
	headerEnd := strings.Index(contentStr, "\r\n\r\n")
	if headerEnd == -1 {
		headerEnd = strings.Index(contentStr, "\n\n")
		if headerEnd != -1 {
			headerEnd += 2
		}
	} else {
		headerEnd += 4
	}

	if headerEnd == -1 || headerEnd >= len(allContent) {
		return fmt.Errorf("could not find PDF content start")
	}

	// Write PDF content
	pdfContent := allContent[headerEnd:]
	if _, err := outFile.Write(pdfContent); err != nil {
		return err
	}

	return nil
}
