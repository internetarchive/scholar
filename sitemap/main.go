// Command sitemap regenerates the Google Scholar sitemap for
// scholar.archive.org. It scans the scholar_fulltext Elasticsearch index once
// and writes two sets of plain-text sitemap files plus their XML indexes:
//
//   - sitemap-works-NNNNN.txt   one /work/{ident} detail-page URL per matched work
//   - sitemap-access-NNNNN.txt  one /work/{ident}/access/... redirect URL per work
//   - sitemap-index-works.xml   sitemap index listing the works files
//   - sitemap-index-access.xml  sitemap index listing the access files
//
// It replaces the legacy fatcat-cli | jq | rg | awk | split pipeline in
// scripts/sitemap/legacy/. It only generates files; deployment to the serving
// host is left to the operator (see docs/sitemap.md).
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// pdRuleOffset converts the current year into the US public-domain cutoff year.
// A work enters the public domain on January 1 of the 96th year after
// publication (the 95-year copyright term plus the Jan-1 entry convention), so
// in 2026 works published through 1930 are public domain (2026-96=1930).
const pdRuleOffset = 96

// progressEvery controls how often scan progress is logged to stderr.
const progressEvery = 100000

func main() {
	esURL := flag.String("es-url", "https://scholar.archive.org/_es", "Elasticsearch base URL")
	index := flag.String("index", "scholar_fulltext", "Elasticsearch index name")
	baseURL := flag.String("base-url", "https://scholar.archive.org", "URL prefix for emitted sitemap URLs and <loc> entries")
	outdir := flag.String("outdir", ".", "directory to write sitemap files into")
	perFile := flag.Int("per-file", 20000, "max URLs per sitemap file (Google Scholar's stated limit)")
	pageSize := flag.Int("page-size", 10000, "Elasticsearch scroll batch size (hard-capped at the index max_result_window of 10000)")
	pdYearFlag := flag.Int("pd-year", 0, "public-domain cutoff year; 0 computes it from the current year")
	limit := flag.Int("limit", 0, "stop after scanning this many docs (0 = unlimited); for smoke tests")
	keepAlive := flag.String("keep-alive", "30m", "Elasticsearch scroll context keep-alive; must exceed the worst-case retry window (~10m) so a transient ES stall doesn't orphan the scroll")
	slicesFlag := flag.Int("slices", 1, "number of parallel sliced-scroll workers; >1 is ~Nx faster but ~Nx more load on the shared cluster (ideally <= shard count)")
	countOnly := flag.Bool("count-only", false, "print the matching document count and exit")
	flag.Parse()

	if *pageSize > 10000 {
		log.Printf("page-size %d exceeds the index max_result_window (10000); clamping to 10000", *pageSize)
		*pageSize = 10000
	}
	slices := *slicesFlag
	if slices < 1 {
		slices = 1
	}

	pdYear := *pdYearFlag
	if pdYear == 0 {
		pdYear = time.Now().Year() - pdRuleOffset
	}
	query := buildQuery(pdYear)
	client := &http.Client{Timeout: 120 * time.Second}
	es := strings.TrimRight(*esURL, "/")

	if *countOnly {
		n, err := count(client, es, *index, query)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(n)
		return
	}

	if err := os.MkdirAll(*outdir, 0o755); err != nil {
		log.Fatal(err)
	}
	g := &generator{
		baseURL: strings.TrimRight(*baseURL, "/"),
		works:   newSitemapWriter(*outdir, "works", *perFile),
		access:  newSitemapWriter(*outdir, "access", *perFile),
		limit:   *limit,
		nextLog: progressEvery,
	}

	// work_ident comes from the doc _id (= "work_<ident>"), so _source only
	// needs the access fields; access_url is doc_values:false and unavailable
	// any cheaper way.
	source := []string{"fulltext.access_type", "fulltext.access_url"}
	log.Printf("scanning %s/%s/_search (pd-year<=%d, batch=%d, slices=%d)", es, *index, pdYear, *pageSize, slices)

	var wg sync.WaitGroup
	errCh := make(chan error, slices)
	for i := 0; i < slices; i++ {
		wg.Add(1)
		go func(sliceID int) {
			defer wg.Done()
			if err := runSlice(client, es, *index, query, *pageSize, source, *keepAlive, sliceID, slices, g); err != nil {
				errCh <- err
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	if err := <-errCh; err != nil {
		log.Fatal(err)
	}

	if err := g.works.close(); err != nil {
		log.Fatal(err)
	}
	if err := g.access.close(); err != nil {
		log.Fatal(err)
	}

	today := time.Now().Format("2006-01-02")
	if err := writeIndex(*outdir, g.baseURL, "works", g.works.files, today); err != nil {
		log.Fatal(err)
	}
	if err := writeIndex(*outdir, g.baseURL, "access", g.access.files, today); err != nil {
		log.Fatal(err)
	}

	log.Printf("done: scanned=%d work_urls=%d access_urls=%d access_skipped=%d empty_url=%d no_ident=%d",
		g.scanned, g.nWork, g.nAccess, g.nAccessSkip, g.nNoURL, g.nNoIdent)
	log.Printf("wrote %d works files + %d access files + 2 index files to %s",
		len(g.works.files), len(g.access.files), *outdir)
}

// buildQuery reproduces the legacy Lucene query as a structured bool query:
//
//	doc_type:work
//	(fulltext.access_type:ia_file OR fulltext.access_type:wayback)
//	(NOT biblio.arxiv_id:*) (NOT biblio.pmcid:*)
//	((NOT biblio.publisher_type:big5) OR biblio.release_year:<=pdYear OR tags:oa)
//
// (The live index also exposes a top-level `year` alias to biblio.release_year,
// which is what the legacy query used.) Everything is in filter context: a
// sitemap scan only needs yes/no matching, and filter context skips relevance
// scoring and is cacheable, so it is cheaper on the shared cluster. The inner
// publisher-type disjunction uses minimum_should_match:1 because, alongside
// other clauses, should clauses are otherwise scoring-only.
func buildQuery(pdYear int) map[string]any {
	return map[string]any{
		"bool": map[string]any{
			"filter": []any{
				map[string]any{"term": map[string]any{"doc_type": "work"}},
				map[string]any{"terms": map[string]any{"fulltext.access_type": []string{"ia_file", "wayback"}}},
				map[string]any{"bool": map[string]any{
					"should": []any{
						map[string]any{"bool": map[string]any{"must_not": map[string]any{"term": map[string]any{"biblio.publisher_type": "big5"}}}},
						map[string]any{"range": map[string]any{"biblio.release_year": map[string]any{"lte": pdYear}}},
						map[string]any{"term": map[string]any{"tags": "oa"}},
					},
					"minimum_should_match": 1,
				}},
			},
			"must_not": []any{
				map[string]any{"exists": map[string]any{"field": "biblio.arxiv_id"}},
				map[string]any{"exists": map[string]any{"field": "biblio.pmcid"}},
			},
		},
	}
}

type docSource struct {
	Fulltext struct {
		AccessType string `json:"access_type"`
		AccessURL  string `json:"access_url"`
	} `json:"fulltext"`
}

type searchResponse struct {
	ScrollID string `json:"_scroll_id"`
	Hits     struct {
		Hits []struct {
			ID     string    `json:"_id"`
			Source docSource `json:"_source"`
		} `json:"hits"`
	} `json:"hits"`
}

// runSlice scans one scroll stream (one slice when sliceMax > 1, the whole
// result set when sliceMax == 1) and feeds every hit to the generator.
func runSlice(client *http.Client, es, index string, query map[string]any, pageSize int, source []string, keepAlive string, sliceID, sliceMax int, g *generator) error {
	resp, err := scrollStart(client, es, index, query, pageSize, source, keepAlive, sliceID, sliceMax)
	if err != nil {
		return err
	}
	scrollID := resp.ScrollID
	defer func() { clearScroll(client, es, scrollID) }()
	for {
		hits := resp.Hits.Hits
		if len(hits) == 0 {
			return nil
		}
		for _, h := range hits {
			stop, err := g.process(h.ID, h.Source)
			if err != nil {
				return err
			}
			if stop { // -limit reached
				return nil
			}
		}
		resp, err = scrollNext(client, es, scrollID, keepAlive)
		if err != nil {
			return err
		}
		if resp.ScrollID != "" {
			scrollID = resp.ScrollID
		}
	}
}

// scrollStart opens a scroll context and returns the first batch. We use the
// scroll API (rather than search_after) because work_ident and key are indexed
// with doc_values disabled and so cannot be used as a sort key; scroll needs no
// sort field. This mirrors the proven approach in scripts/thumbs. When
// sliceMax > 1 the scroll is sliced for parallel consumption.
func scrollStart(client *http.Client, es, index string, query map[string]any, pageSize int, source []string, keepAlive string, sliceID, sliceMax int) (*searchResponse, error) {
	req := map[string]any{
		"size":    pageSize,
		"sort":    []string{"_doc"},
		"_source": source,
		"query":   query,
	}
	if sliceMax > 1 {
		req["slice"] = map[string]any{"id": sliceID, "max": sliceMax}
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	data, err := doPost(client, es+"/"+index+"/_search?scroll="+keepAlive, body)
	if err != nil {
		return nil, err
	}
	return decodeSearch(data)
}

// scrollNext fetches the next batch for an open scroll context.
func scrollNext(client *http.Client, es, scrollID, keepAlive string) (*searchResponse, error) {
	body, err := json.Marshal(map[string]any{"scroll": keepAlive, "scroll_id": scrollID})
	if err != nil {
		return nil, err
	}
	data, err := doPost(client, es+"/_search/scroll", body)
	if err != nil {
		return nil, err
	}
	return decodeSearch(data)
}

func decodeSearch(data []byte) (*searchResponse, error) {
	var sr searchResponse
	if err := json.Unmarshal(data, &sr); err != nil {
		return nil, fmt.Errorf("decoding search response: %w", err)
	}
	return &sr, nil
}

// clearScroll best-effort releases a scroll context. Errors are ignored: the
// context expires on its own after keep-alive, and the proxy may disallow DELETE.
func clearScroll(client *http.Client, es, scrollID string) {
	if scrollID == "" {
		return
	}
	body, err := json.Marshal(map[string]any{"scroll_id": scrollID})
	if err != nil {
		return
	}
	req, err := http.NewRequest("DELETE", es+"/_search/scroll", bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if resp, err := client.Do(req); err == nil {
		resp.Body.Close()
	}
}

func count(client *http.Client, es, index string, query map[string]any) (int64, error) {
	b, err := json.Marshal(map[string]any{"query": query})
	if err != nil {
		return 0, err
	}
	data, err := doPost(client, es+"/"+index+"/_count", b)
	if err != nil {
		return 0, err
	}
	var cr struct {
		Count int64 `json:"count"`
	}
	if err := json.Unmarshal(data, &cr); err != nil {
		return 0, fmt.Errorf("decoding count response: %w", err)
	}
	return cr.Count, nil
}

// doPost POSTs a JSON body and returns the response body, retrying on network
// errors, 429, and 5xx. Mirrors the retry approach in scripts/fcmatch/main.go.
func doPost(client *http.Client, url string, body []byte) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		req, err := http.NewRequest("POST", url, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			time.Sleep(time.Duration(attempt+1) * time.Second)
			continue
		}
		data, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == 429 || resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("status %d: %s", resp.StatusCode, truncate(string(data), 256))
			time.Sleep(time.Duration(attempt+1) * time.Second)
			continue
		}
		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("status %d: %s", resp.StatusCode, truncate(string(data), 256))
		}
		if readErr != nil {
			return nil, readErr
		}
		return data, nil
	}
	return nil, fmt.Errorf("after retries: %w", lastErr)
}

// generator turns matched docs into sitemap URLs and tracks counters. Its
// process method is safe for concurrent use by multiple slice workers; all
// state (the rotating writers and the counters) is guarded by mu.
type generator struct {
	baseURL string
	works   *sitemapWriter
	access  *sitemapWriter

	mu      sync.Mutex
	scanned int
	limit   int
	nextLog int

	nWork       int
	nAccess     int
	nAccessSkip int
	nNoURL      int
	nNoIdent    int
}

// process emits the sitemap URLs for one doc. It returns true once the -limit
// cap (if any) has been reached, signalling workers to stop.
func (g *generator) process(id string, s docSource) (bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.scanned++

	// The doc _id is "work_<ident>"; the bare ident is what the /work/ routes use.
	ident := strings.TrimPrefix(id, "work_")
	if ident == "" {
		g.nNoIdent++
	} else {
		// The work detail page is a valid, indexable scholar.archive.org URL
		// regardless of the access URL, so it always goes in the works sitemap.
		if err := g.works.write(g.baseURL + "/work/" + ident); err != nil {
			return false, err
		}
		g.nWork++

		switch {
		case s.Fulltext.AccessURL == "":
			g.nNoURL++
		default:
			if path, ok := rewriteAccessURL(ident, s.Fulltext.AccessType, s.Fulltext.AccessURL); ok {
				if err := g.access.write(g.baseURL + path); err != nil {
					return false, err
				}
				g.nAccess++
			} else {
				// Not rewritable to a scholar redirect (e.g. 12-digit wayback
				// timestamps, non-IA hosts). The legacy pipeline dropped such a
				// work from both sitemaps; we keep its work-detail URL and skip
				// only its access line.
				g.nAccessSkip++
			}
		}
	}

	if g.scanned >= g.nextLog {
		log.Printf("scanned %d (work=%d access=%d)", g.scanned, g.nWork, g.nAccess)
		for g.scanned >= g.nextLog {
			g.nextLog += progressEvery
		}
	}
	return g.limit > 0 && g.scanned >= g.limit, nil
}

var (
	waybackRe = regexp.MustCompile(`^https?://web\.archive\.org/web/\d{14}/(.+)$`)
	iaFileRe  = regexp.MustCompile(`^https?://archive\.org/download/([^/]+)/(.+)$`)
)

// rewriteAccessURL ports make_access_redirect_url from
// scripts/sitemap/legacy/transform_access_url.py, which mirrors djscholar's
// ftsearch/views.py::_rewrite_access_url. It returns the scholar-relative
// access-redirect path and true, or ("", false) when the raw URL can't be
// turned into a scholar redirect.
func rewriteAccessURL(workIdent, accessType, accessURL string) (string, bool) {
	switch accessType {
	case "wayback":
		if m := waybackRe.FindStringSubmatch(accessURL); m != nil {
			return "/work/" + workIdent + "/access/wayback/" + m[1], true
		}
	case "ia_file":
		if m := iaFileRe.FindStringSubmatch(accessURL); m != nil {
			return "/work/" + workIdent + "/access/ia_file/" + m[1] + "/" + m[2], true
		}
	}
	return "", false
}

// sitemapWriter writes URLs one per line, rotating to a new numbered file every
// perFile lines. The first file is created lazily on the first write.
type sitemapWriter struct {
	outdir  string
	prefix  string
	perFile int

	files   []string
	fileIdx int
	n       int
	f       *os.File
	bw      *bufio.Writer
}

func newSitemapWriter(outdir, prefix string, perFile int) *sitemapWriter {
	return &sitemapWriter{outdir: outdir, prefix: prefix, perFile: perFile, fileIdx: -1}
}

func (w *sitemapWriter) write(url string) error {
	if w.bw == nil || w.n >= w.perFile {
		if err := w.rotate(); err != nil {
			return err
		}
	}
	if _, err := w.bw.WriteString(url); err != nil {
		return err
	}
	if err := w.bw.WriteByte('\n'); err != nil {
		return err
	}
	w.n++
	return nil
}

func (w *sitemapWriter) rotate() error {
	if err := w.close(); err != nil {
		return err
	}
	w.fileIdx++
	name := fmt.Sprintf("sitemap-%s-%05d.txt", w.prefix, w.fileIdx)
	f, err := os.Create(filepath.Join(w.outdir, name))
	if err != nil {
		return err
	}
	w.f = f
	w.bw = bufio.NewWriter(f)
	w.n = 0
	w.files = append(w.files, name)
	return nil
}

func (w *sitemapWriter) close() error {
	if w.bw == nil {
		return nil
	}
	if err := w.bw.Flush(); err != nil {
		w.f.Close()
		return err
	}
	err := w.f.Close()
	w.bw = nil
	w.f = nil
	return err
}

// writeIndex writes an XML sitemap index listing each generated text file,
// reproducing the format of legacy/generate_sitemap_indices.py.
func writeIndex(outdir, baseURL, prefix string, files []string, today string) error {
	f, err := os.Create(filepath.Join(outdir, "sitemap-index-"+prefix+".xml"))
	if err != nil {
		return err
	}
	defer f.Close()
	bw := bufio.NewWriter(f)
	fmt.Fprintln(bw, `<?xml version="1.0" encoding="UTF-8"?>`)
	fmt.Fprintln(bw, `<sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">`)
	for _, name := range files {
		fmt.Fprintln(bw, "  <sitemap>")
		fmt.Fprintf(bw, "    <loc>%s/%s</loc>\n", baseURL, name)
		fmt.Fprintf(bw, "    <lastmod>%s</lastmod>\n", today)
		fmt.Fprintln(bw, "  </sitemap>")
	}
	fmt.Fprintln(bw, "</sitemapindex>")
	return bw.Flush()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
