package main

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"

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
}

func main() {
	fc1conn, err := pgx.Connect(context.Background(), os.Getenv("FATCAT1_PGURL"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "unable to connect to database: %s", err.Error())
		os.Exit(2)
	}

	cfg := &config{
		FCConn:    fc1conn,
		CSVReader: csv.NewReader(os.Stdin),
		Workers:   8,
		Out:       csv.NewWriter(os.Stdout),
	}

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

func worker(id int, jobs <-chan record, results chan<- record) {
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	for r := range jobs {
		wbmLink := ""
		if r.DOI != "" {
			// check fc2
			//  "https://scholar.archive.org/api/fatcat/v2/release/lookup/fulltext" \
			//-d "id_type=doi" -d "id_value=${1}" -w '%{redirect_url}'
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
			resp, err := client.Do(req)
			if err == nil {
				// TODO pull location header and see if it's wbm
			}

			// if miss, check fc1

		}

		//fmt.Println("Worker", id, "started job", j)
		//time.Sleep(time.Second)
		//fmt.Println("Worker", id, "finished job", j)
		//results <- j * 2 // Send result to results channel
	}
}

func _main(cfg *config) error {
	jobs := make(chan record)
	results := make(chan record)

	workers := cfg.Workers

	for w := 1; w <= workers; w++ {
		go worker(w, jobs, results)
	}

	var line []string
	var err error
	var lineErrs = []error{}
	count := 0
	for {
		line, err = cfg.CSVReader.Read()
		if err != nil {
			if errors.Is(err, io.EOF) {
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
		count++
	}
	if len(lineErrs) > 0 {
		errRatio := float64(len(lineErrs)) / float64(count)
		fmt.Fprintf(os.Stderr,
			"encountered %d line error (ratio: %f)\n", len(lineErrs), errRatio)
	}

	close(jobs)

	outLine := []string{}
	for i := 0; i < count; i++ {
		r := <-results
		if r.WBM == "" {
			continue
		}
		outLine = []string{
			r.ID,
			r.Citation,
			r.DOI,
			r.Type,
			r.WBM,
		}
		cfg.Out.Write(outLine)
	}

	close(results)

	return nil
}
