// CLI to convert various metadata formats, mostly to fatcat entities. WIP.
//
// $ cat file | sk-convert -f openalex > out.jsonl
package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"log"
	"os"
	"runtime/pprof"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/internetarchive/scholar/scholkit"
	"github.com/internetarchive/scholar/scholkit/convert"
	"github.com/internetarchive/scholar/scholkit/parallel"
	"github.com/internetarchive/scholar/scholkit/parallel/record"
	"github.com/internetarchive/scholar/scholkit/schema/arxiv"
	"github.com/internetarchive/scholar/scholkit/schema/crossref"
	"github.com/internetarchive/scholar/scholkit/schema/datacite"
	"github.com/internetarchive/scholar/scholkit/schema/dblp"
	"github.com/internetarchive/scholar/scholkit/schema/doaj"
	"github.com/internetarchive/scholar/scholkit/schema/oaiscrape"
	"github.com/internetarchive/scholar/scholkit/schema/openalex"
	"github.com/internetarchive/scholar/scholkit/schema/pubmed"
	"github.com/internetarchive/scholar/scholkit/xmlstream"
	"github.com/segmentio/encoding/json"
)

var (
	fromFormat     = flag.String("f", "", fmt.Sprintf("source format (one of: %s)", strings.Join(availableSourceFormats, ", ")))
	toFormat       = flag.String("t", "fatcat-release", "target format, only fatcat-release for now")
	maxBytesApprox = flag.Uint("x", 1048576, "max bytes per batch for XML processing")
	batchSize      = flag.Int("b", 10000, "batch size")
	cpuprofile     = flag.String("cpuprofile", "", "file to write cpu pprof to")
	showVersion    = flag.Bool("version", false, "show version")
)

var availableSourceFormats = []string{
	"arxiv",
	"crossref",
	"datacite",
	"dblp",
	"doaj",
	"oaiscrape",
	"oai",
	"openalex",
	"pubmed",
	"fatcat-release", // for downstream tasks
}

var bufPool = sync.Pool{
	New: func() interface{} {
		var b bytes.Buffer
		return &b
	},
}

var help = fmt.Sprintf(`bibconv reshapes bibliographic data 🗃️

Current target only: "fatcat-release" entity. WIP: "fatcat-container",
"fatcat-work", "fatcat-contrib" and "fatcat-file" entities.

Examples:

    $ zstdcat pubmed.xml.zst | bibconv -f pubmed

Usage:

`)

func main() {
	flag.Usage = func() {
		fmt.Fprint(os.Stderr, help)
		flag.PrintDefaults()
	}
	flag.Parse()
	if *showVersion {
		fmt.Println(scholkit.Version)
		os.Exit(0)
	}
	if *cpuprofile != "" {
		f, err := os.Create(*cpuprofile)
		if err != nil {
			log.Fatal("could not create CPU profile: ", err)
		}
		defer f.Close() // error handling omitted for example
		if err := pprof.StartCPUProfile(f); err != nil {
			log.Fatal("could not start CPU profile: ", err)
		}
		defer pprof.StopCPUProfile()
	}
	br := bufio.NewReader(os.Stdin)
	bw := bufio.NewWriter(os.Stdout)
	defer bw.Flush()
	switch *fromFormat {
	case "fatcat-release":
		log.Fatal("todo")
		// TODO: turn release combined release entitiy into tables
	case "arxiv": // XML
		// t: 02:18 min single threaded, 8s with threads :)
		proc := record.NewProcessor(os.Stdin, os.Stdout, func(p []byte) ([]byte, error) {
			// setup new xml streaming scanner
			r := bytes.NewReader(p)
			scanner := xmlstream.NewScanner(r, new(arxiv.Record))
			// get a buffer to write result to
			buf := bufPool.Get().(*bytes.Buffer)
			buf.Reset()
			defer bufPool.Put(buf)
			var enc = json.NewEncoder(buf)
			// iterate over batch
			for scanner.Scan() {
				tag := scanner.Element()
				if record, ok := tag.(*arxiv.Record); ok {
					release, _ := convert.ArxivRecordToFatcatRelease(record)
					if err := enc.Encode(release); err != nil {
						return nil, err
					}
				}
			}
			if scanner.Err() != nil {
				return nil, scanner.Err()
			}
			return buf.Bytes(), nil
		})
		// batch XML elements, without expensive XML parsing
		ts := &record.TagSplitter{Tag: "record", MaxBytesApprox: *maxBytesApprox}
		proc.Split(ts.Split)
		if err := proc.Run(); err != nil {
			log.Fatal(err)
		}
	case "crossref": // JSON
		// 146402362 docs, result 20GB compressed, 80GB; iteration over the
		// 80GB in 51s.
		//
		// real    14m51.307s
		// user    130m17.872s
		// sys     17m40.039s
		pp := parallel.NewProcessor(br, bw, func(p []byte) ([]byte, error) {
			var work crossref.Work
			if err := json.Unmarshal(p, &work); err != nil {
				return nil, err
			}
			release, err := convert.CrossrefWorkToFatcatRelease(&work)
			if _, ok := err.(convert.Skip); ok {
				return nil, nil
			}
			if err != nil {
				return nil, fmt.Errorf("convert: %v, %v", err, string(p))
			}
			b, err := json.Marshal(release)
			b = append(b, '\n')
			return b, err
		})
		pp.BatchSize = *batchSize
		if err := pp.Run(); err != nil {
			log.Fatal(err)
		}
	case "datacite": // JSON
		pp := parallel.NewProcessor(br, bw, func(p []byte) ([]byte, error) {
			var doc datacite.Document
			if err := json.Unmarshal(p, &doc); err != nil {
				log.Printf("skipping failed doc: %v", err)
				return nil, nil
			}
			release, _ := convert.DataCiteToFatcatRelease(&doc)
			b, err := json.Marshal(release)
			b = append(b, '\n')
			return b, err
		})
		pp.BatchSize = *batchSize
		if err := pp.Run(); err != nil {
			log.Fatal(err)
		}
	case "doaj": // XML
		// t: about 2 min (45 min, single threaded)
		//
		// real    5m28.915s
		// user    57m51.091s
		// sys     1m59.721s
		//
		proc := record.NewProcessor(os.Stdin, os.Stdout, func(p []byte) ([]byte, error) {
			// setup new xml streaming scanner
			r := bytes.NewReader(p)
			scanner := xmlstream.NewScanner(r, new(doaj.Record))
			// get a buffer to write result to
			buf := bufPool.Get().(bytes.Buffer)
			defer bufPool.Put(buf)
			var enc = json.NewEncoder(&buf)
			// iterate over batch
			for scanner.Scan() {
				tag := scanner.Element()
				if record, ok := tag.(*doaj.Record); ok {
					release, _ := convert.DOAJRecordToFatcatRelease(record)
					if err := enc.Encode(release); err != nil {
						return nil, err
					}
				}
			}
			if scanner.Err() != nil {
				return nil, scanner.Err()
			}
			return buf.Bytes(), nil
		})
		// batch XML elements, without expensive XML parsing
		ts := &record.TagSplitter{Tag: "record", MaxBytesApprox: *maxBytesApprox}
		proc.Split(ts.Split)
		if err := proc.Run(); err != nil {
			log.Fatal(err)
		}
	case "oaiscrape": // JSON
		// t: about 15min
		// 0:08:31 for 326M lines
		pp := parallel.NewProcessor(br, bw, func(p []byte) ([]byte, error) {
			var doc oaiscrape.Document
			if err := json.Unmarshal(p, &doc); err != nil {
				return nil, err
			}
			release, _ := convert.OaiScrapeToFatcatRelease(&doc)
			b, err := json.Marshal(release)
			b = append(b, '\n')
			return b, err
		})
		pp.BatchSize = *batchSize
		if err := pp.Run(); err != nil {
			log.Fatal(err)
		}
	case "oai": // oai records from XML
		// t: about 20 min
		//
		// real    22m41.454s
		// user    207m46.951s
		// sys     5m31.498s
		//
		// TODO(martin): on k9: 2024/04/29 16:31:09 success: 96579282, skipped: 8311685
		// processing stops after 242GiB, but the content is actually 885GiB
		var (
			skippedCount int64 // total count of skipped records
			successCount int64 // successful conversions
		)
		proc := record.NewProcessor(os.Stdin, os.Stdout, func(p []byte) ([]byte, error) {
			// setup new xml streaming scanner
			var (
				skipped int64
				success int64
			)
			r := bytes.NewReader(p)
			scanner := xmlstream.NewScanner(r, new(oaiscrape.Record))
			scanner.Decoder.Strict = false
			// get a buffer to write result to
			buf := bufPool.Get().(*bytes.Buffer)
			buf.Reset()
			defer bufPool.Put(buf)
			var enc = json.NewEncoder(buf)
			// iterate over batch
			for scanner.Scan() {
				tag := scanner.Element()
				if article, ok := tag.(*oaiscrape.Record); ok {
					release, err := convert.OaiRecordToFatcatRelease(article)
					switch {
					case err == convert.ErrOaiDeleted:
						skipped++
						continue
					case err == convert.ErrOaiMissingTitle:
						skipped++
						continue
					case release == nil:
						skipped++
						continue
					}
					if err := enc.Encode(release); err != nil {
						return nil, err
					}
					success++
				}
			}
			if scanner.Err() != nil {
				return nil, fmt.Errorf("scan: %w", scanner.Err())
			}
			atomic.AddInt64(&skippedCount, skipped)
			atomic.AddInt64(&successCount, success)
			return buf.Bytes(), nil
		})
		// batch XML elements, without expensive XML parsing
		ts := &record.TagSplitter{Tag: "record", MaxBytesApprox: *maxBytesApprox}
		proc.Split(ts.Split)
		if err := proc.Run(); err != nil {
			log.Fatal(err)
		}
		log.Printf("success: %v, skipped: %v", successCount, skippedCount)
	case "openalex": // JSON
		// t: about 45 min
		//
		// real    42m16.220s
		// user    596m43.107s
		// sys     47m55.332s
		pp := parallel.NewProcessor(br, bw, func(p []byte) ([]byte, error) {
			var work openalex.Work
			if err := json.Unmarshal(p, &work); err != nil {
				return nil, err
			}
			release, _ := convert.OpenAlexWorkToFatcatRelease(&work)
			b, err := json.Marshal(release)
			b = append(b, '\n')
			return b, err
		})
		pp.BatchSize = *batchSize
		if err := pp.Run(); err != nil {
			log.Fatal(err)
		}
	case "pubmed": // XML
		// t: about 20 min
		//
		// real    22m41.454s
		// user    207m46.951s
		// sys     5m31.498s
		proc := record.NewProcessor(os.Stdin, os.Stdout, func(p []byte) ([]byte, error) {
			// setup new xml streaming scanner
			r := bytes.NewReader(p)
			scanner := xmlstream.NewScanner(r, new(pubmed.Article))
			scanner.Decoder.Strict = false
			// get a buffer to write result to
			buf := bufPool.Get().(bytes.Buffer)
			defer bufPool.Put(buf)
			var enc = json.NewEncoder(&buf)
			// iterate over batch
			for scanner.Scan() {
				tag := scanner.Element()
				if article, ok := tag.(*pubmed.Article); ok {
					release, _ := convert.PubmedArticleToFatcatRelease(article)
					if err := enc.Encode(release); err != nil {
						return nil, err
					}
				}
			}
			if scanner.Err() != nil {
				return nil, fmt.Errorf("scan: %w", scanner.Err())
			}
			return buf.Bytes(), nil
		})
		// batch XML elements, without expensive XML parsing
		ts := &record.TagSplitter{Tag: "PubmedArticle", MaxBytesApprox: *maxBytesApprox}
		proc.Split(ts.Split)
		if err := proc.Run(); err != nil {
			log.Fatal(err)
		}
	case "dblp": // XML
		// t: about 3 min
		proc := record.NewProcessor(os.Stdin, os.Stdout, func(p []byte) ([]byte, error) {
			// setup new xml streaming scanner
			r := bytes.NewReader(p)
			scanner := xmlstream.NewScanner(r, new(dblp.Article))
			scanner.Decoder.Strict = false
			// get a buffer to write result to
			buf := bufPool.Get().(bytes.Buffer)
			defer bufPool.Put(buf)
			var enc = json.NewEncoder(&buf)
			// iterate over batch
			for scanner.Scan() {
				tag := scanner.Element()
				if article, ok := tag.(*dblp.Article); ok {
					release, _ := convert.DBLPArticleToFatcatRelease(article)
					if err := enc.Encode(release); err != nil {
						return nil, err
					}
				}
			}
			if scanner.Err() != nil {
				return nil, fmt.Errorf("scan: %w", scanner.Err())
			}
			return buf.Bytes(), nil
		})
		// batch XML elements, without expensive XML parsing
		ts := &record.TagSplitter{Tag: "article", MaxBytesApprox: *maxBytesApprox}
		proc.Split(ts.Split)
		if err := proc.Run(); err != nil {
			log.Fatal(err)
		}
	case "":
		log.Fatalf("missing input format")
	default:
		log.Fatalf("unknown format: %s", *fromFormat)
	}
}
