package etd

import (
	"bufio"
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"time"

	"git.archive.org/webgroup/scholar/trawler/httpclient"
	"github.com/spf13/viper"
)

// userAgent is the archive bot UA the PDF crawler uses for wayback replays.
const userAgent = "Mozilla/5.0 (compatible; archive.org_bot +http://www.archive.org/details/archive.org_bot)"

// PermanentFetchError is a fetch that will fail the same way next time: the
// capture is gone, or what is there is not a PDF. It is a verdict on one
// fulltext entry rather than an error worth retrying.
type PermanentFetchError struct{ Reason string }

func (e *PermanentFetchError) Error() string { return e.Reason }

func isPermanent(err error) bool {
	var p *PermanentFetchError
	return errors.As(err, &p)
}

// Checksums is what the metadata side needs from a PDF: enough to look it up
// and to describe it as a fatcat file, without keeping its bytes.
type Checksums struct {
	Size   int
	Sha1   string
	Sha256 string
	Md5    string
	// WaybackURL is the replay URL the fetch actually landed on, in plain
	// (not id_) form -- for an inexact fulltext entry this is the only place
	// its capture timestamp comes from.
	WaybackURL string
}

// Fetcher gets PDFs out of wayback.
type Fetcher interface {
	Checksums(ctx context.Context, fetchURL string) (Checksums, error)
	Bytes(ctx context.Context, fetchURL string) ([]byte, error)
}

type waybackFetcher struct {
	client   *http.Client
	retries  int
	backoff  time.Duration
	stall    time.Duration
	maxBytes int64
}

func newWaybackFetcher(maxBytes int64) *waybackFetcher {
	f := &waybackFetcher{
		client:   httpclient.Shared(),
		retries:  max(viper.GetInt("wayback.retries"), 1),
		backoff:  viper.GetDuration("wayback.backoff"),
		stall:    60 * time.Second,
		maxBytes: maxBytes,
	}
	if viper.IsSet("crawling.body_stall_timeout") {
		f.stall = viper.GetDuration("crawling.body_stall_timeout")
	}
	return f
}

// get returns a 200 response for url, retrying what looks transient.
func (f *waybackFetcher) get(ctx context.Context, url string) (*http.Response, context.CancelFunc, error) {
	var lastErr error
	for attempt := 1; attempt <= f.retries; attempt++ {
		if attempt > 1 {
			if err := sleepCtx(ctx, f.backoff*time.Duration(attempt-1)); err != nil {
				return nil, nil, err
			}
		}
		reqCtx, cancel := context.WithCancel(ctx)
		req, err := http.NewRequestWithContext(reqCtx, "GET", url, nil)
		if err != nil {
			cancel()
			return nil, nil, &PermanentFetchError{Reason: "bad fetch url: " + err.Error()}
		}
		req.Header.Set("User-Agent", userAgent)
		resp, err := f.client.Do(req)
		if err != nil {
			cancel()
			lastErr = err
			continue
		}
		switch {
		case resp.StatusCode == http.StatusOK:
			resp.Body = httpclient.StallGuard(resp.Body, cancel, f.stall)
			return resp, cancel, nil
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			httpclient.DrainAndClose(resp)
			cancel()
			lastErr = fmt.Errorf("wayback returned %d", resp.StatusCode)
		default:
			httpclient.DrainAndClose(resp)
			cancel()
			return nil, nil, &PermanentFetchError{Reason: fmt.Sprintf("wayback returned %d", resp.StatusCode)}
		}
	}
	return nil, nil, fmt.Errorf("wayback fetch of %s failed after %d attempts: %w", url, f.retries, lastErr)
}

// pdfMagicWindow is how far into the body "%PDF-" may start. The spec says
// byte 0, but real-world PDFs often carry some junk first.
const pdfMagicWindow = 1024

func (f *waybackFetcher) Checksums(ctx context.Context, fetchURL string) (Checksums, error) {
	resp, cancel, err := f.get(ctx, fetchURL)
	if err != nil {
		return Checksums{}, err
	}
	defer cancel()
	defer resp.Body.Close()

	br := bufio.NewReaderSize(resp.Body, pdfMagicWindow)
	head, _ := br.Peek(pdfMagicWindow)
	if !bytes.Contains(head, []byte("%PDF-")) {
		return Checksums{}, &PermanentFetchError{Reason: "not a pdf"}
	}

	h1, h256, h5 := sha1.New(), sha256.New(), md5.New()
	n, err := io.Copy(io.MultiWriter(h1, h256, h5), io.LimitReader(br, f.maxBytes+1))
	if err != nil {
		return Checksums{}, fmt.Errorf("reading %s: %w", fetchURL, err)
	}
	if n > f.maxBytes {
		return Checksums{}, &PermanentFetchError{Reason: fmt.Sprintf("pdf larger than %d bytes", f.maxBytes)}
	}
	return Checksums{
		Size:       int(n),
		Sha1:       hex.EncodeToString(h1.Sum(nil)),
		Sha256:     hex.EncodeToString(h256.Sum(nil)),
		Md5:        hex.EncodeToString(h5.Sum(nil)),
		WaybackURL: plainReplayURL(resp.Request.URL.String()),
	}, nil
}

func (f *waybackFetcher) Bytes(ctx context.Context, fetchURL string) ([]byte, error) {
	resp, cancel, err := f.get(ctx, fetchURL)
	if err != nil {
		return nil, err
	}
	defer cancel()
	defer resp.Body.Close()
	bs, err := io.ReadAll(io.LimitReader(resp.Body, f.maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", fetchURL, err)
	}
	if int64(len(bs)) > f.maxBytes {
		return nil, &PermanentFetchError{Reason: fmt.Sprintf("pdf larger than %d bytes", f.maxBytes)}
	}
	return bs, nil
}

var idReplay = regexp.MustCompile(`^(https?://web\.archive\.org/web/\d{14})id_/(.+)$`)

// plainReplayURL turns a raw-bytes replay URL (.../web/TSid_/URL) into the
// ordinary replay URL (.../web/TS/URL) stored on file records. Anything else
// comes back unchanged.
func plainReplayURL(u string) string {
	if m := idReplay.FindStringSubmatch(u); m != nil {
		return m[1] + "/" + m[2]
	}
	return u
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
