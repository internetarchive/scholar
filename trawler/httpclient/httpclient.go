// Package httpclient hands out the HTTP clients trawler makes outbound calls
// with, so that they share one connection pool sized for crawl concurrency.
//
// The reason this package exists is a default that is wrong for us. A client
// built as &http.Client{} has a nil Transport and so uses http.DefaultTransport,
// which keeps MaxIdleConnsPerHost (2) idle connections per host. Every trawler
// client written that way therefore shares one process-wide pool two
// connections deep. That is fine for a handful of calls and quietly awful at
// crawl concurrency: SPN, CDX and the Wayback replay fetches all talk to
// web.archive.org, so past two in-flight requests every additional one opens a
// fresh connection, pays a TLS handshake, and is dropped rather than pooled on
// the way out. The cost scales linearly with concurrency, in latency on every
// request and in sockets left in TIME_WAIT.
//
// Clients here are built on first use rather than at init, because they read
// their configuration from viper and package initialisation runs before the
// config file is loaded.
package httpclient

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spf13/viper"
)

const (
	// defaultMaxIdleConnsPerHost is the number that matters. It wants to be at
	// least the number of requests in flight to one host, which for a crawl is
	// roughly the worker's activity slots; below that, connections are closed
	// as fast as they are opened.
	defaultMaxIdleConnsPerHost = 64
	defaultMaxIdleConns        = 512
	defaultIdleConnTimeout     = 90 * time.Second
	// defaultResponseHeaderTimeout bounds a connected-but-silent server. It is
	// deliberately generous: CDX in particular can take a long time to answer,
	// and this is here to break a wedged crawl loose, not to enforce a latency
	// budget. It does not limit reading the body, so a slow multi-megabyte PDF
	// replay is unaffected.
	defaultResponseHeaderTimeout = 120 * time.Second
)

func durationOr(key string, fallback time.Duration) time.Duration {
	// An explicit zero means "off", which is worth being able to say; an absent
	// key means "we have no opinion", which is not the same thing.
	if viper.IsSet(key) {
		return viper.GetDuration(key)
	}
	return fallback
}

func intOr(key string, fallback int) int {
	if n := viper.GetInt(key); n > 0 {
		return n
	}
	return fallback
}

// StallGuard wraps a response body so that a read making no progress for
// timeout aborts the request instead of blocking forever.
//
// It exists because none of the transport's timeouts cover the body. Dial, TLS
// and response-header timeouts all fire before the first byte; after that a
// server can dribble, or go quiet while holding the connection open, and the
// read blocks with nothing to interrupt it. For a crawl that means an activity
// slot held until the worker restarts, because the crawl goroutine outlives
// Temporal giving up on it.
//
// cancel must be the CancelFunc of the context the request was issued with:
// cancelling the request is what makes a blocked read return. The guard owns it
// from here and calls it on Close, so the body decides how long the request's
// context lives -- which is what lets a response body outlive the function that
// fetched it, as Crawl's does. A timeout of zero or less installs no watchdog
// but still cancels on Close.
//
// The clock starts when the response arrives and resets at the start of every
// Read, so this bounds a lack of progress rather than total duration: a large
// PDF that streams steadily is never interrupted, however long it takes. A
// caller sitting on an open body without reading it is treated the same as a
// server that has stopped sending, which is the honest reading of "no
// progress".
func StallGuard(body io.ReadCloser, cancel context.CancelFunc, timeout time.Duration) io.ReadCloser {
	g := &stallGuard{body: body, cancel: cancel, timeout: timeout}
	if timeout > 0 {
		g.timer = time.AfterFunc(timeout, func() {
			g.fired.Store(true)
			cancel()
		})
	}
	return g
}

type stallGuard struct {
	body    io.ReadCloser
	cancel  context.CancelFunc
	timeout time.Duration
	timer   *time.Timer
	fired   atomic.Bool
	once    sync.Once
}

func (g *stallGuard) Read(p []byte) (int, error) {
	if g.timer != nil {
		g.timer.Reset(g.timeout)
	}
	n, err := g.body.Read(p)
	if err != nil && g.fired.Load() {
		// The read was aborted by the watchdog, so the transport's error is
		// some flavour of "context canceled". Say what actually happened, since
		// this is the line an operator will be reading.
		return n, fmt.Errorf("body read stalled for %s: %w", g.timeout, err)
	}
	return n, err
}

func (g *stallGuard) Close() error {
	g.once.Do(func() {
		if g.timer != nil {
			g.timer.Stop()
		}
		g.cancel()
	})
	return g.body.Close()
}

// MaxDrainBytes bounds how much of an unwanted body DrainAndClose reads before
// closing it. It is exported because it is part of that function's contract:
// a body larger than this is closed part-read rather than downloaded.
// Redirect and error bodies are small, so this is generous; the cap is what
// keeps a surprise multi-megabyte body from being downloaded just to be
// discarded.
const MaxDrainBytes = 64 << 10

// DrainAndClose finishes with a response whose body is not going to be used.
// It reads a bounded amount so the transport can put the connection back in
// its pool, then closes it.
//
// This is not housekeeping: an undrained, unclosed body keeps both its
// connection and the transport's read goroutine alive until the far end gives
// up on us. At one leak per retry or redirect hop and crawls running in the
// hundreds, that is a file descriptor exhaustion bug, and it shows up as
// unrelated failures long after the requests that caused it.
func DrainAndClose(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, MaxDrainBytes))
	resp.Body.Close()
}

// Transport is the connection pool shared by every client in this package.
//
// It deliberately leaves MaxConnsPerHost unset. That setting looks like a
// safety belt but is really a hidden throttle: over the limit, Do blocks
// waiting for a connection, which would show up as crawls mysteriously slowing
// down with nothing in the logs. Concurrency belongs to the workflow and the
// worker's activity slots, where it is visible and tunable.
var Transport = sync.OnceValue(newTransport)

// newTransport is split out from Transport so a test can build one per case;
// the exported entry point reads its config exactly once per process.
func newTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConnsPerHost = intOr("http.max_idle_conns_per_host", defaultMaxIdleConnsPerHost)
	t.MaxIdleConns = intOr("http.max_idle_conns", defaultMaxIdleConns)
	t.IdleConnTimeout = durationOr("http.idle_conn_timeout", defaultIdleConnTimeout)
	t.ResponseHeaderTimeout = durationOr("http.response_header_timeout", defaultResponseHeaderTimeout)
	return t
}

// Shared is the ordinary client: pooled connections, redirects followed.
//
// It sets no Timeout on purpose. A client timeout covers the whole exchange
// including reading the body, so any value large enough for a big PDF replay is
// too large to catch a stall. The transport's dial, TLS and response-header
// timeouts bound the parts that can wedge; bounding the body read wants a
// context from the caller.
var Shared = sync.OnceValue(func() *http.Client {
	return &http.Client{Transport: Transport()}
})

// NoRedirect is the same client for callers that need to read a redirect rather
// than follow it -- the crawler inspects Location headers on Wayback replays to
// walk a redirect chain itself.
var NoRedirect = sync.OnceValue(func() *http.Client {
	return &http.Client{
		Transport: Transport(),
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
})
