// sk-oai-dctojsonl converts a stream of XML records, where each record is
// separated by a record separator "1E". This version supports streaming data
// from stdin.
package main

import (
	"bufio"
	"bytes"
	"encoding/xml"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"runtime"
	"sync"

	"github.com/segmentio/encoding/json"
)

var (
	bufferSize = flag.Int("b", 8*1024*1024, "buffer size for reading chunks")
	numWorkers = flag.Int("w", runtime.NumCPU(), "number of worker goroutines")
	printStats = flag.Bool("stats", false, "print processing statistics")
)

// Record and other type definitions remain the same as before
type Record struct {
	XMLName  xml.Name `xml:"record"`
	Header   Header   `xml:"header"`
	Metadata struct {
		DC DublinCore `xml:"dc"`
	} `xml:"metadata"`
}

type Header struct {
	Status     string   `xml:"status,attr"`
	Identifier string   `xml:"identifier"`
	Datestamp  string   `xml:"datestamp"`
	SetSpec    []string `xml:"setSpec"`
}

type DublinCore struct {
	Title       []string `xml:"title"`
	Creator     []string `xml:"creator"`
	Subject     []string `xml:"subject"`
	Description []string `xml:"description"`
	Publisher   []string `xml:"publisher"`
	Contributor []string `xml:"contributor"`
	Date        []string `xml:"date"`
	Type        []string `xml:"type"`
	Format      []string `xml:"format"`
	Identifier  []string `xml:"identifier"`
	Source      []string `xml:"source"`
	Language    []string `xml:"language"`
	Relation    []string `xml:"relation"`
	Rights      []string `xml:"rights"`
}

type FlatRecord struct {
	Identifier   string   `json:"identifier"`
	Datestamp    string   `json:"datestamp"`
	SetSpec      []string `json:"set_spec"`
	Title        []string `json:"title"`
	Creator      []string `json:"creator"`
	Subject      []string `json:"subject"`
	Description  []string `json:"description"`
	Publisher    []string `json:"publisher"`
	Contributor  []string `json:"contributor"`
	Date         []string `json:"date"`
	Type         []string `json:"type"`
	Format       []string `json:"format"`
	DCIdentifier []string `json:"dc_identifier"`
	Source       []string `json:"source"`
	Language     []string `json:"language"`
	Relation     []string `json:"relation"`
	Rights       []string `json:"rights"`
}

const recordSep = 0x1E // ASCII record separator

// Stats for reporting
type Stats struct {
	mu             sync.Mutex
	BytesRead      int64
	RecordsRead    int64
	RecordsWritten int64
}

func (s *Stats) IncrementBytesRead(n int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.BytesRead += n
}

func (s *Stats) IncrementRecordsRead() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.RecordsRead++
}

func (s *Stats) IncrementRecordsWritten() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.RecordsWritten++
}

func convertRecord(record *Record) *FlatRecord {
	flat := &FlatRecord{
		Identifier:   record.Header.Identifier,
		Datestamp:    record.Header.Datestamp,
		SetSpec:      record.Header.SetSpec,
		Title:        record.Metadata.DC.Title,
		Creator:      record.Metadata.DC.Creator,
		Subject:      record.Metadata.DC.Subject,
		Description:  record.Metadata.DC.Description,
		Publisher:    record.Metadata.DC.Publisher,
		Contributor:  record.Metadata.DC.Contributor,
		Date:         record.Metadata.DC.Date,
		Type:         record.Metadata.DC.Type,
		Format:       record.Metadata.DC.Format,
		DCIdentifier: record.Metadata.DC.Identifier,
		Source:       record.Metadata.DC.Source,
		Language:     record.Metadata.DC.Language,
		Relation:     record.Metadata.DC.Relation,
		Rights:       record.Metadata.DC.Rights,
	}
	return flat
}

// splitIntoRecords the data into XML records, separated by the record separator
func splitIntoRecords(data []byte) [][]byte {
	var records [][]byte
	var start int
	for i, b := range data {
		if b == recordSep {
			if i > start {
				records = append(records, data[start:i])
			}
			start = i + 1
		}
	}
	if start < len(data) {
		records = append(records, data[start:])
	}
	return records
}

func processRecords(records [][]byte, stats *Stats) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, record := range records {
		stats.IncrementRecordsRead()
		var r Record
		err := xml.Unmarshal(record, &r)
		if err != nil {
			log.Printf("error unmarshaling record: %v", err)
			continue
		}
		flat := convertRecord(&r)
		err = enc.Encode(flat)
		if err != nil {
			return nil, fmt.Errorf("error encoding record: %w", err)
		}
		stats.IncrementRecordsWritten()
	}
	return buf.Bytes(), nil
}

func worker(jobs <-chan []byte, results chan<- []byte, wg *sync.WaitGroup, stats *Stats) {
	defer wg.Done()
	for data := range jobs {
		records := splitIntoRecords(data)
		result, err := processRecords(records, stats)
		if err != nil {
			log.Printf("error processing records: %v", err)
			continue
		}
		results <- result
	}
}

// readChunks reads from the reader and ensures chunks end with a record separator
func readChunks(r io.Reader, chunkSize int, jobs chan<- []byte, stats *Stats) error {
	buffer := make([]byte, chunkSize)
	carryover := make([]byte, 0)
	for {
		n, err := r.Read(buffer)
		if err != nil && err != io.EOF {
			return fmt.Errorf("error reading from stdin: %w", err)
		}
		if n == 0 {
			if len(carryover) > 0 {
				jobs <- carryover
				stats.IncrementBytesRead(int64(len(carryover)))
			}
			break
		}
		stats.IncrementBytesRead(int64(n))
		lastSepIdx := -1
		for i := n - 1; i >= 0; i-- {
			if buffer[i] == recordSep {
				lastSepIdx = i
				break
			}
		}
		if lastSepIdx == -1 {
			carryover = append(carryover, buffer[:n]...)
			continue
		}
		chunk := make([]byte, len(carryover)+lastSepIdx+1)
		copy(chunk, carryover)
		copy(chunk[len(carryover):], buffer[:lastSepIdx+1])
		jobs <- chunk
		carryover = make([]byte, n-(lastSepIdx+1))
		copy(carryover, buffer[lastSepIdx+1:n])
		if err == io.EOF {
			if len(carryover) > 0 {
				jobs <- carryover
			}
			break
		}
	}
	return nil
}

func main() {
	flag.Parse()
	var (
		stats   = &Stats{}
		jobs    = make(chan []byte, *numWorkers)
		results = make(chan []byte, *numWorkers)
		done    = make(chan struct{})
	)
	var wg sync.WaitGroup
	for i := 0; i < *numWorkers; i++ {
		wg.Add(1)
		go worker(jobs, results, &wg, stats)
	}
	go func() {
		bufWriter := bufio.NewWriter(os.Stdout)
		defer bufWriter.Flush()
		for result := range results {
			_, err := bufWriter.Write(result)
			if err != nil {
				log.Printf("error writing to stdout: %v", err)
			}
		}
		done <- struct{}{}
	}()
	err := readChunks(os.Stdin, *bufferSize, jobs, stats)
	if err != nil {
		log.Fatalf("error reading chunks: %v", err)
	}
	close(jobs)
	wg.Wait()
	close(results)
	<-done
	if *printStats {
		log.Printf("read: %d bytes, records read: %d, records written: %d",
			stats.BytesRead, stats.RecordsRead, stats.RecordsWritten)
	}
}
