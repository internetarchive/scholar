package main

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"
)

/*

- connect to fatcat1
- connect to fatcat2 - or no, i should just use API
- connect to fc es
- read line
- if DOI, look in fatcat2
  - if not found, DOI in fatcat1
		- if not found, title search in es
- else title search in es
- if found, use release ID to check files
- if file found, print line with WBM link added
*/

/*
FATCAT2_PGURL
FATCAT1_PGURL
*/

type config struct {
	FCConn    *pgx.Conn
	CSVReader *csv.Reader
	Workers   int
	Out       *csv.Writer
	Log       *log.Logger
}

func main() {
	fc1conn, err := pgx.Connect(context.Background(), os.Getenv("FATCAT1_PGURL"))
	defer fc1conn.Close(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "unable to connect to database: %s", err.Error())
		os.Exit(2)
	}

	cfg := &config{
		Workers:   8,
		FCConn:    fc1conn,
		CSVReader: csv.NewReader(os.Stdin),
		Out:       csv.NewWriter(os.Stdout),
		Log:       log.New(os.Stderr, "", log.Lshortfile),
	}

	cfg.Log.Println("starting")

	if err := _main(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "failed: %s", err.Error())
		os.Exit(1)
	}
}

type record struct {
	ID       string
	Citation string
	DOI      string
	Type     string
	WBM      string
}

const doiQ = `
SELECT
  fru.url
FROM release_rev rr
JOIN release_ident ri
  ON ri.rev_id = rr.id
JOIN file_rev_release frr
  ON frr.target_release_ident_id = ri.id
JOIN file_rev_url fru
  ON fru.file_rev = frr.file_rev
WHERE
  rr.doi = $1
AND
  fru.url LIKE '%web.archive.org%'
LIMIT 1;
`

func worker(cfg *config, jobs <-chan record, results chan<- record) {
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	for r := range jobs {
		wbmLink := ""
		if r.DOI != "" {
			req, err := http.NewRequest(
				"GET",
				"https://scholar.archive.org/api/fatcat/v2/release/lookup/fulltext",
				nil)
			if err != nil {
				panic(err)
			}
			q := req.URL.Query()
			q.Add("id_type", "doi")
			q.Add("id_value", r.DOI)
			req.URL.RawQuery = q.Encode()
			cfg.Log.Printf("%#v", req.URL)
			cfg.Log.Printf("%#v", req.URL.RawQuery)
			resp, err := client.Do(req)
			if err != nil {
				cfg.Log.Printf("doi '%s' got error '%s'", r.DOI, err.Error())
			} else if resp.StatusCode == 301 || resp.StatusCode == 302 {
				cfg.Log.Printf("%s: got a 30{1,2}", r.ID)
				loc := resp.Header.Get("Location")
				if strings.Contains(loc, "web.archive.org") {
					wbmLink = loc
				} else {
					cfg.Log.Printf("%s: non-wbm link: '%s'", r.ID, loc)
				}
			} else if resp.StatusCode == 404 {
				cfg.Log.Printf("%s: 404", r.ID)
				// TODO look up via fc1 db
			} else {
				cfg.Log.Printf("doi '%s' got unexpected status '%d'", r.DOI, resp.StatusCode)
			}

		}

		if wbmLink == "" {
			ctx := context.Background()
			err := cfg.FCConn.QueryRow(ctx, doiQ, r.DOI).Scan(&wbmLink)
			if err != nil {
				cfg.Log.Printf("%s: db error '%s'", r.ID, err.Error())
			}
		}

		if wbmLink == "" {
			// TODO fuzzy search
		}

		rr := r
		rr.WBM = wbmLink

		results <- rr
	}
}

func _main(cfg *config) error {
	jobs := make(chan record)
	results := make(chan record)

	var wg sync.WaitGroup

	for w := 0; w < cfg.Workers; w++ {
		// 1.25 has wg.Go
		wg.Add(1)
		go func() {
			defer wg.Done()
			worker(cfg, jobs, results)
		}()
	}

	go func() {
		outLine := []string{}
		for r := range results {
			if r.WBM == "" {
				cfg.Log.Printf("%s: no wbm found", r.ID)
				continue
			}
			cfg.Log.Printf("%s: wbm found", r.ID)
			outLine = []string{
				r.ID,
				r.Citation,
				r.DOI,
				r.Type,
				r.WBM,
			}
			cfg.Out.Write(outLine)
		}
	}()

	var line []string
	var err error
	var lineErrs = []error{}
	count := 0
	for {
		line, err = cfg.CSVReader.Read()
		if err != nil {
			if errors.Is(err, io.EOF) {
				cfg.Log.Println("finished submitting jobs")
				break
			} else {
				lineErrs = append(lineErrs, err)
				continue
			}
		}

		r := record{
			ID:       line[0],
			Citation: line[1],
			DOI:      line[2],
			Type:     line[3],
		}
		jobs <- r
		cfg.Log.Printf("submitted job for %s", r.ID)
		count++
	}
	if len(lineErrs) > 0 {
		errRatio := float64(len(lineErrs)) / float64(count)
		cfg.Log.Printf(
			"encountered %d line error (ratio: %f)", len(lineErrs), errRatio)
	}
	close(jobs)

	wg.Wait()
	close(results)

	return nil
}
