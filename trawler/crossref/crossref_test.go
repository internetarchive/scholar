package crossref

import (
	"testing"

	"git.archive.org/webgroup/scholar/trawler/fatcat2"
	"github.com/spf13/viper"
)

func TestCrawlSkipReason(t *testing.T) {
	tests := []struct {
		name     string
		release  fatcat2.Release
		setup    func()
		expected string
	}{
		{
			name:     "no DOI",
			release:  fatcat2.Release{Type: "article-journal"},
			expected: "no-doi",
		},
		{
			name: "not paperlike",
			release: fatcat2.Release{
				Type:        "dataset",
				ExternalIDs: []fatcat2.ExternalID{{Type: "doi", Value: "10.1234/test"}},
			},
			expected: "not-paperlike",
		},
		{
			name: "blocked DOI prefix",
			release: fatcat2.Release{
				Type:        "article-journal",
				ExternalIDs: []fatcat2.ExternalID{{Type: "doi", Value: "10.blocked/test"}},
			},
			setup: func() {
				viper.Set("crawling.doi_prefix_blocklist", []string{"10.blocked/"})
			},
			expected: "blocked-doi-prefix",
		},
		// NB "no-fulltext-urls" reason isn't yet reachable due to generating URLs
		// based on DOIs but this code will likely be re-used for non crossref
		// cases.
		{
			name: "no skip reason",
			release: fatcat2.Release{
				Type:        "article-journal",
				ExternalIDs: []fatcat2.ExternalID{{Type: "doi", Value: "10.1234/test"}},
			},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			viper.Reset()
			if tt.setup != nil {
				tt.setup()
			}
			result := crawlSkipReason(tt.release)
			if result != tt.expected {
				t.Errorf("got %q, want %q", result, tt.expected)
			}
		})
	}
}
