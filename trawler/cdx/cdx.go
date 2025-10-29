// cdx defines a client for the Wayback CDX server API. It does not expose
// every parameter nor does it support every form of the parameters it does
// expose; for now, it's just enough to port sandcrawler code.
package cdx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	defaultEndpoint  = "https://web.archive.org/cdx/search/cdx"
	defaultUserAgent = "Mozilla/5.0 ia scholar trawler"
	defaultMatchType = "exact"
	defaultLimit     = 1
	defaultOutput    = "json"
	timeFormat       = "20060102150405"
)

type CDXClient struct {
	opts   CDXClientOpts
	client *http.Client
}

type CDXClientOpts struct {
	Endpoint  string
	Auth      string
	UserAgent string
	Retries   int
	// Backoff is the retry backoff factor expressed in duration format (eg, 10s)
	Backoff string
}

type CDXParams struct {
	URL       string
	From      *time.Time
	To        *time.Time
	MatchType string
	Limit     int
	Output    string
	Filters   []string
}

type CDXRow struct {
	Surt       string
	Datetime   time.Time
	URL        string
	Mimetype   string
	StatusCode int
	SHA1b32    string
	SHA1hex    string
	WarcCsize  int
	WarcOffset int
	WarcPath   string
}

func NewClient(opts CDXClientOpts) CDXClient {
	if opts.Auth == "" {
		panic("Auth required")
	}

	if opts.Endpoint == "" {
		opts.Endpoint = defaultEndpoint
	}

	if opts.UserAgent == "" {
		opts.UserAgent = defaultUserAgent
	}

	// TODO is it correct to have retry logic *inside* the client? I think it
	// should probably be handled externally.

	if opts.Retries == 0 {
		opts.Retries = 3
	}

	if opts.Backoff == "" {
		opts.Backoff = "10s"
	}

	return CDXClient{
		opts:   opts,
		client: &http.Client{},
	}
}

func (c CDXClient) Query(params CDXParams) ([]CDXRow, error) {
	out := []CDXRow{}
	req, err := http.NewRequest("GET", c.opts.Endpoint, nil)
	if err != nil {
		panic(err)
	}

	u := params.URL
	if u == "" {
		return nil, errors.New("URL required")
	}

	q := req.URL.Query()

	q.Set("url", params.URL)

	if params.From != nil {
		from := params.From.Format(timeFormat)
		q.Set("from", from)
	}

	if params.To != nil {
		to := params.To.Format(timeFormat)
		q.Set("to", to)
	}

	mt := defaultMatchType
	if params.MatchType != "" {
		mt = params.MatchType
	}
	q.Set("matchType", mt)

	l := defaultLimit
	if params.Limit > 0 {
		l = params.Limit
	}
	q.Set("limit", fmt.Sprintf("%d", l))

	o := defaultOutput
	if params.Output != "" {
		o = params.Output
	}
	q.Set("output", o)

	for _, f := range params.Filters {
		q.Add("filter", f)
	}

	req.URL.RawQuery = q.Encode()

	req.Header.Add("User-Agent", c.opts.UserAgent)
	req.Header.Add("Cookie", fmt.Sprintf("cdx_auth_token=%s", c.opts.UserAgent))

	var resp *http.Response

	backoff, err := time.ParseDuration(c.opts.Backoff)
	if err != nil {
		panic(err)
	}

	attempts := 0
	for {
		attempts++
		resp, err = c.client.Do(req)
		if err == nil || attempts == c.opts.Retries {
			break
		}

		time.Sleep(backoff * time.Duration(attempts))
	}

	if err != nil {
		return out, fmt.Errorf("cdx api call failed after %d attempts: %w", attempts, err)
	}

	bs, err := io.ReadAll(resp.Body)
	if err != nil {
		return out, fmt.Errorf("failed to read cdx api response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return out, fmt.Errorf("cdx api returned %d: '%s'", resp.StatusCode, string(bs))
	}

	var payload [][]string
	err = json.Unmarshal(bs, &payload)
	if err != nil {
		return out, fmt.Errorf("failed to unmarshal cdx api response: %w", err)
	}

	for _, r := range payload[1:] {
		if len(r) != 11 {
			return out, fmt.Errorf("cdx api returned malformed row: %v", r)
		}

		if slices.Contains(r[8:11], "-") {
			// sometimes warc fields end up empty
			continue
		}

		if strings.HasPrefix(r[5], "sha-256") {
			// do not want rows without sha1 digests
			continue
		}

		if strings.ToLower(r[5]) == "error" {
			continue
		}

		var statusCode int
		if r[4] == "-" {
			if r[3] != "warc/revisit" && strings.HasPrefix(r[2], "ftp://") {
				statusCode = 226
			}
		} else {
			statusCode, err = strconv.Atoi(r[4])
			if err != nil {
				return out, fmt.Errorf("cdx row has corrupted status code: '%s'", r[4])
			}
		}

		dateTime, err := time.Parse(timeFormat, r[1])
		if err != nil {
			return out, fmt.Errorf("cdx row has corrupted date time '%s': %w", r[1], err)
		}

		warcCsize, err := strconv.Atoi(r[8])
		if err != nil {
			return out, fmt.Errorf("cdx row has corrupted warc csize '%s': %w", r[8], err)
		}

		warcOffset, err := strconv.Atoi(r[9])
		if err != nil {
			return out, fmt.Errorf("cdx row has corrupted warc offset '%s': %w", r[9], err)
		}

		// TODO wait until I actually see this used
		var sha1hex string

		row := CDXRow{
			Surt:       r[0],
			URL:        r[2],
			Mimetype:   r[3],
			SHA1b32:    r[5],
			SHA1hex:    sha1hex,
			WarcPath:   r[10],
			StatusCode: statusCode,
			Datetime:   dateTime,
			WarcCsize:  warcCsize,
			WarcOffset: warcOffset,
		}

		out = append(out, row)
	}

	return out, nil
}
