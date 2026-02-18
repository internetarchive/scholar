// Command pubmed2json converts a gzipped PubMed XML update file to
// newline-delimited JSON (.ndjson).
//
// Usage:
//
//	pubmed2json <file.xml.gz> [output.ndjson]
//
// If no output file is given, writes to stdout.
package main

import (
	"compress/gzip"
	"fmt"
	"log"
	"os"

	"pubmed2json"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: pubmed2json <file.xml.gz> [output.ndjson]\n")
		os.Exit(1)
	}

	f, err := os.Open(os.Args[1])
	if err != nil {
		log.Fatalf("opening input: %v", err)
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		log.Fatalf("creating gzip reader: %v", err)
	}
	defer gz.Close()

	var out *os.File
	if len(os.Args) >= 3 {
		out, err = os.Create(os.Args[2])
		if err != nil {
			log.Fatalf("creating output file: %v", err)
		}
		defer out.Close()
	} else {
		out = os.Stdout
	}

	stats, err := pubmed2json.Convert(gz, out)
	if err != nil {
		log.Fatalf("conversion failed: %v", err)
	}
	fmt.Fprintf(os.Stderr, "done: %d articles, %d deleteCitation records\n", stats.Articles, stats.DeleteCitations)
}
