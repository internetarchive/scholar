package feeds

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/adrg/xdg"
	"github.com/miku/scholkit"
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
