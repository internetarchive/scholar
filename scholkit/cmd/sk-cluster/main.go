// sk-cluster is a release entity clusterer. It takes release
// entities and will group similar items together. WIP.
//
// $ cat releases.ndj | sk-cluster
package main

import (
	"bufio"
	"encoding/base32"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"runtime"
	"strings"
	"sync"

	"github.com/miku/scholkit"
	"github.com/miku/scholkit/normal"
	"github.com/miku/scholkit/parallel"
	"github.com/miku/scholkit/schema/fatcat"
)

var (
	batchSize       = flag.Int("b", 20000, "batch size")
	makeTable       = flag.Bool("T", false, "releases to tabular form")
	includeBlob     = flag.Bool("I", false, "include source document as last column (base32 encoded)")
	runGroupVerify  = flag.Bool("G", false, "group and run verification on a cluster")
	groupFieldIndex = flag.Int("f", 0, "group by column given by index (starting at 1, like cut) ")
	showVersion     = flag.Bool("version", false, "show version")
)

func main() {
	flag.Parse()
	if *showVersion {
		fmt.Println(scholkit.Version)
		os.Exit(0)
	}
	switch {
	case *makeTable:
		var C = normal.ReplaceNewlineAndTab // cleanup function for ids
		var nzr = &normal.Pipeline{         // pipeline to clean titles
			Normalizer: []normal.Normalizer{
				&normal.Simple{},
				&normal.RemoveWhitespace{},
				&normal.LettersOnly{},
			},
		}
		pp := parallel.NewProcessor(os.Stdin, os.Stdout, func(p []byte) ([]byte, error) {
			var r fatcat.Release
			if err := json.Unmarshal(p, &r); err != nil {
				return nil, err
			}
			// tabularize data
			fields := []string{
				C(r.ID),
				C(r.Source),
				C(r.ExtIDs.Ark),
				C(r.ExtIDs.Arxiv),
				C(r.ExtIDs.Core),
				C(r.ExtIDs.DBLP),
				C(r.ExtIDs.DOI),
				C(r.ExtIDs.FatcatReleaseIdent),
				C(r.ExtIDs.FatcatWorkIdent),
				C(r.ExtIDs.HDL),
				C(r.ExtIDs.ISBN13),
				C(r.ExtIDs.JStor),
				C(r.ExtIDs.MAG),
				C(r.ExtIDs.MID),
				C(r.ExtIDs.OAI),
				C(r.ExtIDs.OpenAlex),
				C(r.ExtIDs.PII),
				C(r.ExtIDs.PMCID),
				C(r.ExtIDs.PMID),
				C(r.ExtIDs.WikidataQID),
				C(nzr.Normalize(r.Title)),
			}
			if *includeBlob {
				// Running this took about 25min, result is 1TB uncompressed
				// data; 230GB compressed. TODO: With base64 we could try to
				// utility AVX2 with a library like
				// https://github.com/segmentio/asm/, but base64 is probably
				// dwarfed by json encoding.
				encoded := base32.StdEncoding.EncodeToString(p)
				fields = append(fields, encoded)
			}
			b := []byte(strings.Join(fields, "\t"))
			b = append(b, '\n')
			return b, nil
		})
		pp.BatchSize = *batchSize
		if err := pp.Run(); err != nil {
			log.Fatal(err)
		}
	case *runGroupVerify && *groupFieldIndex > 0:
		// read line until we find all lines sharing the given key, then pass
		// off to verification thread
		// TODO: custom Split function, a mix of size (e.g. 16MB) and complete
		// set of keys
		// TODO: cannot use mmap, as we have a zstd file most of the time; we
		// would need to have a "streaming zstd" file "szstd" that would write
		// out a given number of bytes into a single zstd file and then
		// concatenate multiple one into one single file; would need to fix
		// some size upfront, e.g. 64MB chunks; then a 230GB file would contain
		// about 4000 chunks; we could mmap the whole file and then let threads
		// work on different parts simultaneously.
		// TODO: for now, just iterate over stdin lines

		// Setup goroutines
		var (
			queue   = make(chan [][]string)
			resultC = make(chan []GroupResult)
			done    = make(chan bool)
			wg      sync.WaitGroup
		)
		// the fan-in goroutine
		go writeWorker(resultC, done)
		// start N worker threads
		for i := 0; i < runtime.NumCPU(); i++ {
			wg.Add(1)
			go verifyWorker(queue, resultC, &wg)
		}
		// TODO: this is an inefficient way to get the key
		keyFromLine := func(line string) string {
			fields := strings.Split(line, "\t")
			if *groupFieldIndex <= len(fields) {
				return fields[*groupFieldIndex-1]
			}
			return ""
		}
		// Read from stdin, linewise; cf. https://gist.github.com/miku/2e1a9509527a547f6ffaf29e0b396de4
		scanner := bufio.NewScanner(os.Stdin)
		const bufSize = 512 * 1024 // increased, until we did not ran into overflows
		var buf = make([]byte, bufSize)
		scanner.Buffer(buf, bufSize)
		var (
			line, key    string
			batch        [][]string // a list of list of lines sharing the same key
			group        []string   // a list of lines sharing a key
			currentKey   string
			maxBatchSize = 1000000 // may be more lines
		)
		for scanner.Scan() {
			line = scanner.Text()
			key = keyFromLine(line)
			if key == "" {
				continue
			}
			if currentKey != "" && key != currentKey {
				g := make([]string, len(group))
				copy(g, group)
				batch = append(batch, g)
				group = nil
			}
			if len(batch) == maxBatchSize {
				b := make([][]string, len(batch))
				copy(b, batch)
				queue <- b
				batch = nil
			}
			group = append(group, line)
			currentKey = key
		}
		// TODO: handle last batch
		if scanner.Err() != nil {
			log.Fatal(scanner.Err())
		}
		close(queue)
		wg.Wait()
		close(resultC)
		<-done
		log.Printf("done")
	default:
		log.Printf("use -T to create a table")
	}
}

type GroupResult struct {
	// ID is some cluster id, we are not using that downstream just yet, so can
	// be anything, really.
	ID string `json:"id"`
	// Releases are ids of releases which most likely describe the same thing.
	Releases []string `json:"r"`
	Size     int      `json:"size"`
	Key      string   `json:"key"`
}

func verifyWorker(queue chan [][]string, resultC chan []GroupResult, wg *sync.WaitGroup) {
	defer wg.Done()
	// TODO: this is an inefficient way to get the key
	keyFromLine := func(line string) string {
		fields := strings.Split(line, "\t")
		if *groupFieldIndex <= len(fields) {
			return fields[*groupFieldIndex-1]
		}
		return ""
	}
	for batch := range queue {
		var result []GroupResult
		for _, g := range batch {
			key := keyFromLine(g[0])
			gr := GroupResult{
				ID:       "dummy",
				Releases: []string{"x", "y", "z"},
				Size:     len(g),
				Key:      key,
			}
			result = append(result, gr)
		}
		resultC <- result
	}
}

func writeWorker(resultC chan []GroupResult, done chan bool) {
	bw := bufio.NewWriter(os.Stdout)
	defer bw.Flush()
	enc := json.NewEncoder(bw)
	for grs := range resultC {
		for _, gr := range grs {
			_ = enc.Encode(gr)
		}
	}
	done <- true
}
