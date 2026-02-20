// sk-oai-dctojsonl converts a stream of XML records, where each record is
// separated by a record separator "1E". Currently, we do not support streaming
// data in from stdin, but need a decompressed file, as we seek through it.
//
// Raw input in 02/2025 is 1.3TB XML with 1E separators. On a 32 core machine
// conversion runs at about 500MB/s, overall it may take an hour to create a
// JSONL version.
package main

import (
	"bufio"
	"bytes"
	"encoding/xml"
	"flag"
	"io"
	"log"
	"os"
	"runtime"
	"sync"

	"github.com/segmentio/encoding/json"
)

var filename = flag.String("f", "2025-01-16-metha-oai-sep-1e.xml", "filename to process")

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

const (
	chunkSize = 67108864 // bytes per thread
	recordSep = 0x1E     // ASCII record separator
)

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

func batchWorker(queue chan []byte, resultC chan []byte, wg *sync.WaitGroup) {
	defer wg.Done()
	for batch := range queue {
		var i, j int
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		for _, b := range batch {
			j++
			if b == recordSep {
				var record Record
				_ = xml.Unmarshal(batch[i:j], &record)
				i = j + 1
				flat := convertRecord(&record)
				err := enc.Encode(flat)
				if err != nil {
					log.Fatal(err)
				}
			}
		}
		resultC <- buf.Bytes()
	}
}

func writer(resultC chan []byte, done chan bool) {
	bw := bufio.NewWriter(os.Stdout)
	defer bw.Flush()
	for blob := range resultC {
		_, _ = bw.Write(blob)
	}
	done <- true
}

func main() {
	flag.Parse()
	f, err := os.Open(*filename)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	fi, err := os.Stat(*filename)
	if err != nil {
		log.Fatal(err)
	}
	var (
		queue   = make(chan []byte)
		resultC = make(chan []byte)
		done    = make(chan bool)
		wg      sync.WaitGroup
	)
	go writer(resultC, done)
	for k := 0; k < runtime.NumCPU(); k++ {
		wg.Add(1)
		go batchWorker(queue, resultC, &wg)
	}
	var i, j, L int64 // start, stop and length of chunk
	var singleByte = make([]byte, 1)
	for i < fi.Size() {
		j = i + chunkSize
		if j > fi.Size() {
			L = j - i
			_, err := f.Seek(i, io.SeekStart)
			if err != nil {
				log.Fatal(err)
			}
			buf := make([]byte, L)
			_, err = f.Read(buf)
			if err != nil {
				log.Fatal(err)
			}
			queue <- buf
			break
		}
		_, err = f.Seek(j, io.SeekStart)
		if err != nil {
			log.Fatal(err)
		}
		for j < fi.Size() {
			_, err := f.Read(singleByte)
			if err != nil {
				log.Fatal(err)
			}
			if singleByte[0] == recordSep {
				break
			}
			j++
		}
		L = j - i
		_, err := f.Seek(i, io.SeekStart)
		if err != nil {
			log.Fatal(err)
		}
		buf := make([]byte, L)
		_, err = f.Read(buf)
		if err != nil {
			log.Fatal(err)
		}
		queue <- buf
		i = j + 1
	}
	close(queue)
	wg.Wait()
	close(resultC)
	<-done
}
