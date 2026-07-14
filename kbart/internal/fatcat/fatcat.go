// Package fatcat is a small read-only client for the fatcat v2 API
// (https://scholar.archive.org/api/fatcat/v2). It covers the container
// metadata, stats, and preservation-histogram endpoints this tool needs.
//
// Note: the v2 stats/preservation endpoints are backed by Elasticsearch
// aggregations; when ES is unavailable they return empty stats ({} / []) rather
// than an error.
package fatcat

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const DefaultAPIHost = "https://scholar.archive.org/api/fatcat/v2"

// Container is the subset of a fatcat v2 container record this tool uses. Ident
// is the base32 form and is not part of the API response; callers fill it in.
type Container struct {
	Ident         string         `json:"-"`
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	ContainerType string         `json:"container_type"`
	Publisher     string         `json:"publisher"`
	ISSNL         string         `json:"issnl"`
	ISSNE         string         `json:"issne"`
	ISSNP         string         `json:"issnp"`
	Extra         map[string]any `json:"extra"`
}

// Preservation is the aggregate preservation breakdown returned inside Stats.
// The v2 API no longer reports shadows_only (it was present in the v1 web-host
// stats); it is treated as zero throughout this tool.
type Preservation struct {
	Bright int `json:"bright"`
	Dark   int `json:"dark"`
	None   int `json:"none"`
	Total  int `json:"total"`
}

// Stats is the container /stats response.
type Stats struct {
	Total        int            `json:"total"`
	InWeb        int            `json:"in_web"`
	InKbart      int            `json:"in_kbart"`
	IsPreserved  int            `json:"is_preserved"`
	Preservation Preservation   `json:"preservation"`
	ReleaseType  map[string]int `json:"release_type"`
}

// YearBucket is one bucket of the preservation_by_year histogram.
type YearBucket struct {
	Year   int `json:"year"`
	Bright int `json:"bright"`
	Dark   int `json:"dark"`
	None   int `json:"none"`
}

// VolumeBucket is one bucket of the preservation_by_volume histogram. The v2
// API returns volume as a string (e.g. "7"); it is not always a clean integer,
// so the eligibility logic parses it and rejects containers whose volumes are
// not integer-contiguous.
type VolumeBucket struct {
	Volume string `json:"volume"`
	Bright int    `json:"bright"`
	Dark   int    `json:"dark"`
	None   int    `json:"none"`
}

// TypeBucket is one bucket of the preservation_by_type histogram.
type TypeBucket struct {
	ReleaseType string `json:"release_type"`
	Bright      int    `json:"bright"`
	Dark        int    `json:"dark"`
	None        int    `json:"none"`
	Total       int    `json:"total"`
}

type histogramResp[T any] struct {
	ContainerID string `json:"container_id"`
	Histogram   []T    `json:"histogram"`
}

// Client is a fatcat v2 API client with simple retry/backoff, mirroring the
// requests_retry_session behavior of the original Python (retry on 5xx and
// transport errors with exponential backoff).
type Client struct {
	APIHost string
	HTTP    *http.Client
	Retries int
	Backoff time.Duration // base backoff; grows linearly per attempt
}

// NewClient returns a Client with sensible defaults.
func NewClient(apiHost string) *Client {
	if apiHost == "" {
		apiHost = DefaultAPIHost
	}
	return &Client{
		APIHost: apiHost,
		HTTP:    &http.Client{Timeout: 60 * time.Second},
		Retries: 10,
		Backoff: 3 * time.Second,
	}
}

// getJSON fetches path (relative to APIHost) and decodes the body into v,
// retrying on 5xx responses and transport errors.
func (c *Client) getJSON(path string, v any) error {
	url := c.APIHost + path
	var lastErr error
	for attempt := 0; attempt <= c.Retries; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * c.Backoff)
		}
		resp, err := c.HTTP.Get(url)
		if err != nil {
			lastErr = err
			continue
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		switch {
		case resp.StatusCode == http.StatusOK:
			if err := json.Unmarshal(body, v); err != nil {
				return fmt.Errorf("decode %s: %w", url, err)
			}
			return nil
		case resp.StatusCode >= 500:
			lastErr = fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
			continue
		default:
			return fmt.Errorf("%s: HTTP %d: %s", url, resp.StatusCode, truncate(body))
		}
	}
	return fmt.Errorf("giving up after %d retries: %w", c.Retries, lastErr)
}

func truncate(b []byte) string {
	const max = 200
	if len(b) > max {
		return string(b[:max])
	}
	return string(b)
}

// Container fetches container metadata by UUID.
func (c *Client) Container(uuid string) (*Container, error) {
	var out Container
	if err := c.getJSON("/container/"+uuid, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Stats fetches the /stats aggregate for a container by UUID.
func (c *Client) Stats(uuid string) (*Stats, error) {
	var out Stats
	if err := c.getJSON("/container/"+uuid+"/stats", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// PreservationByYear fetches the year histogram for a container by UUID.
func (c *Client) PreservationByYear(uuid string) ([]YearBucket, error) {
	var out histogramResp[YearBucket]
	if err := c.getJSON("/container/"+uuid+"/preservation_by_year", &out); err != nil {
		return nil, err
	}
	return out.Histogram, nil
}

// PreservationByVolume fetches the volume histogram for a container by UUID.
func (c *Client) PreservationByVolume(uuid string) ([]VolumeBucket, error) {
	var out histogramResp[VolumeBucket]
	if err := c.getJSON("/container/"+uuid+"/preservation_by_volume", &out); err != nil {
		return nil, err
	}
	return out.Histogram, nil
}

// PreservationByType fetches the release-type histogram for a container by UUID.
func (c *Client) PreservationByType(uuid string) ([]TypeBucket, error) {
	var out histogramResp[TypeBucket]
	if err := c.getJSON("/container/"+uuid+"/preservation_by_type", &out); err != nil {
		return nil, err
	}
	return out.Histogram, nil
}
