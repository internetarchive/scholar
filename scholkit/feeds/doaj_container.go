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
	"time"

	"github.com/internetarchive/scholar/scholkit/atomicfile"
	"github.com/klauspost/compress/zstd"
)

const (
	doajContainerDefaultEndpoint = "https://doaj.org/api/search/journals"
	doajContainerDefaultPageSize = 100
	doajContainerDefaultDelay    = 500 * time.Millisecond
)

type doajContainerSearchResponse struct {
	Total    int64             `json:"total"`
	Page     int               `json:"page"`
	PageSize int               `json:"pageSize"`
	Results  []json.RawMessage `json:"results"`
	Next     string            `json:"next"`
}

// DOAJContainerHarvester fetches journal-level metadata from the DOAJ search
// API and writes zstd-compressed NDJSON to disk.
type DOAJContainerHarvester struct {
	// Client is the HTTP client to use; nil uses http.DefaultClient.
	Client Doer
	// ApiEndpoint is the base URL; defaults to doajContainerDefaultEndpoint.
	ApiEndpoint string
	// PageSize is the number of results per API page; defaults to 100.
	PageSize int
	// MaxRecords limits total records written; 0 means unlimited.
	MaxRecords int
	// RequestDelay is inserted between successive API requests to respect the
	// 2 req/s rate limit. Defaults to 500ms.
	RequestDelay time.Duration
}

func (h *DOAJContainerHarvester) endpoint() string {
	if h.ApiEndpoint != "" {
		return h.ApiEndpoint
	}
	return doajContainerDefaultEndpoint
}

func (h *DOAJContainerHarvester) pageSize() int {
	if h.PageSize > 0 {
		return h.PageSize
	}
	return doajContainerDefaultPageSize
}

func (h *DOAJContainerHarvester) client() Doer {
	if h.Client != nil {
		return h.Client
	}
	return http.DefaultClient
}

func (h *DOAJContainerHarvester) requestDelay() time.Duration {
	if h.RequestDelay > 0 {
		return h.RequestDelay
	}
	return doajContainerDefaultDelay
}

// DaySliceKey returns the filename for a given day's slice and the start/end
// times used.
func (h *DOAJContainerHarvester) DaySliceKey(t time.Time, prefix string) (key string, start, end time.Time) {
	start = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	end = time.Date(t.Year(), t.Month(), t.Day(), 23, 59, 59, 0, time.UTC)
	key = fmt.Sprintf("%sdoaj-container-%s-%s.json.zst",
		prefix, start.Format("2006-01-02"), end.Format("2006-01-02"))
	return
}

// WriteDaySlice atomically writes a zstd-compressed NDJSON file for all DOAJ
// journals updated on the given day. Idempotent once the file exists.
func (h *DOAJContainerHarvester) WriteDaySlice(t time.Time, dir, prefix string) error {
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

// WriteSlice fetches all DOAJ journals whose last_updated timestamp falls
// between from and until, writing one raw JSON object per line (NDJSON) to w.
func (h *DOAJContainerHarvester) WriteSlice(w io.Writer, from, until time.Time) error {
	query := fmt.Sprintf("last_updated:[%s TO %s]",
		from.Format("2006-01-02T15:04:05Z"),
		until.Format("2006-01-02T15:04:05Z"))

	vs := url.Values{}
	vs.Set("pageSize", fmt.Sprintf("%d", h.pageSize()))
	vs.Set("sort", "last_updated:asc")
	vs.Set("page", "1")

	link := fmt.Sprintf("%s/%s?%s", h.endpoint(), url.PathEscape(query), vs.Encode())

	var (
		seen  int64
		total int64
		page  int
	)
	for link != "" {
		if page > 0 {
			time.Sleep(h.requestDelay())
		}
		req, err := http.NewRequest("GET", link, nil)
		if err != nil {
			return fmt.Errorf("doaj-container: build request: %w", err)
		}
		resp, err := h.client().Do(req)
		if err != nil {
			return fmt.Errorf("doaj-container: fetch %s: %w", link, err)
		}
		if resp.StatusCode >= 400 {
			resp.Body.Close()
			return fmt.Errorf("doaj-container: HTTP %d for %s", resp.StatusCode, link)
		}
		var result doajContainerSearchResponse
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			resp.Body.Close()
			return fmt.Errorf("doaj-container: decode response: %w", err)
		}
		resp.Body.Close()

		if page == 0 {
			total = result.Total
		}
		for _, raw := range result.Results {
			if _, err := w.Write(raw); err != nil {
				return fmt.Errorf("doaj-container: write record: %w", err)
			}
			if _, err := w.Write(bNewline); err != nil {
				return fmt.Errorf("doaj-container: write newline: %w", err)
			}
		}
		seen += int64(len(result.Results))
		page++
		log.Printf("doaj-container: page=%d seen=%d total=%d [%s – %s]",
			page, seen, total,
			from.Format("2006-01-02"), until.Format("2006-01-02"))
		if h.MaxRecords > 0 && seen >= int64(h.MaxRecords) {
			log.Printf("doaj-container: reached sync limit %d, stopping", h.MaxRecords)
			break
		}
		link = result.Next
	}
	log.Printf("doaj-container: done, seen=%d total=%d for %s",
		seen, total, from.Format("2006-01-02"))
	return nil
}
