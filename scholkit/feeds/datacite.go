package feeds

import (
	"crypto/tls"
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
	dataciteDefaultEndpoint = "https://api.datacite.org/dois"
	dataciteDefaultPageSize = 500
)

// dataciteAPIResponse is the envelope returned by the Datacite REST API for
// the /dois endpoint.  We only need Data (raw per-record JSON), Meta.Total,
// and Links.Next for cursor-based pagination.
type dataciteAPIResponse struct {
	Data []json.RawMessage `json:"data"`
	Meta struct {
		Total int64 `json:"total"`
	} `json:"meta"`
	Links struct {
		Next string `json:"next"`
	} `json:"links"`
}

// DataciteHarvester fetches DOI records from the Datacite REST API and writes
// zstd-compressed NDJSON to disk, analogous to CrossrefHarvester.
//
// Each output line is a raw JSON object matching schema/datacite.Document.
//
// The default HTTP client forces HTTP/1.1 to work around HTTP/2 GOAWAY frames
// that api.datacite.org sends mid-harvest.
type DataciteHarvester struct {
	// Client is the HTTP client to use; nil uses a built-in HTTP/1.1 client.
	Client Doer
	// ApiEndpoint is the base URL; nil defaults to dataciteDefaultEndpoint.
	ApiEndpoint string
	// PageSize is the number of records per API page; 0 defaults to
	// dataciteDefaultPageSize.
	PageSize int
	// MaxRecords limits total records written; 0 means unlimited.
	MaxRecords int
}

func (h *DataciteHarvester) endpoint() string {
	if h.ApiEndpoint != "" {
		return h.ApiEndpoint
	}
	return dataciteDefaultEndpoint
}

func (h *DataciteHarvester) pageSize() int {
	if h.PageSize > 0 {
		return h.PageSize
	}
	return dataciteDefaultPageSize
}

func (h *DataciteHarvester) client() Doer {
	if h.Client != nil {
		return h.Client
	}
	// Force HTTP/1.1: api.datacite.org sends HTTP/2 GOAWAY mid-harvest.
	return &http.Client{
		Transport: &http.Transport{
			TLSNextProto: make(map[string]func(string, *tls.Conn) http.RoundTripper),
		},
		Timeout: 30 * time.Minute,
	}
}

// DaySliceKey returns the filename for a given day's slice and the start/end
// times used, mirroring the other harvesters.
func (h *DataciteHarvester) DaySliceKey(t time.Time, prefix string) (key string, start, end time.Time) {
	start = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	end = time.Date(t.Year(), t.Month(), t.Day(), 23, 59, 59, 0, time.UTC)
	key = fmt.Sprintf("%sdatacite-%s-%s.json.zst",
		prefix, start.Format("2006-01-02"), end.Format("2006-01-02"))
	return
}

// WriteDaySlice atomically writes a zstd-compressed NDJSON file for all
// Datacite records with an updated timestamp on the given day. Idempotent once
// the file exists.
func (h *DataciteHarvester) WriteDaySlice(t time.Time, dir, prefix string) error {
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

// WriteSlice fetches all Datacite records whose updated timestamp falls between
// from and until, writing one JSON object per line (NDJSON) to w.
//
// The initial request uses cursor-based pagination; subsequent pages are
// fetched by following links.next until it is empty.
func (h *DataciteHarvester) WriteSlice(w io.Writer, from, until time.Time) error {
	vs := url.Values{}
	vs.Set("query", fmt.Sprintf("updated:[%s TO %s]",
		from.Format(time.RFC3339),
		until.Format(time.RFC3339)))
	vs.Set("state", "findable")
	vs.Set("page[cursor]", "1")
	vs.Set("page[size]", fmt.Sprintf("%d", h.pageSize()))
	vs.Set("affiliation", "true")

	link := fmt.Sprintf("%s?%s", h.endpoint(), vs.Encode())

	var (
		seen  int64
		total int64
		page  int
	)
	for link != "" {
		req, err := http.NewRequest("GET", link, nil)
		if err != nil {
			return fmt.Errorf("datacite: build request: %w", err)
		}
		req.Header.Set("Accept-Encoding", "identity") // avoid unexpected-EOF with compressed responses
		resp, err := h.client().Do(req)
		if err != nil {
			return fmt.Errorf("datacite: fetch %s: %w", link, err)
		}
		if resp.StatusCode >= 400 {
			resp.Body.Close()
			return fmt.Errorf("datacite: HTTP %d for %s", resp.StatusCode, link)
		}
		var result dataciteAPIResponse
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			resp.Body.Close()
			return fmt.Errorf("datacite: decode response: %w", err)
		}
		resp.Body.Close()

		if page == 0 {
			total = result.Meta.Total
		}
		for _, raw := range result.Data {
			if _, err := w.Write(raw); err != nil {
				return fmt.Errorf("datacite: write record: %w", err)
			}
			if _, err := w.Write(bNewline); err != nil {
				return fmt.Errorf("datacite: write newline: %w", err)
			}
		}
		seen += int64(len(result.Data))
		page++
		log.Printf("datacite: page=%d seen=%d total=%d [%s – %s]",
			page, seen, total,
			from.Format("2006-01-02"), until.Format("2006-01-02"))
		if h.MaxRecords > 0 && seen >= int64(h.MaxRecords) {
			log.Printf("datacite: reached sync limit %d, stopping", h.MaxRecords)
			break
		}
		link = result.Links.Next
	}
	log.Printf("datacite: done, seen=%d total=%d for %s",
		seen, total, from.Format("2006-01-02"))
	return nil
}
