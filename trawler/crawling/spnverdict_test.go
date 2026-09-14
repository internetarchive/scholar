package crawling

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	cdx "git.archive.org/webgroup/scholar/trawler/cdx/cdxclient"
	spn "git.archive.org/webgroup/scholar/trawler/spn/spnclient"
)

// These cover the handoff between spnToCdx and Crawl: spnToCdx reports a
// finished-but-unsuccessful SPN job as an error, and Crawl is supposed to
// recognise the ones it has characterised and turn them into a verdict on the
// URL instead of letting them retry.
//
// That recognition is errors.As against a *SPNError, so it only works if the
// error is returned as a pointer. It was returned by value for a long time,
// which compiles, never matches, and leaves every branch below dead -- so
// these tests are as much about the shape of the error as about the verdicts.

// emptyCDX stands in for a CDX server that has never heard of the URL, which
// is what sends Crawl to SPN and, afterwards, what makes a successful capture
// look like it went missing.
func emptyCDX(t *testing.T) cdx.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	t.Cleanup(srv.Close)
	return cdx.NewClient(cdx.Config{
		Auth:       "test",
		Endpoint:   srv.URL,
		HTTPClient: srv.Client(),
	})
}

func crawlWithSPN(t *testing.T, f *fakeSPN) (CrawlResult, error) {
	t.Helper()
	t.Cleanup(spnSlotCache.reset)
	spnSlotCache.reset()

	c := quietCrawler()
	c.SPNClient = f
	c.CDXClient = emptyCDX(t)
	return c.Crawl(context.Background(), "https://example.com/paper.pdf")
}

func Test_Crawl_characterisedSPNFailuresBecomeVerdicts(t *testing.T) {
	cs := []struct {
		statusExt string
		want      string
	}{
		{"error:job-failed", "spn-error:job-failed"},
		{"error:browser-running-error", "spn-error:browser-running-error"},
		{"error:no-captures", "spn-error:no-captures"},
	}

	for _, c := range cs {
		t.Run(c.statusExt, func(t *testing.T) {
			res, err := crawlWithSPN(t, &fakeSPN{jobStatus: spn.JobStatus{
				Status:      "error",
				StatusExt:   c.statusExt,
				OriginalURL: "https://example.com/paper.pdf",
			}})

			// A verdict, not a failure: retrying will get the same answer, and
			// the outcome log wants the reason rather than an exhausted URL.
			if err != nil {
				t.Fatalf("expected the crawl to end with a verdict, got error %v", err)
			}
			if res.Success {
				t.Error("a failed SPN job was reported as a success")
			}
			if res.FailReason != c.want {
				t.Errorf("expected fail reason %q, got %q", c.want, res.FailReason)
			}
		})
	}
}

// An SPN failure we have not characterised must keep its error, so it retries
// and stays visible. Swallowing it would let a novel SPN fault mark a whole
// list as crawled-and-empty.
func Test_Crawl_unrecognisedSPNFailureStaysAnError(t *testing.T) {
	res, err := crawlWithSPN(t, &fakeSPN{jobStatus: spn.JobStatus{
		Status:      "error",
		StatusExt:   "error:something-new",
		OriginalURL: "https://example.com/paper.pdf",
	}})

	if err == nil {
		t.Fatalf("an unrecognised SPN status was swallowed as %q", res.FailReason)
	}
	var spnErr *SPNError
	if !errors.As(err, &spnErr) {
		t.Fatalf("expected the SPN error to survive, got %v", err)
	}
	if spnErr.StatusExt != "error:something-new" {
		t.Errorf("expected the status to be carried, got %q", spnErr.StatusExt)
	}
}

// The same goes for a /save response with no job id in it: there is no
// status_ext to recognise, and a malformed answer from SPN is a problem with
// SPN, not a fact about the URL.
func Test_Crawl_missingJobIDStaysAnError(t *testing.T) {
	res, err := crawlWithSPN(t, &fakeSPN{noJobID: true})

	if err == nil {
		t.Fatalf("a malformed SPN response was swallowed as %q", res.FailReason)
	}
}

// SPN says it captured the page but CDX has no row for it. Crawl has a name
// for that, and reaching it also depends on the error being a pointer.
func Test_Crawl_cdxNoRowsBecomesAVerdict(t *testing.T) {
	res, err := crawlWithSPN(t, &fakeSPN{jobStatus: spn.JobStatus{
		Status:      "success",
		OriginalURL: "https://example.com/paper.pdf",
	}})

	if err != nil {
		t.Fatalf("expected the crawl to end with a verdict, got error %v", err)
	}
	if res.FailReason != "cdx-no-rows" {
		t.Errorf("expected fail reason cdx-no-rows, got %q", res.FailReason)
	}
}
