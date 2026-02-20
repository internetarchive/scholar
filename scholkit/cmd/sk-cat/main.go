// sk-cat takes one or more links to (compressed) files and will stream
// their content to stdout. Uses curl and external compression programs.
// Nothing bash and curl could not do, but a bit shorter to type.
//
// Often, datasets come split over many files and for ad-hoc data inspection, a
// single file can be more convenient.
//
// $ cat urls.txt
// https://archive.org/download/openalex_snapshot_2023-07-11/data/works/updated_date=2023-05-09/part_000.gz
// https://archive.org/download/openalex_snapshot_2023-07-11/data/works/updated_date=2023-05-01/part_000.gz
// https://archive.org/download/openalex_snapshot_2023-07-11/data/works/updated_date=2023-04-16/part_000.gz
// ...
//
// $ cat urls.txt | sk-cat | zstd -c > data.zst
//
// Another example:
//
// Turn PubMed baseline into a single zst file (concatenated, most likely invalid XML):
//
//		$ curl -sL "https://ftp.ncbi.nlm.nih.gov/pubmed/baseline/" | \
//				pup 'a[href] text{}' | \
//		        grep -o 'pubmed.*[.]xml[.]gz' | \
//		        awk '{print "https://ftp.ncbi.nlm.nih.gov/pubmed/baseline/"$0}' | \
//				sk-cat -v | \
//	            zstd -c > pubmed.xml.zst
//
// Other datasets that come scattered across many files are wikipedia, openalex, ...
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/miku/scholkit"
)

var decompMap = map[string]string{
	"7z":  "7z e -so",
	"bz2": "pbzip2 -cd",
	"gz":  "pigz -cd",
	"lz4": "lz4 -cd",
	"rar": "unrar px",
	"zst": "zstd -cd -T0",
}

var (
	timeout      = flag.Duration("t", 3600*time.Second, "timeout for a single command")
	retry        = flag.Int("r", 5, "curl: --retry")
	retryMaxTime = flag.Int("m", 300, "curl: --retry-max-time")
	filterProg   = flag.String("F", "", "run each file through this filter command")
	verbose      = flag.Bool("v", false, "debug output")
	forceDecomp  = flag.String("d", "", "force a decompression tool: 7z, bz2, gz, rar, zst")
	autoMode     = flag.Bool("a", false, "determine compression tool by extension")
	sleep        = flag.Duration("s", 2*time.Second, "sleep duration between requests")
	showVersion  = flag.Bool("version", false, "show version")

	externalDependencies = []string{"curl", "bash"}

	help = `This utility streams content from one or more URLs. The content is optionally
decompressed and filtered and then streamed to stdout. The compression tool of
choice (7z, bz2, lz4, pigz, rar, zstd) needs to be installed on the system.

Many datasets are scattered across many files, e.g. wikipedia, pubmed, openalex
and others.

For example, pubmed (in 02/2024) is spread over 2438 gzip-compressed XML files.
We can assemble a link list and feed convert it to nice single file version on
the fly:

	$ curl -sL "https://ftp.ncbi.nlm.nih.gov/pubmed/baseline/" | \
	    pup 'a[href] text{}' | \
    	grep -o 'pubmed.*[.]xml[.]gz' | \
    	awk '{print "https://ftp.ncbi.nlm.nih.gov/pubmed/baseline/"$0}' > links.txt

    $ cat links.txt | sk-cat | zstd -c > pubmed.zst

Note: Not all file formats will be suitable for concatenation.
`
)

func main() {
	for _, p := range externalDependencies {
		if _, err := exec.LookPath(p); err != nil {
			log.Fatalf("%s required", p)
		}
	}
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, help)
		fmt.Fprintf(os.Stderr, "\n")
		fmt.Fprintf(os.Stderr, "Usage of %s:\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()
	if *showVersion {
		fmt.Println(scholkit.Version)
		os.Exit(0)
	}
	br := bufio.NewReader(os.Stdin)
	bw := bufio.NewWriter(os.Stdout)
	defer bw.Flush()
	var i int
	for {
		line, err := br.ReadString('\n')
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Fatal(err)
		}
		line = strings.TrimSpace(line)
		if len(line) == 0 || strings.HasPrefix(line, "#") {
			continue
		}
		if _, err := url.Parse(line); err != nil {
			log.Fatal(err)
		}
		i++
		var decomp string
		switch {
		case *forceDecomp != "":
			var ok bool
			decomp, ok = decompMap[*forceDecomp]
			if !ok {
				log.Fatalf("unsupported: %v", *forceDecomp)
			}
		default:
			switch {
			case strings.HasSuffix(line, ".gz"):
				decomp = decompMap["gz"]
			case strings.HasSuffix(line, ".bz2"):
				decomp = decompMap["bz2"]
			case strings.HasSuffix(line, ".7z"):
				decomp = decompMap["7z"]
			case strings.HasSuffix(line, ".zst"):
				decomp = decompMap["zst"]
			case strings.HasSuffix(line, ".rar"):
				decomp = decompMap["rar"]
			default:
				decomp = ""
			}
		}
		var script string
		switch {
		case decomp != "" && *filterProg != "":
			script = fmt.Sprintf("(set -euo pipefail; curl --retry %d --retry-max-time %d --fail -sL '%s' | %s | %s)",
				*retry, *retryMaxTime, line, decomp, *filterProg)
		case decomp == "" && *filterProg != "":
			script = fmt.Sprintf("(set -euo pipefail; curl --retry %d --retry-max-time %d --fail -sL '%s' | %s)",
				*retry, *retryMaxTime, line, *filterProg)
		case decomp != "" && *filterProg == "":
			script = fmt.Sprintf("(set -euo pipefail; curl --retry %d --retry-max-time %d --fail -sL '%s' | %s)",
				*retry, *retryMaxTime, line, decomp)
		case decomp == "" && *filterProg == "":
			script = fmt.Sprintf("(set -euo pipefail; curl --retry %d --retry-max-time %d --fail -sL '%s')",
				*retry, *retryMaxTime, line)
		}
		for j := 0; j < *retryMaxTime; j++ {
			// TODO: hitting the timeout can lead to garbled output, as we also
			// retry, but data may have already been written; mitigation for
			// now is to increase the timeout
			ctx, cancel := context.WithTimeout(context.Background(), *timeout)
			defer cancel()
			cmd := exec.CommandContext(ctx, "bash", "-c", script)
			cmd.Stdout = bw
			cmd.Stderr = os.Stderr
			if *verbose {
				log.Printf("%06d[%d] %s", i, j, cmd.String())
			}
			err := cmd.Run()
			if err == nil {
				break
			}
			if err != nil && j == *retryMaxTime {
				log.Fatal(err)
			} else {
				time.Sleep(2 * time.Second)
			}
		}
		time.Sleep(*sleep)
	}
}
