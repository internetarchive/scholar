package feeds

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/adrg/xdg"
	"github.com/internetarchive/scholar/pubmed2json"
	"github.com/klauspost/compress/zstd"
	"github.com/internetarchive/scholar/scholkit"
	"github.com/internetarchive/scholar/scholkit/atomicfile"
)

const DefaultCacheTTL = 24 * time.Hour // TODO: move to a cache pkg

// PubMedFile represents metadata for a PubMed update file, cf.
// https://ftp.ncbi.nlm.nih.gov/pubmed/updatefiles/.
type PubMedFile struct {
	Filename     string
	URL          string
	LastModified time.Time
	Size         string
}

// PubMedFetcher handles fetching and parsing PubMed update files list
type PubMedFetcher struct {
	BaseURL  string
	CacheTTL time.Duration
	CacheDir string
}

// NewPubMedFetcher creates a new fetcher with default settings
func NewPubMedFetcher(baseURL string) (*PubMedFetcher, error) {
	cacheDir, err := xdg.CacheFile(filepath.Join(scholkit.AppName, "pubmed"))
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return nil, err
	}
	return &PubMedFetcher{
		BaseURL:  baseURL,
		CacheTTL: DefaultCacheTTL,
		CacheDir: cacheDir,
	}, nil
}

// getCachedIndex returns the cached content if it exists and is not expired
func (pf *PubMedFetcher) getCachedIndex() ([]byte, error) {
	// TODO: take into account the base url
	cacheFile := filepath.Join(pf.CacheDir, "pubmed_index.html")
	info, err := os.Stat(cacheFile)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if time.Since(info.ModTime()) > pf.CacheTTL {
		return nil, nil
	}
	return os.ReadFile(cacheFile)
}

// fetchIndex fetches content from URL or uses cached content if available
func (pf *PubMedFetcher) fetchIndex() ([]byte, error) {
	b, err := pf.getCachedIndex()
	if err != nil {
		return nil, err
	}
	if b != nil {
		return b, nil
	}
	// TODO: more resilient client
	resp, err := http.Get(pf.BaseURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch URL, status code: %d", resp.StatusCode)
	}
	b, err = io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	cacheFile := filepath.Join(pf.CacheDir, "pubmed_index.html")
	if err := os.WriteFile(cacheFile, b, 0644); err != nil {
		return nil, err
	}
	return b, nil
}

// parseLastModified parses date strings like "2025-01-10 14:05" into time.Time
func parseLastModified(dateStr string) (time.Time, error) {
	return time.Parse("2006-01-02 15:04", dateStr)
}

// FetchFiles retrieves and parses the PubMed update files.
func (pf *PubMedFetcher) FetchFiles() ([]PubMedFile, error) {
	b, err := pf.fetchIndex()
	if err != nil {
		return nil, err
	}
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(b)))
	if err != nil {
		return nil, err
	}
	var files []PubMedFile
	xmlPattern := regexp.MustCompile(`^pubmed\d+n\d+\.xml\.gz$`)
	doc.Find("pre a").Each(func(i int, s *goquery.Selection) {
		href, _ := s.Attr("href")
		if xmlPattern.MatchString(href) {
			var (
				parentText = s.Parent().Text()
				parts      = strings.Fields(parentText)
			)
			for j, part := range parts {
				if part == href && j+3 < len(parts) {
					dateStr := parts[j+1] + " " + parts[j+2]
					size := parts[j+3]
					lastModified, err := parseLastModified(dateStr)
					if err != nil {
						continue
					}
					files = append(files, PubMedFile{
						Filename:     href,
						URL:          pf.BaseURL + href,
						LastModified: lastModified,
						Size:         size,
					})
					break
				}
			}
		}
	})
	return files, nil
}

// FilterPubmedFiles returns a list of file filtered by a given filter function.
func FilterPubmedFiles(files []PubMedFile, f func(PubMedFile) bool) (result []PubMedFile) {
	for _, fi := range files {
		if f(fi) {
			result = append(result, fi)
		}
	}
	return
}

// -- PubMedHarvester: day-slice harvesting via NCBI E-utilities API --

// esearchResult holds the fields we need from the NCBI esearch JSON response.
type esearchResult struct {
	EsearchResult struct {
		Count    string   `json:"count"`
		RetMax   string   `json:"retmax"`
		RetStart string   `json:"retstart"`
		IdList   []string `json:"idlist"`
		WebEnv   string   `json:"webenv"`
		QueryKey string   `json:"querykey"`
	} `json:"esearchresult"`
}

// PubMedHarvester fetches records from the NCBI E-utilities API and writes
// zstd-compressed NDJSON to disk, analogous to CrossrefHarvester.
type PubMedHarvester struct {
	Client    Doer   // HTTP client; use pester.New() for retries
	ApiBase   string // defaults to https://eutils.ncbi.nlm.nih.gov/entrez/eutils
	ApiKey    string // optional; raises rate limit from 3 to 10 req/s
	FetchSize int    // PMIDs per esearch page (max 10000; default 10000)
	BatchSize int    // PMIDs per efetch request (max ~200; default 200)

	mu      sync.Mutex
	lastReq time.Time
}

// rateInterval returns the minimum gap between API requests: 110 ms with a
// key (~9 req/s, safely under NCBI's 10/s limit) or 370 ms without (~2.7
// req/s, safely under the 3/s limit).
func (h *PubMedHarvester) rateInterval() time.Duration {
	if h.ApiKey != "" {
		return 110 * time.Millisecond
	}
	return 370 * time.Millisecond
}

// wait blocks until it is safe to issue the next API request.
func (h *PubMedHarvester) wait() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if elapsed := time.Since(h.lastReq); elapsed < h.rateInterval() {
		time.Sleep(h.rateInterval() - elapsed)
	}
	h.lastReq = time.Now()
}

func (h *PubMedHarvester) base() string {
	if h.ApiBase != "" {
		return h.ApiBase
	}
	return "https://eutils.ncbi.nlm.nih.gov/entrez/eutils"
}

func (h *PubMedHarvester) addKey(vs url.Values) {
	if h.ApiKey != "" {
		vs.Set("api_key", h.ApiKey)
	}
}

// DaySliceKey returns the filename for a given day's slice and the start/end
// times used, mirroring CrossrefHarvester.DaySliceKey.
func (h *PubMedHarvester) DaySliceKey(t time.Time, prefix string) (key string, start, end time.Time) {
	start = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	end = time.Date(t.Year(), t.Month(), t.Day(), 23, 59, 59, 0, time.UTC)
	key = fmt.Sprintf("%smdat-%s-%s.json.zst", prefix, start.Format("2006-01-02"), end.Format("2006-01-02"))
	return
}

// WriteDaySlice atomically writes a zstd-compressed NDJSON file for all PubMed
// records with modification dates on the given day. Idempotent once the file exists.
func (h *PubMedHarvester) WriteDaySlice(t time.Time, dir, prefix string) error {
	fn, start, end := h.DaySliceKey(t, prefix)
	dst := path.Join(dir, fn)
	if _, err := os.Stat(dst); err == nil {
		return nil
	}
	f, err := atomicfile.New(dst)
	if err != nil {
		return err
	}
	enc, err := zstd.NewWriter(f)
	if err != nil {
		return err
	}
	if err := h.WriteSlice(enc, start, end); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	return f.Close()
}

// esearchAll stores results on the NCBI History Server (usehistory=y) and
// returns the total count along with the WebEnv and QueryKey needed to page
// through those results via efetch without hitting the retstart=9998 cap.
func (h *PubMedHarvester) esearchAll(from, until time.Time) (*esearchResult, error) {
	vs := url.Values{}
	vs.Set("db", "pubmed")
	vs.Set("datetype", "mdat")
	vs.Set("mindate", from.Format("2006/01/02"))
	vs.Set("maxdate", until.Format("2006/01/02"))
	vs.Set("retmax", "0") // only need count + history keys
	vs.Set("usehistory", "y")
	vs.Set("retmode", "json")
	h.addKey(vs)
	link := fmt.Sprintf("%s/esearch.fcgi?%s", h.base(), vs.Encode())
	log.Printf("pubmed esearch: %s to %s", from.Format("2006-01-02"), until.Format("2006-01-02"))
	h.wait()
	req, err := http.NewRequest("GET", link, nil)
	if err != nil {
		return nil, err
	}
	resp, err := h.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("pubmed esearch: HTTP %d", resp.StatusCode)
	}
	var result esearchResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("pubmed esearch decode: %v", err)
	}
	return &result, nil
}

// pubmedMaxRetStart is the maximum retstart value accepted by the NCBI efetch
// API. Requests with retstart >= 9999 return HTTP 400 regardless of the
// history-server result count.
const pubmedMaxRetStart = 9999

// fetchBatch calls efetch using History Server keys for a window of records
// starting at retstart and writes converted NDJSON to w.
func (h *PubMedHarvester) fetchBatch(w io.Writer, webEnv, queryKey string, retstart, retmax int) error {
	vs := url.Values{}
	vs.Set("db", "pubmed")
	vs.Set("rettype", "xml")
	vs.Set("retmode", "xml")
	vs.Set("WebEnv", webEnv)
	vs.Set("query_key", queryKey)
	vs.Set("retstart", fmt.Sprintf("%d", retstart))
	vs.Set("retmax", fmt.Sprintf("%d", retmax))
	h.addKey(vs)
	link := fmt.Sprintf("%s/efetch.fcgi?%s", h.base(), vs.Encode())
	log.Printf("pubmed efetch: retstart=%d retmax=%d", retstart, retmax)
	h.wait()
	req, err := http.NewRequest("GET", link, nil)
	if err != nil {
		return err
	}
	resp, err := h.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("pubmed efetch: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if _, err := pubmed2json.Convert(resp.Body, w); err != nil {
		return fmt.Errorf("pubmed2json: %v", err)
	}
	return nil
}

// WriteSlice fetches all PubMed records with modification dates between from
// and until, writing NDJSON to w.
func (h *PubMedHarvester) WriteSlice(w io.Writer, from, until time.Time) error {
	batchSize := h.BatchSize
	if batchSize <= 0 {
		batchSize = 200
	}
	result, err := h.esearchAll(from, until)
	if err != nil {
		return err
	}
	total, err := strconv.Atoi(result.EsearchResult.Count)
	if err != nil {
		return fmt.Errorf("pubmed esearch count: %v", err)
	}
	log.Printf("pubmed: %d records modified on %s", total, from.Format("2006-01-02"))
	webEnv := result.EsearchResult.WebEnv
	queryKey := result.EsearchResult.QueryKey
	// NCBI efetch rejects retstart >= 9999 even when using the History
	// Server, so we cannot retrieve more than ~10000 records via this API.
	// Log a warning and cap retrieval rather than failing hard.
	fetchable := total
	if fetchable > pubmedMaxRetStart {
		log.Printf("pubmed WARNING: %d total records exceeds efetch retstart limit (%d); only %d records will be retrieved for %s",
			total, pubmedMaxRetStart, pubmedMaxRetStart, from.Format("2006-01-02"))
		fetchable = pubmedMaxRetStart
	}
	for retstart := 0; retstart < fetchable; retstart += batchSize {
		count := batchSize
		if retstart+count > fetchable {
			count = fetchable - retstart
		}
		if err := h.fetchBatch(w, webEnv, queryKey, retstart, count); err != nil {
			return fmt.Errorf("pubmed batch at %d: %v", retstart, err)
		}
	}
	return nil
}
