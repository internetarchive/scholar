package openalex

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"git.archive.org/webgroup/scholar/trawler/containers"
	"git.archive.org/webgroup/scholar/trawler/containers/containerstest"
	"git.archive.org/webgroup/scholar/trawler/fatcat2"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// A trimmed real record from data/jsonl/sources/updated_date=2026-10-14.
const sampleSource = `{"id": "https://openalex.org/S7407089605", "issn_l": "3063-556X", "issn": ["3063-556X"], "display_name": "Prosiding Seminar Nasional Sanata Dharma Berbagi", "host_organization": null, "host_organization_name": null, "works_count": 0, "summary_stats": {"h_index": 0}, "homepage_url": null, "country_code": null, "type": "journal", "counts_by_year": [], "updated_date": "2026-10-14T10:02:22.000Z"}`

func Test_parseSources(t *testing.T) {
	in := sampleSource + "\n" +
		`{"id":"https://openalex.org/S1","issn_l":"0915-2245","issn":["0915-2245","1881-3933"],"display_name":"A Journal","host_organization_name":"Pub","homepage_url":"http://example.com/","country_code":"JP","type":"journal"}` + "\n"

	recs, err := parseSources(strings.NewReader(in))
	require.NoError(t, err)
	require.Len(t, recs, 2)
	require.Equal(t, sourceRecord{
		ID:          "https://openalex.org/S7407089605",
		ISSNL:       "3063-556X",
		ISSN:        []string{"3063-556X"},
		DisplayName: "Prosiding Seminar Nasional Sanata Dharma Berbagi",
		Type:        "journal",
	}, recs[0])
	require.Equal(t, sourceRecord{
		ID:                   "https://openalex.org/S1",
		ISSNL:                "0915-2245",
		ISSN:                 []string{"0915-2245", "1881-3933"},
		DisplayName:          "A Journal",
		HostOrganizationName: "Pub",
		HomepageURL:          "http://example.com/",
		CountryCode:          "JP",
		Type:                 "journal",
	}, recs[1])
}

func Test_parseSources_Truncated(t *testing.T) {
	_, err := parseSources(strings.NewReader(sampleSource[:40]))
	require.Error(t, err)
}

func Test_resolveISSNs(t *testing.T) {
	// Both of a journal's ISSNs map to the same ISSN-L.
	got := resolveISSNs(sourceRecord{ISSNL: "0915-2245", ISSN: []string{"0915-2245", "1881-3933"}})
	require.Equal(t, sourceISSNs{
		ISSNL:        "0915-2245",
		ISSNs:        []string{"0915-2245", "1881-3933"},
		LinkingISSNs: []string{"0915-2245"},
	}, got)

	// OpenAlex's issn_l is not taken on its own say-so.
	got = resolveISSNs(sourceRecord{ISSNL: containerstest.NewISSN, ISSN: []string{containerstest.NewISSN}})
	require.Equal(t, "", got.ISSNL)
	require.Equal(t, []string{containerstest.NewISSN}, got.ISSNs)

	// Junk is dropped; unhyphenated ISSNs are normalized.
	got = resolveISSNs(sourceRecord{ISSN: []string{"bogus", "18813933"}})
	require.Equal(t, []string{"1881-3933"}, got.ISSNs)
	require.Equal(t, "0915-2245", got.ISSNL)
}

func Test_sourceSkipReason(t *testing.T) {
	ok := sourceRecord{DisplayName: "A Journal", Type: "journal", ISSN: []string{"0915-2245"}}
	cases := []struct {
		name string
		rec  sourceRecord
		want string
	}{
		{"ok", ok, ""},
		{"conference", sourceRecord{DisplayName: "C", Type: "conference", ISSN: []string{"0915-2245"}}, ""},
		{"repository", sourceRecord{DisplayName: "arXiv", Type: "repository", ISSN: []string{"0915-2245"}}, "unsupported-type"},
		{"no issns", sourceRecord{DisplayName: "A Journal", Type: "journal"}, "no-valid-issn"},
		{"too new", sourceRecord{DisplayName: "A Journal", Type: "journal", ISSN: []string{containerstest.NewISSN}}, "no-issnl"},
		{"no title", sourceRecord{DisplayName: "  ", Type: "journal", ISSN: []string{"0915-2245"}}, "empty-title"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.want, sourceSkipReason(c.rec, resolveISSNs(c.rec)))
		})
	}
}

func Test_sourceToContainer(t *testing.T) {
	rec := sourceRecord{
		ID:                   "https://openalex.org/S1",
		ISSN:                 []string{"0915-2245", "1881-3933"},
		DisplayName:          "  Nihon  Shouni Jinzoubyou Gakkai zasshi ",
		HostOrganizationName: "The Japanese Society for Pediatric Nephrology",
		HomepageURL:          "http://www.jspn.jp/",
		CountryCode:          "JP",
		Type:                 "book series",
	}
	c := sourceToContainer(rec, resolveISSNs(rec), "src")

	require.Equal(t, fatcat2.Container{
		Name:      "Nihon Shouni Jinzoubyou Gakkai zasshi",
		Type:      "book-series",
		Publisher: "The Japanese Society for Pediatric Nephrology",
		ISSNL:     "0915-2245",
		Source:    "src",
		Extra: map[string]any{
			"openalex": map[string]any{
				"id":    "S1",
				"issns": []string{"0915-2245", "1881-3933"},
			},
			"urls":    []string{"http://www.jspn.jp/"},
			"country": "jp",
		},
	}, c)
}

var twoISSNJournal = sourceRecord{
	ID:          "https://openalex.org/S1",
	ISSN:        []string{"0915-2245", "1881-3933"},
	DisplayName: "A Journal",
	Type:        "journal",
}

func Test_Ensure_SourceFindsExistingByAnyISSN(t *testing.T) {
	// A legacy container that only knows the journal's electronic ISSN, filed
	// under issne.
	f := containerstest.SetupFatcat(t, map[string]uuid.UUID{"issne=1881-3933": uuid.New()})

	outcome, c, err := containers.Ensure(http.DefaultClient, twoISSNJournal, "src")
	require.NoError(t, err)
	require.Equal(t, containers.Ignored, outcome)
	require.Nil(t, c)
	require.Empty(t, f.Created)
	require.Equal(t, []string{
		"issnl=0915-2245", "issnp=0915-2245", "issne=0915-2245",
		"issnl=1881-3933", "issnp=1881-3933", "issne=1881-3933",
	}, f.Lookups)
}

func Test_Ensure_SourceStopsAtFirstHit(t *testing.T) {
	f := containerstest.SetupFatcat(t, map[string]uuid.UUID{"issnl=0915-2245": uuid.New()})

	outcome, _, err := containers.Ensure(http.DefaultClient, twoISSNJournal, "src")
	require.NoError(t, err)
	require.Equal(t, containers.Ignored, outcome)
	require.Equal(t, []string{"issnl=0915-2245"}, f.Lookups)
}

func Test_Ensure_SourceCreatesNew(t *testing.T) {
	f := containerstest.SetupFatcat(t, map[string]uuid.UUID{})

	outcome, c, err := containers.Ensure(http.DefaultClient, twoISSNJournal, "src")
	require.NoError(t, err)
	require.Equal(t, containers.Added, outcome)
	require.NotNil(t, c)
	require.Len(t, f.Created, 1)
	require.Equal(t, "0915-2245", f.Created[0].ISSNL)
	require.Equal(t, "", f.Created[0].ISSNP)
	require.Equal(t, "", f.Created[0].ISSNE)
	require.Equal(t, "src", f.Created[0].Source)
	require.Equal(t, 1, f.Indexed)
}

func Test_Ensure_SourceSkips(t *testing.T) {
	f := containerstest.SetupFatcat(t, map[string]uuid.UUID{})

	outcome, _, err := containers.Ensure(http.DefaultClient,
		sourceRecord{DisplayName: "A Journal", Type: "journal", ISSN: []string{containerstest.NewISSN}}, "src")
	require.NoError(t, err)
	require.Equal(t, containers.Skipped, outcome)
	require.Empty(t, f.Lookups)
}

// --- fetching -------------------------------------------------------------

func gz(t *testing.T, s string) []byte {
	var b bytes.Buffer
	zw := gzip.NewWriter(&b)
	_, err := zw.Write([]byte(s))
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	return b.Bytes()
}

func Test_partsForDate(t *testing.T) {
	var m sourcesManifest
	require.NoError(t, json.Unmarshal([]byte(`{
		"date": "2026-10-14",
		"files": [
			{"url": "s3://openalex/data/jsonl/sources/updated_date=2026-10-13/part_0000.gz", "meta": {"record_count": 3}},
			{"url": "s3://openalex/data/jsonl/sources/updated_date=2026-10-14/part_0000.gz", "meta": {"record_count": 12}},
			{"url": "s3://openalex/data/jsonl/sources/updated_date=2026-10-14/part_0001.gz", "meta": {"record_count": 5}}
		]}`), &m))

	require.Equal(t, []manifestPart{
		{URL: "https://example.com/data/jsonl/sources/updated_date=2026-10-14/part_0000.gz", RecordCount: 12},
		{URL: "https://example.com/data/jsonl/sources/updated_date=2026-10-14/part_0001.gz", RecordCount: 5},
	}, partsForDate(m, "https://example.com/", "2026-10-14"))
	require.Empty(t, partsForDate(m, "https://example.com", "2026-10-15"))
}

// Concatenated gzip parts, as FetchSourcesActivity stores them, read back as
// one stream.
func Test_concatenatedPartsParse(t *testing.T) {
	day := append(gz(t, sampleSource+"\n"), gz(t, sampleSource+"\n"+sampleSource+"\n")...)
	zr, err := gzip.NewReader(bytes.NewReader(day))
	require.NoError(t, err)
	recs, err := parseSources(zr)
	require.NoError(t, err)
	require.Len(t, recs, 3)
}

func Test_countSources(t *testing.T) {
	n, err := countSources(gz(t, sampleSource+"\n"+sampleSource+"\n"))
	require.NoError(t, err)
	require.Equal(t, 2, n)

	_, err = countSources([]byte("not gzip"))
	require.Error(t, err)
}
