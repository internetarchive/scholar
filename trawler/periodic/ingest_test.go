package periodic

import (
	"reflect"
	"testing"

	"github.com/miku/grobidclient/tei"
)

func Test_findExtIds(t *testing.T) {
	cs := []struct {
		name   string
		header *tei.GrobidBiblio
		want   [][]string
	}{
		{
			name:   "no external ids",
			header: &tei.GrobidBiblio{},
			want:   [][]string{},
		},
		{
			name:   "doi only",
			header: &tei.GrobidBiblio{DOI: "10.1234/foo"},
			want:   [][]string{{"doi", "10.1234/foo"}},
		},
		{
			name:   "pmid only",
			header: &tei.GrobidBiblio{PMID: "12345678"},
			want:   [][]string{{"pmid", "12345678"}},
		},
		{
			name:   "pmcid only",
			header: &tei.GrobidBiblio{PMCID: "PMC1234567"},
			want:   [][]string{{"pmcid", "PMC1234567"}},
		},
		{
			// the struct field is ArxivID but findExtIds keys it as "arxiv"
			name:   "arxiv only",
			header: &tei.GrobidBiblio{ArxivID: "2301.00001"},
			want:   [][]string{{"arxiv", "2301.00001"}},
		},
		{
			name: "doi and arxiv, doi ranks first",
			header: &tei.GrobidBiblio{
				DOI:     "10.1234/foo",
				ArxivID: "2301.00001",
			},
			want: [][]string{
				{"doi", "10.1234/foo"},
				{"arxiv", "2301.00001"},
			},
		},
		{
			// fields set in reverse-priority order to prove the output is
			// ordered by id priority, not by which fields happen to be present.
			name: "pmid and pmcid stay in priority order",
			header: &tei.GrobidBiblio{
				PMCID: "PMC1234567",
				PMID:  "12345678",
			},
			want: [][]string{
				{"pmid", "12345678"},
				{"pmcid", "PMC1234567"},
			},
		},
		{
			name: "all four ids present, in priority order",
			header: &tei.GrobidBiblio{
				DOI:     "10.1234/foo",
				PMID:    "12345678",
				PMCID:   "PMC1234567",
				ArxivID: "2301.00001",
			},
			want: [][]string{
				{"doi", "10.1234/foo"},
				{"pmid", "12345678"},
				{"pmcid", "PMC1234567"},
				{"arxiv", "2301.00001"},
			},
		},
	}

	for _, c := range cs {
		t.Run(c.name, func(t *testing.T) {
			got := findExtIds(tei.GrobidDocument{Header: c.header})
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("findExtIds() = %#v, want %#v", got, c.want)
			}
		})
	}
}
