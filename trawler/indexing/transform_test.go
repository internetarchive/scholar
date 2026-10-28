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

func Test_doiRegistrar(t *testing.T) {
	cases := []struct {
		name  string
		extra map[string]any
		want  string
	}{
		{"crossref", map[string]any{"crossref": map[string]any{"type": "journal-article"}}, "crossref"},
		{"datacite", map[string]any{"datacite": map[string]any{"resourceTypeGeneral": "Dataset"}}, "datacite"},
		// The datacite importer writes extra.datacite even when it has nothing
		// to put in it.
		{"empty datacite", map[string]any{"datacite": map[string]any{}}, "datacite"},
		{"legacy jalc", map[string]any{"jalc": map[string]any{}}, "jalc"},
		{"no registrar", map[string]any{"container_name": "A Journal"}, ""},
		{"nil extra", nil, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.want, doiRegistrar(c.extra))
		})
	}
}

func Test_releaseOA(t *testing.T) {
	doajSynced := &fc2.Container{Extra: map[string]any{"doaj": map[string]any{"id": "abc"}}}
	legacy := &fc2.Container{Extra: map[string]any{"country": "br", "publisher_type": "longtail"}}
	doajArticle := fc2.Release{ExternalIDs: []fc2.ExternalID{{Type: "doaj", Value: "abc"}}}
	crossref := fc2.Release{Extra: map[string]any{"crossref": map[string]any{}}}
	cases := []struct {
		name         string
		release      fc2.Release
		container    *fc2.Container
		hasFiles     bool
		wantOA       bool
		wantLongtail bool
	}{
		{"nothing", fc2.Release{}, nil, false, false, false},
		{"cc license", fc2.Release{LicenseSlug: "CC-BY"}, nil, false, true, false},
		{"arxiv license", fc2.Release{LicenseSlug: "arxiv-1.0"}, nil, false, true, false},
		{"non-oa license", fc2.Release{LicenseSlug: "elsevier-specific"}, nil, false, false, false},
		// A DOAJ-synced container has extra but no license; that must not hide
		// the release's other OA signals.
		{"doaj article in doaj-synced container", doajArticle, doajSynced, false, true, false},
		{"doaj article without container", doajArticle, nil, false, true, false},
		{"crossref with file in container with extra", crossref, legacy, true, true, false},
		{"crossref without file", crossref, legacy, false, false, false},
		{"extra is_oa", fc2.Release{Extra: map[string]any{"is_oa": true}}, legacy, false, true, false},
		{"extra is_oa false", fc2.Release{Extra: map[string]any{"is_oa": false}}, nil, false, false, false},
		{"extra longtail_oa", fc2.Release{Extra: map[string]any{"longtail_oa": true}}, legacy, false, true, true},
		{"container road", fc2.Release{}, &fc2.Container{Extra: map[string]any{"road": map[string]any{"as_of": "2018"}}}, false, true, false},
		{"container szczepanski", fc2.Release{}, &fc2.Container{Extra: map[string]any{"szczepanski": map[string]any{"as_of": "2018"}}}, false, true, false},
		{"container szczepanski big5", fc2.Release{}, &fc2.Container{Extra: map[string]any{"szczepanski": map[string]any{"as_of": "2018"}, "publisher_type": "big5"}}, false, false, false},
		{"container default_license", fc2.Release{}, &fc2.Container{Extra: map[string]any{"default_license": "CC-BY-SA"}}, false, true, false},
		{"container doaj.default_license", fc2.Release{}, &fc2.Container{Extra: map[string]any{"doaj": map[string]any{"default_license": "CC-BY"}}}, false, true, false},
		{"container non-oa default_license", fc2.Release{}, &fc2.Container{Extra: map[string]any{"default_license": "all-rights-reserved"}}, false, false, false},
		{"container ia longtail_oa", fc2.Release{}, &fc2.Container{Extra: map[string]any{"ia": map[string]any{"longtail_oa": true}}}, false, true, true},
		{"container with unrelated extra", fc2.Release{}, legacy, false, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			isOA, isLongtail := releaseOA(c.release, c.container, c.hasFiles)
			require.Equal(t, c.wantOA, isOA)
			require.Equal(t, c.wantLongtail, isLongtail)
		})
	}
}

func Test_containerInDOAJ(t *testing.T) {
	require.False(t, containerInDOAJ(nil))
	require.False(t, containerInDOAJ(&fc2.Container{}))
	require.False(t, containerInDOAJ(&fc2.Container{Extra: map[string]any{"road": map[string]any{}}}))
	require.True(t, containerInDOAJ(&fc2.Container{Extra: map[string]any{"doaj": map[string]any{"as_of": "2022-07-29"}}}))
}

func Test_generateTags(t *testing.T) {
	cases := []struct {
		name      string
		biblio    ScholarBiblioV1
		container *fc2.Container
		want      []string
	}{
		{"nothing", ScholarBiblioV1{}, nil, []string{}},
		{"cc license", ScholarBiblioV1{BiblioCommonV1: BiblioCommonV1{LicenseSlug: "CC-BY"}}, nil, []string{"oa"}},
		{"arxiv license", ScholarBiblioV1{BiblioCommonV1: BiblioCommonV1{LicenseSlug: "ARXIV-1.0"}}, nil, []string{"oa"}},
		{"jstor", ScholarBiblioV1{BiblioCommonV1: BiblioCommonV1{DOIPrefix: "10.2307"}}, nil, []string{"jstor"}},
		{"doaj container", ScholarBiblioV1{}, &fc2.Container{Extra: map[string]any{"doaj": map[string]any{"id": "x"}}}, []string{"doaj", "oa"}},
		{"szczepanski big5", ScholarBiblioV1{}, &fc2.Container{Extra: map[string]any{"szczepanski": map[string]any{}, "publisher_type": "big5"}}, []string{"szczepanski"}},
		{"longtail", ScholarBiblioV1{}, &fc2.Container{Extra: map[string]any{"ia": map[string]any{"longtail_oa": true}}}, []string{"longtail", "oa"}},
		{"platform", ScholarBiblioV1{}, &fc2.Container{Extra: map[string]any{"platform": "OJS"}}, []string{"ojs"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.ElementsMatch(t, c.want, generateTags(c.biblio, c.container))
		})
	}
}
