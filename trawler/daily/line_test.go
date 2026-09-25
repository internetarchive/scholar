package daily

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"git.archive.org/webgroup/scholar/trawler/counts"
	"git.archive.org/webgroup/scholar/trawler/crawling"
	"git.archive.org/webgroup/scholar/trawler/fatcat2"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/log"
)

// crawlDeps builds the slice of lineDeps that crawlFulltext actually touches.
func crawlDeps(crawl crawlFn) lineDeps {
	return lineDeps{
		Crawl: crawl,
		Beat:  func(string) {},
		Log:   log.NewStructuredLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
	}
}

func releaseWithURLs() *fatcat2.Release {
	return &fatcat2.Release{
		ID:          newUUID(),
		Type:        "article-journal",
		ExternalIDs: []fatcat2.ExternalID{extID("doi", "10.1234/ok")},
	}
}

// countingBody reports whether the crawler's body was closed. Leaking one per
// URL is how a worker runs out of connections on a day of crossref.
type countingBody struct {
	r      io.Reader
	closed bool
}

func (c *countingBody) Read(p []byte) (int, error) { return c.r.Read(p) }
func (c *countingBody) Close() error               { c.closed = true; return nil }

// The first URL that lands a PDF wins and the rest are never tried: a release
// only needs one copy of its fulltext.
func Test_crawlFulltext_stopsAtTheFirstSuccess(t *testing.T) {
	var tried []string
	d := crawlDeps(func(_ context.Context, u string) (crawling.CrawlResult, error) {
		tried = append(tried, u)
		if u == "http://b/" {
			return crawling.CrawlResult{Success: true, SnapshotUrl: "http://web/pdf"}, nil
		}
		return crawling.CrawlResult{FailReason: "dead-end"}, nil
	})

	res, seed, reason := crawlFulltext(t.Context(), d, releaseWithURLs(),
		[]string{"http://a/", "http://b/", "http://c/"})

	require.True(t, res.Success)
	require.Equal(t, "http://web/pdf", res.SnapshotUrl)
	require.Equal(t, "http://b/", seed)
	require.Empty(t, reason)
	require.Equal(t, []string{"http://a/", "http://b/"}, tried,
		"a url after the successful one was crawled anyway")
}

// Every URL failing is a verdict on the release, and the reason reported is
// the last one -- which is what the outcome log shows for it.
func Test_crawlFulltext_reportsWhyItGaveUp(t *testing.T) {
	d := crawlDeps(func(_ context.Context, u string) (crawling.CrawlResult, error) {
		if u == "http://a/" {
			return crawling.CrawlResult{FailReason: "blocklist"}, nil
		}
		return crawling.CrawlResult{}, errors.New("spn is sad")
	})

	res, seed, reason := crawlFulltext(t.Context(), d, releaseWithURLs(),
		[]string{"http://a/", "http://b/"})

	require.False(t, res.Success)
	require.Equal(t, "http://b/", seed)
	require.Contains(t, reason, "crawl error: spn is sad")
}

// A crawler that stops without saying why still yields a usable reason rather
// than an empty column in the log.
func Test_crawlFulltext_namesAnUnexplainedFailure(t *testing.T) {
	d := crawlDeps(func(context.Context, string) (crawling.CrawlResult, error) {
		return crawling.CrawlResult{}, nil
	})

	_, _, reason := crawlFulltext(t.Context(), d, releaseWithURLs(), []string{"http://a/"})
	require.Equal(t, "unknown", reason)
}

// A release with nothing to crawl is reported as such rather than as a crawl
// that silently did nothing. shouldCrawlRelease rejects these first, so this
// is belt and braces on the reason string.
func Test_crawlFulltext_noURLs(t *testing.T) {
	d := crawlDeps(func(context.Context, string) (crawling.CrawlResult, error) {
		t.Fatal("crawled something for a release with no urls")
		return crawling.CrawlResult{}, nil
	})

	_, seed, reason := crawlFulltext(t.Context(), d, releaseWithURLs(), nil)
	require.Empty(t, seed)
	require.Equal(t, "no fulltext urls", reason)
}

// Crawl only hands back a body on success, but a body on a failed result must
// still be drained and closed: at a day's volume, one leaked connection per
// dead URL exhausts the worker.
func Test_crawlFulltext_closesBodiesOfFailedCrawls(t *testing.T) {
	body := &countingBody{r: strings.NewReader("not a pdf")}
	d := crawlDeps(func(_ context.Context, u string) (crawling.CrawlResult, error) {
		if u == "http://a/" {
			return crawling.CrawlResult{FailReason: "dead-end", Content: body}, nil
		}
		return crawling.CrawlResult{Success: true}, nil
	})

	res, _, _ := crawlFulltext(t.Context(), d, releaseWithURLs(),
		[]string{"http://a/", "http://b/"})

	require.True(t, res.Success)
	require.True(t, body.closed, "a failed crawl's body was leaked")
}

// --- what a line writes to fatcat -----------------------------------------

// fatcatStub stands in for fatcat2, fatcat1 and elasticsearch at once.
// processLine reaches all three through package level helpers rather than
// through its deps, so the seam a test has is the http.Client it hands them
// and the endpoints in viper.
type fatcatStub struct {
	mu      sync.Mutex
	created []fatcat2.File
	// unexpected collects requests the stub was not written for, so a change in
	// what processLine calls shows up as a named assertion failure rather than
	// as an error from whatever the handler happened to answer.
	unexpected []string
}

func (s *fatcatStub) files() []fatcat2.File {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]fatcat2.File(nil), s.created...)
}

func (s *fatcatStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasSuffix(r.URL.Path, "/files"):
		// fc2 ReleaseFiles: nothing is attached yet, so the line goes on to crawl.
		io.WriteString(w, `{"items":[]}`)
	case strings.HasSuffix(r.URL.Path, "/lookup"):
		// Neither fc2 (by sha256) nor fc1 (by sha1) has seen this pdf, so the
		// line creates the file rather than finding one.
		w.WriteHeader(http.StatusNotFound)
	case r.Method == http.MethodPost && r.URL.Path == "/file":
		var f fatcat2.File
		if err := json.NewDecoder(r.Body).Decode(&f); err != nil {
			s.mu.Lock()
			s.unexpected = append(s.unexpected, "undecodable file POST: "+err.Error())
			s.mu.Unlock()
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		s.created = append(s.created, f)
		s.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{}`)
	case strings.HasSuffix(r.URL.Path, "/_count"):
		// Both ES documents are already current, which stops the line before
		// GROBID: what is under test here is what went to fatcat, not indexing.
		io.WriteString(w, `{"count":1}`)
	default:
		s.mu.Lock()
		s.unexpected = append(s.unexpected, r.Method+" "+r.URL.Path)
		s.mu.Unlock()
		w.WriteHeader(http.StatusInternalServerError)
	}
}

// A file this run creates is stamped with the source the workflow pinned for
// the whole day, not with the source of the release it hangs off. Those differ
// whenever we crawl a release some earlier run (or some other upstream) put in
// fatcat, and taking the release's source would file today's acquisitions
// under that older run.
func Test_processLine_stampsTheRunsSourceOnTheFile(t *testing.T) {
	stub := &fatcatStub{}
	srv := httptest.NewServer(stub)
	t.Cleanup(srv.Close)

	viper.Set("fatcat1.endpoint", srv.URL)
	viper.Set("fatcat2.endpoint", srv.URL)
	viper.Set("indexing.elasticsearch_url", srv.URL)
	viper.Set("indexing.fatcat_file_ix", "fatcat_file")
	viper.Set("indexing.fulltext_ix", "scholar_fulltext")

	release := releaseWithURLs()
	release.Source = "some-earlier-run"

	d := crawlDeps(func(context.Context, string) (crawling.CrawlResult, error) {
		return crawling.CrawlResult{
			Success:     true,
			SnapshotUrl: "http://web/pdf",
			Mimetype:    "application/pdf; charset=binary",
			Content:     io.NopCloser(strings.NewReader("%PDF-1.4 pretend")),
		}, nil
	})
	d.HTTP = srv.Client()
	d.Source = "pinned-src"
	d.Upstream = "crossref"
	d.Process = func(context.Context, *http.Client, string, []byte) (counts.Counts, *fatcat2.Release, error) {
		return counts.Counts{}, release, nil
	}
	d.Rules = func() (crawlRules, error) { return crawlRules{}, nil }

	out, oc, err := processLine(t.Context(), d, `{"DOI":"10.1234/ok"}`)
	require.NoError(t, err)
	require.Empty(t, stub.unexpected)
	require.Equal(t, StatusOK, oc.Status)
	require.Equal(t, 1, out.Releases.Acquired)

	created := stub.files()
	require.Len(t, created, 1)
	require.Equal(t, "pinned-src", created[0].Source,
		"the file carries its release's source instead of the one this run pinned")
	require.Equal(t, "application/pdf", created[0].Mimetype)
	require.Equal(t, "http://web/pdf", oc.WaybackURL)
}

// --- outcome labelling ----------------------------------------------------

func Test_extIDLabel(t *testing.T) {
	// A DOI wins even when the release carries others, because that is what
	// joins the row back to the upstream file.
	require.Equal(t, "doi:10.1234/x", extIDLabel(&fatcat2.Release{
		ExternalIDs: []fatcat2.ExternalID{
			extID("pmid", "123"),
			extID("doi", "10.1234/x"),
		},
	}))
	require.Equal(t, "arxiv:2301.00001", extIDLabel(&fatcat2.Release{
		ExternalIDs: []fatcat2.ExternalID{extID("arxiv", "2301.00001")},
	}))
	require.Empty(t, extIDLabel(&fatcat2.Release{}))
}

func Test_detailOr(t *testing.T) {
	require.Equal(t, "created file, indexed fulltext",
		detailOr([]string{"created file", "indexed fulltext"}, "already current"))
	// A line that paid for a crawl and found everything already in place is an
	// ok, not a skip -- but it needs to say that it changed nothing.
	require.Equal(t, "already current", detailOr(nil, "already current"))
}
