package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/miku/grobidclient/tei"
)

type config struct {
	FCDBURL   string
	CSVReader *csv.Reader
	Workers   int
	Out       *csv.Writer
	Log       *log.Logger
}

type record struct {
	// from source csv
	ID       string
	Citation string
	DOI      string
	Type     string
	// added by this script
	WBM    string
	Source string
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

func main() {
	cfg := &config{
		Workers:   12,
		FCDBURL:   os.Getenv("FATCAT1_PGURL"),
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

func buildESQuery(title string, authors []string) io.Reader {
	joinedAuthors := strings.Join(authors, " ")

	q := map[string]any{
		"size": 1,
		"query": map[string]any{
			"bool": map[string]any{
				"must": []map[string]any{
					{
						"match": map[string]any{
							"title": map[string]string{
								"query":     title,
								"fuzziness": "AUTO",
							},
						},
					},
					{
						"match": map[string]any{
							"contrib_names": map[string]string{
								"query":    joinedAuthors,
								"operator": "or",
							},
						},
					},
				},
			},
		},
	}

	bs, err := json.Marshal(q)
	if err != nil {
		panic(err)
	}

	return bytes.NewBuffer(bs)
}

func fuzzySearch(cfg *config, r record) string {
	v := url.Values{}
	v.Set("citations", r.Citation)
	v.Set("consolidateCitations", "0")
	cfg.Log.Printf("%s: submitting citation '%s'", r.ID, r.Citation)

	resp, err := http.PostForm("http://wbgrp-svc506:8070/api/processCitation", v)
	if err != nil {
		cfg.Log.Printf("%s: grobid failed: '%s'", r.ID, err.Error())
		return ""
	}
	xbody, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		cfg.Log.Printf("%s: grobid xml read failed: '%s'", r.ID, err.Error())
		return ""
	}

	pc := tei.ParseCitation(string(xbody))

	cfg.Log.Printf("%s: parsed tei title '%s'", r.ID, pc.Title)

	if pc.Title == "" {
		return ""
	}

	authorNames := []string{}
	for _, a := range pc.Authors {
		cfg.Log.Printf("%#v", a)
		authorNames = append(authorNames, a.FullName)
	}

	body := buildESQuery(pc.Title, authorNames)

	resp, err = http.Post("https://scholar.archive.org/_es/fatcat_release/_search", "application/json", body)
	if err != nil {
		cfg.Log.Printf("%s: ES failed: '%s'", r.ID, err.Error())
	}

	bs, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		cfg.Log.Printf("%s: could not read ES body: '%s'", r.ID, err.Error())
		return ""
	}

	cfg.Log.Printf("%s: %#v", r.ID, string(bs))

	// TODO use title in ES query

	return ""
}

func worker(ctx context.Context, cfg *config, pool *pgxpool.Pool, jobs <-chan record, results chan<- record) {
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		panic(err)
	}
	defer conn.Release()

	for r := range jobs {
		wbmLink := ""
		source := ""
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
			resp, err := client.Do(req)
			if err != nil {
				cfg.Log.Printf("doi '%s' got error '%s'", r.DOI, err.Error())
			} else if resp.StatusCode == 301 || resp.StatusCode == 302 {
				cfg.Log.Printf("%s: got a 30{1,2}", r.ID)
				loc := resp.Header.Get("Location")
				if strings.Contains(loc, "web.archive.org") {
					wbmLink = loc
					source = "fc2"
				} else {
					cfg.Log.Printf("%s: non-wbm link: '%s'", r.ID, loc)
				}
			} else if resp.StatusCode == 404 {
				msgb, err := io.ReadAll(resp.Body)
				cfg.Log.Printf("%s: 404", r.ID)
				if strings.Contains(string(msgb), "no release found") {
					err = conn.QueryRow(ctx, doiQ, r.DOI).Scan(&wbmLink)
					if err != nil {
						cfg.Log.Printf("%s: db error '%s'", r.ID, err.Error())
					} else {
						source = "fc2"
					}
				}
			} else {
				cfg.Log.Printf("doi '%s' got unexpected status '%d'", r.DOI, resp.StatusCode)
			}
		} else {
			wbmLink = fuzzySearch(cfg, r)
			if wbmLink != "" {
				source = "es"
			}
		}

		rr := r
		rr.WBM = wbmLink
		rr.Source = source

		results <- rr
	}
}

func _main(cfg *config) error {
	ctx := context.Background()

	fcpool, err := pgxpool.New(ctx, os.Getenv("FATCAT1_PGURL"))
	if err != nil {
		return fmt.Errorf("unable to connect to db: %w", err)
	}
	defer fcpool.Close()

	jobs := make(chan record)
	results := make(chan record)

	var wg sync.WaitGroup

	for w := 0; w < cfg.Workers; w++ {
		// 1.25 has wg.Go
		wg.Add(1)
		go func() {
			defer wg.Done()
			worker(ctx, cfg, fcpool, jobs, results)
		}()
	}

	go func() {
		cfg.Out.Write([]string{
			"id",
			"citation_text",
			"doi",
			"type",
			"source",
			"wbm",
		})
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
				r.Source,
				r.WBM,
			}
			cfg.Out.Write(outLine)
		}
		cfg.Out.Flush()
	}()

	var line []string
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
