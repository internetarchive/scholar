// blobfetch finds and fetches files from archive collections to be put into a
// spool folder for postprocessing. Scope of this tool is mostly PDF discovery
// and access.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/miku/blobproc/ia"
	"github.com/miku/blobproc/warcutil"
)

const (
	appName   = "blobproc"
	extracted = "extracted"
)

var (
	fromItem       = flag.String("I", "", "item name, e.g. 'HELLO-CRAWL-2020-10-20250920135023817-00102-00152-123'")
	fromCollection = flag.String("C", "", "collection name, e.g. 'OPENALEX-CRAWL-2025-11'")
	fromWarcFile   = flag.String("W", "", "start with a local WARC file")
	outputDir      = flag.String("o", "", "[P] save extracted PDFs to directory")
	postURL        = flag.String("u", "", "[P] POST extracted PDFs to this URL")
	verbose        = flag.Bool("v", false, "[P] log each extracted item (status, size, URL)")
	quiet          = flag.Bool("q", false, "suppress processor output (disable default verbose mode)")
	timeout        = flag.Duration("T", 30*time.Second, "HTTP client timeout (e.g. 30s, 1m)")
)

// extractItemID extracts the item ID from either a full URL or just the ID
func extractItemID(input string) string {
	if strings.HasPrefix(input, "http://") || strings.HasPrefix(input, "https://") {
		parts := strings.Split(input, "/")
		for i, part := range parts {
			if part == "details" && i+1 < len(parts) {
				return parts[i+1]
			}
		}
		if len(parts) > 0 {
			return parts[len(parts)-1]
		}
	}
	return input
}

// debugProcessor takes an extracted item and does basic filtering and logging.
var debugProcessor = warcutil.FuncProcessor(func(e *warcutil.Extracted) error {
	if e.StatusCode != http.StatusOK {
		return nil
	}
	log.Println(e.StatusCode, e.Size, e.URI)
	return nil
})

// processWarcFile fetches and processes a single WARC file from the given URL.
func processWarcFile(client *http.Client, fileURL string) error {
	resp, err := client.Get(fileURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// TODO: someday, we may expose the filters as flag or other config options
	extractor := &warcutil.Extractor{
		Filters: []warcutil.ResponseFilter{
			warcutil.PDFResponseFilter,
			warcutil.NonZeroContentLengthFilter,
		},
		Processors: []warcutil.Processor{},
	}
	// add processors based on flags (can be combined)
	if *verbose || (!*quiet && *outputDir == "" && *postURL == "") {
		extractor.Processors = append(extractor.Processors, debugProcessor)
	}
	if *outputDir != "" {
		processor := &warcutil.DirProcessor{
			Dir:       *outputDir,
			Prefix:    "blobfetch-",
			Extension: ".pdf",
		}
		extractor.Processors = append(extractor.Processors, processor)
	}
	if *postURL != "" {
		httpPostProcessor := &warcutil.HttpPostProcessor{
			URL: *postURL,
		}
		extractor.Processors = append(extractor.Processors, httpPostProcessor)
	}
	log.Printf("extractor has %d processor(s)", len(extractor.Processors))
	return extractor.Extract(resp.Body)
}

// processItem fetches metadata for an item and processes all WARC files in it.
// An item will likely contain less that 50GB of data.
func processItem(client *http.Client, itemID string) error {
	// ex: https://archive.org/metadata/OPENALEX-CRAWL-2025-09-20251011130616382-07663-07716-wbgrp-crawl047
	link := fmt.Sprintf("https://archive.org/metadata/%s", itemID)
	log.Printf("fetching: %s", link)
	resp, err := client.Get(link)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%v while fetching %s", resp.StatusCode, link)
	}
	var item ia.Item
	dec := json.NewDecoder(resp.Body)
	if err := dec.Decode(&item); err != nil {
		return err
	}
	log.Printf("found %d files in %s", len(item.Files), itemID)
	// Crawl data items are about 50GB in total, with about 1GB per files,
	// with 100-10000s of PDF.
	for i, file := range item.Files {
		if !strings.HasSuffix(file.Name, ".warc.gz") {
			continue
		}
		// https://archive.org/download/OPENALEX-CRAWL-2025-09-20251011130616382-07663-07716-wbgrp-crawl047/OPENALEX-CRAWL-2025-09-20251011144946523-07666-2129926~wbgrp-crawl047.us.archive.org~8443.warc.gz
		fileURL := fmt.Sprintf("https://archive.org/download/%s/%s", itemID, file.Name)
		log.Printf("processing file %d/%d: %v", i+1, len(item.Files), fileURL)
		if err := processWarcFile(client, fileURL); err != nil {
			return err
		}
	}
	return nil
}

func main() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `blobfetch - extract PDFs from Internet Archive WARC files

Usage: blobfetch [options]

Sources (choose one):
  -C collection  Search and process all items in a collection
  -I item        Process a specific item (URL or ID)
  -W warc-file   Process a local WARC file

Processors (can be combined):
  (default)      Log extracted URLs (unless -q is used)
  -v             Verbose: log each extracted PDF (status, size, URL)
  -o dir         Save extracted PDFs to directory
  -u url         POST extracted PDFs to URL
  -q             Quiet: disable default logging

Options:
  -T duration    HTTP client timeout (default: 30s, examples: 1m, 90s)

Examples:
  # Search collection and log found PDFs (default behavior)
  blobfetch -C OPENALEX-CRAWL-2025-11

  # Save PDFs to directory with custom timeout
  blobfetch -I my-item -o /tmp/pdfs -T 1m

  # Both save and POST
  blobfetch -W file.warc.gz -o /tmp/pdfs -u http://localhost:8000/upload

  # Quiet mode with save only
  blobfetch -C my-collection -o /tmp/pdfs -q
`)
	}
	flag.Parse()

	client := &http.Client{
		Timeout: *timeout,
	}
	log.Printf("using HTTP timeout: %v", *timeout)

	switch {
	case *fromCollection != "":
		log.Printf("searching collection: %s", *fromCollection)
		items, err := ia.SearchCollection(client, *fromCollection)
		if err != nil {
			log.Fatal(err)
		}
		log.Printf("found %d items in collection %s", len(items), *fromCollection)
		for i, itemID := range items {
			log.Printf("processing item %d/%d: %s", i+1, len(items), itemID)
			if err := processItem(client, itemID); err != nil {
				log.Printf("error processing item %s: %v", itemID, err)
				continue
			}
		}
	case *fromItem != "":
		// Extract the item ID in case a full URL was provided
		itemID := extractItemID(*fromItem)
		if err := processItem(client, itemID); err != nil {
			log.Fatal(err)
		}
	case *fromWarcFile != "":
		f, err := os.Open(*fromWarcFile)
		if err != nil {
			log.Fatal(err)
		}
		defer f.Close()
		extractor := &warcutil.Extractor{
			Filters: []warcutil.ResponseFilter{
				warcutil.PDFResponseFilter,
			},
			Processors: []warcutil.Processor{},
		}
		if *verbose || (!*quiet && *outputDir == "" && *postURL == "") {
			extractor.Processors = append(extractor.Processors, debugProcessor)
		}
		if *outputDir != "" {
			processor := &warcutil.DirProcessor{
				Dir:       *outputDir,
				Prefix:    "blobfetch-",
				Extension: ".pdf",
			}
			extractor.Processors = append(extractor.Processors, processor)
		}
		if *postURL != "" {
			httpPostProcessor := &warcutil.HttpPostProcessor{
				URL: *postURL,
			}
			extractor.Processors = append(extractor.Processors, httpPostProcessor)
		}
		if err := extractor.Extract(f); err != nil {
			log.Fatal(err)
		}
	default:
		flag.Usage()
		os.Exit(1)
	}
}
