package feeds

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"time"

	"github.com/internetarchive/scholar/scholkit/atomicfile"
	"github.com/jinzhu/now"
	"github.com/klauspost/compress/zstd"
)

var bNewline = []byte("\n")

// crossrefDefaultRetryDelay is the base delay between retries of a failed
// request; it is multiplied by the attempt number.
const crossrefDefaultRetryDelay = 5 * time.Second

// CrossrefHarvester fetches data from crossref API and by default will write
// it to files on disk.
type CrossrefHarvester struct {
	Client              Doer
	ApiEndpoint         string
	ApiFilter           string
	ApiEmail            string
	Rows                int
	UserAgent           string
	MaxRetries          int
	RetryDelay          time.Duration // base delay between retries, scaled by attempt
	AcceptableMissRatio float64       // recommended: 0.1
	MaxRecords          int           // 0 means unlimited
}

// retryDelay returns the base delay between retries.
func (c *CrossrefHarvester) retryDelay() time.Duration {
	if c.RetryDelay > 0 {
		return c.RetryDelay
	}
	return crossrefDefaultRetryDelay
}

// Doer abstracts https://pkg.go.dev/net/http#Client.Do.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// WorksResponse, stripped of the actual messages, as we only need the status
// and mayby total results.
type WorksResponse struct {
	Message struct {
		Facets struct {
		} `json:"facets"`
		Items        []json.RawMessage `json:"items"`
		ItemsPerPage int64             `json:"items-per-page"`
		NextCursor   string            `json:"next-cursor"` // iterate
		Query        struct {
			SearchTerms interface{} `json:"search-terms"`
			StartIndex  int64       `json:"start-index"`
		} `json:"query"`
		TotalResults int64 `json:"total-results"` // want to estimate total results (and verify download)
	} `json:"message"`
	MessageType    string `json:"message-type"`
	MessageVersion string `json:"message-version"`
	Status         string `json:"status"`
}

// crossrefHTTPError is a non-2xx status from the API, kept as a type so the
// retry loop can tell transient failures from permanent ones.
type crossrefHTTPError struct {
	Code int
	Link string
}

func (e *crossrefHTTPError) Error() string {
	return fmt.Sprintf("crossref: HTTP %d while fetching %s", e.Code, e.Link)
}

// isRetryable reports whether a failed fetch is worth repeating. Decode
// failures are assumed transient, e.g. a truncated response. Of the HTTP
// errors, 404 is the interesting one: the API intermittently returns it for a
// deep paging cursor that is perfectly valid, and the same request succeeds
// moments later. The other transient codes are normally handled by the client
// (cf. pester), but may surface here depending on the client in use.
func isRetryable(err error) bool {
	var herr *crossrefHTTPError
	if errors.As(err, &herr) {
		return herr.Code == http.StatusNotFound ||
			herr.Code == http.StatusTooManyRequests ||
			herr.Code >= 500
	}
	return true
}

// IsLast returns true, if there are no more records to fetch.
func (wr *WorksResponse) IsLast() bool {
	return wr.Message.NextCursor == ""
}

// DaySliceKey returns the filename used for a given day's slice, along with
// the start and end times for that day. Useful for callers that need to
// locate the file after WriteDaySlice has run.
func (c *CrossrefHarvester) DaySliceKey(t time.Time, prefix string) (key string, start, end time.Time) {
	start = now.With(t).BeginningOfDay()
	end = now.With(t).EndOfDay()
	key = fmt.Sprintf("%s%s-%s-%s.json.zst",
		prefix, c.ApiFilter, start.Format("2006-01-02"), end.Format("2006-01-02"))
	return
}

// WriteDaySlice is a helper function to atomically write crossref data for a
// single day to file on disk under dir. Idempotent, once the data has been
// captured. TODO: add compression.
func (c *CrossrefHarvester) WriteDaySlice(t time.Time, dir string, prefix string) error {
	fn, start, end := c.DaySliceKey(t, prefix)
	cachePath := path.Join(dir, fn)
	if _, err := os.Stat(cachePath); os.IsNotExist(err) {
		f, err := atomicfile.New(cachePath)
		if err != nil {
			return err
		}
		enc, err := zstd.NewWriter(f)
		if err != nil {
			return err
		}
		if err := c.WriteSlice(enc, start, end); err != nil {
			return err
		}
		if err := enc.Close(); err != nil {
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
	}
	return nil
}

// addOptionalEmail appends mailto parameter.
func (c *CrossrefHarvester) addOptionalEmail(vs url.Values) {
	if c.ApiEmail != "" {
		vs.Add("mailto", c.ApiEmail)
	}
}

// logSeenRatio logs some progress stats for the current works response.
func (c *CrossrefHarvester) logSeenRatio(seen int, wr *WorksResponse) {
	if wr == nil {
		return
	}
	var pct float64
	if wr.Message.TotalResults == 0 {
		pct = 0.0
	} else {
		pct = 100 * (float64(seen) / float64(wr.Message.TotalResults))
	}
	log.Printf("crossref: status=%s, total=%d, seen=%d (%0.2f%%), cursor=%s",
		wr.Status, wr.Message.TotalResults, seen, pct, wr.Message.NextCursor)
}

// fetchWorks issues a single request and decodes the response. Retries are up
// to the caller.
func (c *CrossrefHarvester) fetchWorks(link string) (*WorksResponse, error) {
	req, err := http.NewRequest("GET", link, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Add("User-Agent", c.UserAgent)
	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, &crossrefHTTPError{Code: resp.StatusCode, Link: link}
	}
	var wr WorksResponse
	if err := json.NewDecoder(resp.Body).Decode(&wr); err != nil {
		return nil, fmt.Errorf("crossref: decode failed with %v", err)
	}
	return &wr, nil
}

// WriteSlice writes a slice of data from the API into a writer.
func (c *CrossrefHarvester) WriteSlice(w io.Writer, from, until time.Time) error {
	filter := fmt.Sprintf("from-%s-date:%s,until-%s-date:%s",
		c.ApiFilter,
		from.Format("2006-01-02"),
		c.ApiFilter,
		until.Format("2006-01-02"))
	var (
		seen   int
		i      int   // for retries
		cursor = "*" // deep paging position, only carries a position, not the query
	)
	for {
		// The cursor is a bare Solr cursorMark; the query itself is not
		// encoded in it and has to be resent with every request.
		vs := url.Values{}
		vs.Add("filter", filter)
		vs.Add("rows", fmt.Sprintf("%d", c.Rows))
		vs.Add("cursor", cursor)
		c.addOptionalEmail(vs)
		link := fmt.Sprintf("%s?%s", c.ApiEndpoint, vs.Encode())
		log.Printf("crossref: attempting to fetch: %s", link)
		wr, err := c.fetchWorks(link)
		if err != nil {
			if isRetryable(err) && i < c.MaxRetries {
				i++
				log.Printf("%v, retrying [%d/%d]", err, i, c.MaxRetries)
				time.Sleep(time.Duration(i) * c.retryDelay())
				continue
			}
			return err
		}
		if wr.Status != "ok" {
			return fmt.Errorf("crossref failed with status: %s", wr.Status)
		}
		for _, item := range wr.Message.Items {
			item = append(item, bNewline...)
			if _, err := w.Write(item); err != nil {
				return err
			}
		}
		seen += len(wr.Message.Items)
		c.logSeenRatio(seen, wr)
		if c.MaxRecords > 0 && seen >= c.MaxRecords {
			log.Printf("crossref: reached sync limit %d, stopping", c.MaxRecords)
			return nil
		}
		if wr.IsLast() || seen >= int(wr.Message.TotalResults) {
			log.Printf("crossref slice done: seen=%d, total=%d", seen, wr.Message.TotalResults)
			return nil
		}
		cursor = wr.Message.NextCursor
		if cursor == "" {
			return nil
		}
		// status: ok, total: 55818, seen: 47818 (85.67%)
		// We had repeated requests, with a seemingly new cursor, but no new
		// messages and seen < total; we assume, we have got all we could and
		// move on. Note: this may be a temporary glitch; rather retry.
		if len(wr.Message.Items) == 0 {
			numMissOk := int(c.AcceptableMissRatio * float64(wr.Message.TotalResults))
			if int(wr.Message.TotalResults)-seen < numMissOk {
				log.Printf("crossref: assuming ok to skip, seen=%d, total=%d", seen, wr.Message.TotalResults)
				break
			} else {
				return fmt.Errorf("crossref: no more messages, api may have changed, total=%d, seen=%d",
					wr.Message.TotalResults, seen)
			}
		}
		i = 0
	}
	return nil
}
