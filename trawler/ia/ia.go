// ia exposes thin wrappers around public archive.org endpoints used by the
// periodic-ingest pipeline: advancedsearch (collection enumeration), the
// metadata API (item file listings), and /download/ (full and Range-mode
// reads of item files).
//
// All functions take an *http.Client so the caller controls auth and
// transport. Today everything is hit unauthenticated; when we need
// authenticated access (LOW credentials, or eventually the petabox
// webdata-secret path), the caller swaps in an http.Client with a
// configured Transport.
package ia

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const Host = "https://archive.org"

var (
	ErrNoCDX       = errors.New("no rollup CDX file found in item")
	ErrMultipleCDX = errors.New("multiple CDX files in item; need disambiguation")
)

type ItemFile struct {
	Name   string `json:"name"`
	Format string `json:"format"`
	Source string `json:"source"`
}

// SearchCollection lists item identifiers in an IA collection, paged. Returns
// the identifiers from this page plus hasMore=true if more pages remain.
// pageSize is the "rows" param; page is 1-indexed.
func SearchCollection(ctx context.Context, c *http.Client, collectionID string, page, pageSize int) ([]string, bool, error) {
	q := url.Values{}
	q.Set("q", "collection:"+collectionID)
	q.Set("fl", "identifier")
	q.Set("output", "json")
	q.Set("rows", fmt.Sprintf("%d", pageSize))
	q.Set("page", fmt.Sprintf("%d", page))
	u := Host + "/advancedsearch.php?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, false, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("advancedsearch GET failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return nil, false, fmt.Errorf("advancedsearch returned %d: %s", resp.StatusCode, b)
	}

	var payload struct {
		Response struct {
			NumFound int                 `json:"numFound"`
			Start    int                 `json:"start"`
			Docs     []map[string]string `json:"docs"`
		} `json:"response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, false, fmt.Errorf("advancedsearch decode failed: %w", err)
	}

	ids := make([]string, 0, len(payload.Response.Docs))
	for _, d := range payload.Response.Docs {
		if id := d["identifier"]; id != "" {
			ids = append(ids, id)
		}
	}
	hasMore := payload.Response.Start+len(payload.Response.Docs) < payload.Response.NumFound
	return ids, hasMore, nil
}

// ItemFiles returns the file list from an IA item's metadata API response.
func ItemFiles(ctx context.Context, c *http.Client, identifier string) ([]ItemFile, error) {
	u := Host + "/metadata/" + url.PathEscape(identifier)

	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("metadata GET failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("metadata returned %d: %s", resp.StatusCode, b)
	}

	var payload struct {
		Files []ItemFile `json:"files"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("metadata decode failed: %w", err)
	}
	return payload.Files, nil
}

// FindRollupCDX picks the rollup CDX file from an item's file list. Items
// produced by our periodic crawls contain one rollup CDX plus N per-WARC
// indices named *.warc.os.cdx.gz; only the rollup gets selected. Returns
// ErrNoCDX if none found, ErrMultipleCDX if more than one matches.
func FindRollupCDX(files []ItemFile) (string, error) {
	var matches []string
	for _, f := range files {
		if strings.HasSuffix(f.Name, ".cdx.gz") && !strings.HasSuffix(f.Name, ".warc.os.cdx.gz") {
			matches = append(matches, f.Name)
		}
	}
	switch len(matches) {
	case 0:
		return "", ErrNoCDX
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("%w: %v", ErrMultipleCDX, matches)
	}
}

// OpenFile returns a reader for the full contents of <identifier>/<filename>
// from archive.org/download/. Caller closes.
func OpenFile(ctx context.Context, c *http.Client, identifier, filename string) (io.ReadCloser, error) {
	u := Host + "/download/" + url.PathEscape(identifier) + "/" + url.PathEscape(filename)

	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download GET failed: %w", err)
	}
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("download returned %d: %s", resp.StatusCode, b)
	}
	return resp.Body, nil
}

// ReadRange returns the bytes from <identifier>/<filename> in [offset,
// offset+length). For .warc.gz files, the returned slice is the raw
// (still-gzipped) record envelope that gowarc can read directly.
func ReadRange(ctx context.Context, c *http.Client, identifier, filename string, offset, length int64) ([]byte, error) {
	u := Host + "/download/" + url.PathEscape(identifier) + "/" + url.PathEscape(filename)

	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, offset+length-1))

	resp, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("range GET failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 206 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("range request returned %d (expected 206): %s", resp.StatusCode, b)
	}
	return io.ReadAll(resp.Body)
}
