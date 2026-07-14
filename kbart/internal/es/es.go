// Package es is a minimal read-only client for the scholar Elasticsearch
// endpoint (https://scholar.archive.org/_es). The fatcat v2 API has no
// full-text container search, so candidate containers are selected here, over
// the fatcat_container index, exactly as the original
// search_fatcat_containers.sh did with fatcat-cli.
package es

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const DefaultHost = "https://scholar.archive.org/_es"

// ContainerQuery is the preservation-threshold query used to select candidate
// containers, ported verbatim from search_fatcat_containers.sh (the positional
// fatcat-cli arguments are ANDed together here into one query_string).
const ContainerQuery = `((preservation_bright:>=20 AND preservation_none:<=5 AND preservation_dark:<=5 AND preservation_shadows_only:<=5) OR ` +
	`(preservation_bright:>=200 AND preservation_none:<=20 AND preservation_dark:<=20 AND preservation_shadows_only:<=20) OR ` +
	`(preservation_bright:>=1000 AND preservation_none:<=100 AND preservation_dark:<=100 AND preservation_shadows_only:<=100)) AND ` +
	`NOT container_type:archive AND NOT container_type:repository AND NOT publisher_type:big5 AND ` +
	`(issne:* OR issnp:*) AND issnl:*`

// Client talks to the scholar _es endpoint.
type Client struct {
	Host string
	HTTP *http.Client
}

// NewClient returns a Client with a sane default timeout. ES aggregation/scroll
// requests can be slow, so the timeout is generous.
func NewClient(host string) *Client {
	if host == "" {
		host = DefaultHost
	}
	return &Client{Host: host, HTTP: &http.Client{Timeout: 120 * time.Second}}
}

// Hit is one search hit: the base32 container ident (the ES _id) and the full
// _source document.
type Hit struct {
	Ident  string
	Source json.RawMessage
}

type searchResp struct {
	ScrollID string `json:"_scroll_id"`
	Hits     struct {
		Total struct {
			Value int `json:"value"`
		} `json:"total"`
		Hits []struct {
			ID     string          `json:"_id"`
			Source json.RawMessage `json:"_source"`
		} `json:"hits"`
	} `json:"hits"`
}

func (c *Client) postJSON(path string, body any, out any) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Post(c.Host+path, "application/json", bytes.NewReader(buf))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: HTTP %d: %s", path, resp.StatusCode, data)
	}
	return json.Unmarshal(data, out)
}

// ScrollContainers runs the given query_string over fatcat_container and invokes
// fn for every matching hit, paginating with the scroll API. batch is the page
// size (0 defaults to 1000).
func (c *Client) ScrollContainers(query string, batch int, fn func(Hit) error) error {
	if batch <= 0 {
		batch = 1000
	}
	// Open the scroll. Sorting by _doc is the cheapest total-ordering for scroll.
	req := map[string]any{
		"size":  batch,
		"sort":  []string{"_doc"},
		"query": map[string]any{"query_string": map[string]any{"query": query}},
	}
	var resp searchResp
	if err := c.postJSON("/fatcat_container/_search?scroll=5m", req, &resp); err != nil {
		return fmt.Errorf("open scroll: %w", err)
	}
	scrollID := resp.ScrollID
	defer c.clearScroll(scrollID)

	for {
		if len(resp.Hits.Hits) == 0 {
			return nil
		}
		for _, h := range resp.Hits.Hits {
			if err := fn(Hit{Ident: h.ID, Source: h.Source}); err != nil {
				return err
			}
		}
		resp = searchResp{}
		next := map[string]any{"scroll": "5m", "scroll_id": scrollID}
		if err := c.postJSON("/_search/scroll", next, &resp); err != nil {
			return fmt.Errorf("scroll: %w", err)
		}
		if resp.ScrollID != "" {
			scrollID = resp.ScrollID
		}
	}
}

func (c *Client) clearScroll(scrollID string) {
	if scrollID == "" {
		return
	}
	req, err := http.NewRequest(http.MethodDelete, c.Host+"/_search/scroll", bytes.NewReader(mustJSON(map[string]any{"scroll_id": scrollID})))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if resp, err := c.HTTP.Do(req); err == nil {
		resp.Body.Close()
	}
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
