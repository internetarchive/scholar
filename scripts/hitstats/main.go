package main

import (
	"encoding/csv"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
)

var signal int
var threshold float64

func init() {
	flag.IntVar(&signal, "s", 100, "how many requests required to consider a domain")
	flag.Float64Var(&threshold, "t", 50.0, "percentage of hits to pivot on")
}

type record struct {
	BaseURL     string
	TerminalURL string
	Hit         bool
}

// stat holds statistics for a domain
type stat struct {
	// Total is the number of times we tried to find a pdf at this domain
	Total int
	// Number of times we found a PDF at terminal url
	Hits int
	// Number of times we failed to find a PDF at terminal url
	Misses int
	// DirectHits is a count of how many times base url == terminal url on a pdf
	// hit; ie, how often does this domain lead to direct PDF downloads?
	DirectHits int
}

func main() {
	flag.Parse()
	stats := map[string]stat{}
	overF, err := os.Create("over.tsv")
	if err != nil {
		panic(err)
	}

	var viewcontentTotal int
	var viewcontentHits int

	underF, err := os.Create("under.tsv")
	if err != nil {
		panic(err)
	}
	r := csv.NewReader(os.Stdin)
	overW := csv.NewWriter(overF)
	underW := csv.NewWriter(underF)
	overW.Comma = '\t'
	underW.Comma = '\t'
	r.Comma = '\t'

	for {
		line, err := r.Read()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				panic(err)
			}
			break
		}

		// skip header
		if line[0] == "base_url" {
			continue
		}

		rec := record{
			BaseURL:     line[0],
			TerminalURL: line[1],
		}

		rec.Hit = line[2] == "t"

		if strings.Contains(rec.TerminalURL, "://%20doi.org") {
			fmt.Fprintf(os.Stderr, "bad doi hostname: %s\n", rec.TerminalURL)
		}

		u, err := url.Parse(rec.TerminalURL)
		if err != nil {
			if rec.Hit {
				fmt.Fprintf(os.Stderr,
					"failed to parse successful url '%s': %s", rec.TerminalURL, err.Error())
			}
			continue
		}

		if strings.Contains(u.RawPath, "viewcontent.cgi") {
			viewcontentTotal++
			if rec.Hit {
				viewcontentHits++
			}
		}

		var s stat
		var ok bool

		if s, ok = stats[u.Host]; !ok {
			s = stat{}
		}

		if rec.Hit {
			s.Hits++
		} else {
			s.Misses++
		}
		s.Total++
		if (rec.BaseURL == rec.TerminalURL) && rec.Hit {
			s.DirectHits++
		}

		stats[u.Host] = s
	}

	var count int

	for k, v := range stats {
		if v.Total < signal {
			continue
		}
		hitRate := (float64(v.Hits) / float64(v.Total)) * 100.0
		directHitRate := (float64(v.DirectHits) / float64(v.Hits)) * 100.0
		outLine := []string{
			k,
			fmt.Sprintf("%d", v.Total),
			fmt.Sprintf("%.2f", directHitRate),
			fmt.Sprintf("%.2f", hitRate)}
		var err error
		if hitRate < threshold {
			err = underW.Write(outLine)
		} else {
			err = overW.Write(outLine)
		}
		if err != nil {
			panic(err)
		}
		if count%10000 == 0 {
			overW.Flush()
			underW.Flush()
		}
	}

	overW.Flush()
	underW.Flush()

	if overW.Error() != nil {
		panic(overW.Error())
	}
	if underW.Error() != nil {
		panic(underW.Error())
	}

	fmt.Printf("%d of %d viewcontent.cgi attempts succeeded\n",
		viewcontentHits, viewcontentTotal)
}
