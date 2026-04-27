package daily

import (
	"testing"

	"git.archive.org/webgroup/scholar/trawler/fatcat2"
	"github.com/google/uuid"
)

func extID(t, v string) fatcat2.ExternalID {
	return fatcat2.ExternalID{Type: t, Value: v}
}

func newUUID() uuid.UUID {
	uid, err := uuid.NewV7()
	if err != nil {
		panic(err)
	}

	return uid
}

func Test_shouldCrawlRelease(t *testing.T) {
	cs := []struct {
		name           string
		release        fatcat2.Release
		doiPrefixBlock []string
		expectCrawl    bool
		expectReason   string
	}{
		{
			name: "not paperlike",
			release: fatcat2.Release{
				ID:          newUUID(),
				Type:        "dataset",
				ExternalIDs: []fatcat2.ExternalID{extID("doi", "10.1234/foo")},
			},
			expectCrawl:  false,
			expectReason: "not-paperlike",
		},
		{
			name: "paperlike but no external IDs",
			release: fatcat2.Release{
				ID:   newUUID(),
				Type: "article-journal",
			},
			expectCrawl:  false,
			expectReason: "no-extids",
		},
		{
			name: "DOI prefix blocked",
			release: fatcat2.Release{
				ID:          newUUID(),
				Type:        "article-journal",
				ExternalIDs: []fatcat2.ExternalID{extID("doi", "10.6084/blocked-figshare")},
			},
			doiPrefixBlock: []string{"10.6084/"},
			expectCrawl:    false,
			expectReason:   "doi-prefix-blocked:10.6084/",
		},
		{
			name: "paperlike with extid but no fulltext URLs",
			release: fatcat2.Release{
				ID:   newUUID(),
				Type: "article-journal",
				// pmid alone produces no fulltext URL
				ExternalIDs: []fatcat2.ExternalID{extID("pmid", "12345678")},
			},
			expectCrawl:  false,
			expectReason: "no-fulltext-urls",
		},
		{
			name: "happy path via arxiv ID",
			release: fatcat2.Release{
				ID:          newUUID(),
				Type:        "article-journal",
				ExternalIDs: []fatcat2.ExternalID{extID("arxiv", "2301.00001")},
			},
			expectCrawl: true,
		},
		{
			name: "happy path via DOI not on blocklist",
			release: fatcat2.Release{
				ID:          newUUID(),
				Type:        "article-journal",
				ExternalIDs: []fatcat2.ExternalID{extID("doi", "10.1234/ok")},
			},
			doiPrefixBlock: []string{"10.6084/"},
			expectCrawl:    true,
		},
		{
			name: "happy path via pmcid",
			release: fatcat2.Release{
				ID:          newUUID(),
				Type:        "article-journal",
				ExternalIDs: []fatcat2.ExternalID{extID("pmcid", "PMC1234567")},
			},
			expectCrawl: true,
		},
		{
			name: "book type is paperlike",
			release: fatcat2.Release{
				ID:          newUUID(),
				Type:        "book",
				ExternalIDs: []fatcat2.ExternalID{extID("doi", "10.1234/book")},
			},
			expectCrawl: true,
		},
		{
			name: "second DOI prefix blocked",
			release: fatcat2.Release{
				ID:          newUUID(),
				Type:        "article-journal",
				ExternalIDs: []fatcat2.ExternalID{extID("doi", "10.5281/zenodo.123")},
			},
			doiPrefixBlock: []string{"10.6084/", "10.5281/"},
			expectCrawl:    false,
			expectReason:   "doi-prefix-blocked:10.5281/",
		},
	}

	for _, c := range cs {
		t.Run(c.name, func(t *testing.T) {
			ok, reason := shouldCrawlRelease(&c.release, c.doiPrefixBlock)
			if ok != c.expectCrawl {
				t.Errorf("expected crawl=%v, got crawl=%v (reason=%q)", c.expectCrawl, ok, reason)
			}
			if c.expectReason != reason {
				t.Errorf("expected reason %q, got %q", c.expectReason, reason)
			}
		})
	}
}
