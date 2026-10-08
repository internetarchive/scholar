package indexing

import (
	"testing"

	fc2 "git.archive.org/webgroup/scholar/trawler/fatcat2"
	"github.com/stretchr/testify/require"
)

func Test_PrepareFatcatContainerDoc_InDOAJ(t *testing.T) {
	cases := []struct {
		name  string
		extra map[string]any
		want  bool
	}{
		{"doaj extra", map[string]any{"doaj": map[string]any{"id": "abc"}}, true},
		{"empty doaj extra", map[string]any{"doaj": map[string]any{}}, true},
		{"legacy doaj extra", map[string]any{"doaj": map[string]any{"as_of": "2021-01-01"}}, true},
		{"no doaj extra", map[string]any{"country_name": "Japan"}, false},
		{"nil extra", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			doc := PrepareFatcatContainerDoc(fc2.Container{Extra: c.extra})
			require.Equal(t, c.want, doc.InDOAJ)
		})
	}
}
