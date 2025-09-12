package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/miku/grobidclient/tei"
)

type config struct {
	FCDBURL     string
	CSVReader   *csv.Reader
	Workers     int
	Out         *csv.Writer
	Log         *log.Logger
	WaybackOnly bool
}

// The full csv had a different structure than the samples.
// sample: id,citation_text,doi,type
// full: id,url,citation_text

type record struct {
	// from source csv
	ID       string
	URL      string
	Citation string
	DOI      string // unused for full csv
	//Type     string
	// added by this script
	WBM    string
	Source string
}

type elasticHit struct {
	Source struct {
		Revision string
	} `json:"_source"`
}

type elasticResult struct {
	Hits struct {
		Hits []elasticHit
	}
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

const revQ = `
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
  rr.id = $1
AND
  fru.url LIKE '%web.archive.org%'
LIMIT 1;
`

var waybackOnly bool

func init() {
	flag.BoolVar(&waybackOnly, "wayback-only", false, "only consider wbm pdf urls as worth of output")
}

func main() {
	cfg := &config{
		Workers:     64,
		FCDBURL:     os.Getenv("FATCAT1_PGURL"),
		CSVReader:   csv.NewReader(os.Stdin),
		Out:         csv.NewWriter(os.Stdout),
		Log:         log.New(os.Stderr, "", log.Lshortfile),
		WaybackOnly: waybackOnly,
	}

	cfg.CSVReader.Comma = '\t'

	cfg.Log.Println("starting")

	if err := _main(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "failed: %s", err.Error())
		os.Exit(1)
	}
}

func buildESQuery(title string, year int, authors []string) io.Reader {
	joinedAuthors := strings.Join(authors, " ")

	must := []map[string]any{
		{
			"match": map[string]any{
				"title": map[string]any{
					"query": title,
					"boost": 1.5,
					//"fuzziness": "AUTO",
				},
			},
		},
		{
			"match": map[string]any{
				"contrib_names": map[string]any{
					"query":    joinedAuthors,
					"operator": "or",
				},
			},
		},
	}

	if year > 0 {
		must = append(must, map[string]any{
			"match": map[string]any{
				"release_year": year,
			},
		})
	}

	q := map[string]any{
		"size":      1,
		"min_score": 80,
		"query": map[string]any{
			"bool": map[string]any{
				"must": must,
			},
		},
	}

	bs, err := json.Marshal(q)
	if err != nil {
		panic(err)
	}

	return bytes.NewBuffer(bs)
}

func fuzzySearch(ctx context.Context, cfg *config, conn *pgxpool.Conn, r record) string {
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

	cfg.Log.Printf("%s: %#v\n", r.ID, pc)

	if pc == nil {
		return ""
	}

	cfg.Log.Printf("%s: parsed tei title '%s'", r.ID, pc.Title)

	if pc.Title == "" {
		return ""
	}

	queryTitle := pc.Title

	if strings.Contains(queryTitle, "Archived") {
		ix := strings.Index(queryTitle, "Archived ")
		if ix > 0 {
			queryTitle = strings.TrimSpace(queryTitle[0:ix])
		}
	}

	authorNames := []string{}
	for _, a := range pc.Authors {
		authorNames = append(authorNames, a.FullName)
	}

	queryYear := -1
	if pc.Date != "" {
		queryYear, _ = strconv.Atoi(pc.Date)
	}

	body := buildESQuery(queryTitle, queryYear, authorNames)

	esRetries := 6

	for n := 1; n <= esRetries; n++ {
		resp, err = http.Post("https://scholar.archive.org/_es/fatcat_release/_search", "application/json", body)
		if err == nil {
			break
		}
		cfg.Log.Printf("%s: ES attempt %d failed: '%s'", r.ID, n, err.Error())
		time.Sleep(10 * time.Second)
	}

	if err != nil {
		return ""
	}

	bs, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		cfg.Log.Printf("%s: could not read ES body: '%s'", r.ID, err.Error())
		return ""
	}

	cfg.Log.Printf("%s: %s, %d, %v ->\n%#v", r.ID, queryTitle, queryYear, authorNames, string(bs))

	var esr elasticResult
	err = json.Unmarshal(bs, &esr)
	if err != nil {
		cfg.Log.Printf("%s: failed to parse es json: '%s'", r.ID, err.Error())
		return ""
	}

	if len(esr.Hits.Hits) == 0 {
		return ""
	}

	var wbmLink string

	err = conn.QueryRow(ctx, revQ, esr.Hits.Hits[0].Source.Revision).Scan(&wbmLink)
	if err != nil {
		cfg.Log.Printf("%s: revQ db error: %s", r.ID, err.Error())
	}

	return wbmLink
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
				if cfg.WaybackOnly && !strings.Contains(loc, "web.archive.org") {
					cfg.Log.Printf("%s: non-wbm link and wayback-only is true: '%s'", r.ID, loc)
				} else {
					wbmLink = loc
					source = "fc2"
				}
			} else if resp.StatusCode == 404 {
				msgb, err := io.ReadAll(resp.Body)
				if err != nil {
					cfg.Log.Printf("%s: could not read fc2 resp body: '%s'", r.ID, err.Error())
				} else {
					cfg.Log.Printf("%s: fc2: '%s'", r.ID, string(msgb))
				}
				if err != nil || strings.Contains(string(msgb), "no release found") {
					err = conn.QueryRow(ctx, doiQ, r.DOI).Scan(&wbmLink)
					if err != nil {
						cfg.Log.Printf("%s: db error '%s'", r.ID, err.Error())
					} else {
						source = "fc1"
					}
				}
			} else {
				cfg.Log.Printf("doi '%s' got unexpected status '%d'", r.DOI, resp.StatusCode)
			}
		} else {
			wbmLink = fuzzySearch(ctx, cfg, conn, r)
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
				r.URL,
				r.Citation,
				//r.DOI,
				//r.Type,
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
			URL:      line[1],
			Citation: line[2],
			//DOI:      line[2],
			//Type:     line[3],
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
