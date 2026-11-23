package etd

import (
	"bufio"
	"encoding/json"
	"os"
	"testing"
	"time"

	"git.archive.org/webgroup/scholar/trawler/fatcat2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sampleRecords returns the real lines in testdata/sample.ndjson, in order:
// 0. doi: key, handle landing page, one exact PDF
// 1. wikidata: key, DOI landing page, no title, one inexact PDF
// 2. doi: key, non-ID landing page, three PDFs (two inexact)
// 3. withdrawn
func sampleRecords(t *testing.T) ([]Record, [][]byte) {
	t.Helper()
	f, err := os.Open("testdata/sample.ndjson")
	require.NoError(t, err)
	defer f.Close()
	var recs []Record
	var raw [][]byte
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		var r Record
		require.NoError(t, json.Unmarshal(sc.Bytes(), &r))
		recs = append(recs, r)
		raw = append(raw, append([]byte(nil), sc.Bytes()...))
	}
	require.Len(t, recs, 4)
	return recs, raw
}

func TestRecord_ExtIDs(t *testing.T) {
	recs, _ := sampleRecords(t)
	assert.Equal(t, []fatcat2.ExternalID{
		{Type: "doi", Value: "10.11144/javeriana.10554.1457"},
		{Type: "hdl", Value: "10554/1457"},
	}, recs[0].ExtIDs())
	assert.Equal(t, []fatcat2.ExternalID{
		{Type: "wikidata_qid", Value: "Q100142558"},
		{Type: "doi", Value: "10.1007/bf01702717"},
	}, recs[1].ExtIDs())
	// A handle-system URL on the repository's own host is not hdl.handle.net
	// and so is not a handle we trust; the key still gives a DOI.
	assert.Equal(t, []fatcat2.ExternalID{{Type: "doi", Value: "10.17625/nke.2016.04"}}, recs[2].ExtIDs())
}

func TestLandingParsers(t *testing.T) {
	assert.Equal(t, "10.1/abc", landingDOI("http://dx.doi.org/10.1/ABC"))
	assert.Equal(t, "", landingDOI("https://example.org/10.1/abc"))
	assert.Equal(t, "20.500.12345/rul-9", landingHandle("http://hdl.handle.net/20.500.12345/RUL-9"))
	assert.Equal(t, "10366/121360", landingHandle("https://hdl.handle.net/10366/121360/"))
	assert.Equal(t, "", landingHandle("https://hdl.handle.net/justaprefix"))
	assert.Equal(t, "", landingHandle("::not a url"))
}

func TestRecord_ToRelease(t *testing.T) {
	recs, _ := sampleRecords(t)
	r := recs[0].ToRelease("etd-test")

	assert.Equal(t, "thesis", r.Type)
	assert.Equal(t, "published", r.Stage)
	assert.Equal(t, "etd-test", r.Source)
	assert.Equal(t, "es", r.Language)
	assert.Equal(t, 2023, r.ReleaseYear)
	require.NotNil(t, r.ReleaseDate)
	assert.Equal(t, "2023-05-30", r.ReleaseDate.Format(time.DateOnly))
	assert.Contains(t, r.Title, "Hacia un marco de fundamentación pedagógica")

	require.Len(t, r.Contribs, 1)
	c := r.Contribs[0]
	assert.Equal(t, "Arias Vargas, Viviana", c.RawName)
	assert.Equal(t, "Arias Vargas", c.Surname)
	assert.Equal(t, "Viviana", c.GivenName)
	assert.Equal(t, "author", c.Role)
	assert.Equal(t, new(0), c.Position)

	etd := r.Extra["etd"].(map[string]any)
	assert.Equal(t, recs[0].Key, etd["key"])
	assert.Equal(t, "http://hdl.handle.net/10554/1457", etd["landing"])
	if subj, ok := etd["subjects"]; ok {
		// JSON-normalized so it compares equal to what a GET returns.
		assert.IsType(t, []any{}, subj)
	}
}

func TestRecord_ReleaseDate(t *testing.T) {
	for in, want := range map[string]string{
		"2023-05-30":           "2023-05-30",
		"2023-05-30T12:00:00Z": "2023-05-30",
		"2023":                 "",
		"2023-05":              "",
		"30/05/2023":           "",
		"2023-13-45":           "",
	} {
		d := Record{Date: in}.ReleaseDate()
		if want == "" {
			assert.Nil(t, d, in)
		} else if assert.NotNil(t, d, in) {
			assert.Equal(t, want, d.Format(time.DateOnly), in)
		}
	}
}

func TestContrib(t *testing.T) {
	c := contrib("Leopold Vietoris", 3)
	assert.Equal(t, "Leopold Vietoris", c.RawName)
	assert.Empty(t, c.Surname)
	assert.Equal(t, new(3), c.Position)

	c = contrib("Silva, Thainá, Jr.", 0)
	assert.Empty(t, c.Surname, "two commas is not the Surname, Given form")
}

func TestRankedFulltext(t *testing.T) {
	r := Record{Fulltext: []Fulltext{
		{Part: "chapter", Exact: true, FetchURL: "c"},
		{Part: "whole", Exact: false, FetchURL: "w-fuzzy"},
		{Part: "whole", Exact: true, FetchURL: "w-exact"},
		{Part: "whole-verified", Exact: false, FetchURL: "wv"},
		{Part: "whole", Exact: true, FetchURL: ""},
	}}
	var got []string
	for _, f := range r.rankedFulltext() {
		got = append(got, f.FetchURL)
	}
	assert.Equal(t, []string{"wv", "w-exact", "w-fuzzy", "c"}, got)
}

func TestPlainReplayURL(t *testing.T) {
	assert.Equal(t, "https://web.archive.org/web/20231117062634/http://x.org/a.pdf?b=c",
		plainReplayURL("https://web.archive.org/web/20231117062634id_/http://x.org/a.pdf?b=c"))
	assert.Equal(t, "https://web.archive.org/web/2/http://x.org/a.pdf",
		plainReplayURL("https://web.archive.org/web/2/http://x.org/a.pdf"))
}

func TestFillBlanks(t *testing.T) {
	recs, _ := sampleRecords(t)
	incoming := recs[0].ToRelease("etd-test")

	existing := fatcat2.Release{
		Title:       "The Title Fatcat Has",
		Type:        "article-journal",
		ReleaseYear: 2020,
		ExternalIDs: []fatcat2.ExternalID{{Type: "doi", Value: "10.1/other"}},
		Contribs:    []fatcat2.ReleaseContrib{{RawName: "Someone Else"}},
		Extra:       map[string]any{"crossref": map[string]any{"x": 1.0}},
	}
	assert.Equal(t, []string{"release_stage", "language", "publisher", "release_date", "extid:hdl", "extra.etd"},
		fillBlanks(&existing, incoming))

	assert.Equal(t, "The Title Fatcat Has", existing.Title, "title is never changed")
	assert.Equal(t, "article-journal", existing.Type, "a set type is kept")
	assert.Equal(t, 2020, existing.ReleaseYear, "a set year is kept")
	assert.Equal(t, "published", existing.Stage, "a blank stage is filled")
	assert.Equal(t, "es", existing.Language)
	assert.NotNil(t, existing.ReleaseDate)
	assert.Equal(t, []fatcat2.ReleaseContrib{{RawName: "Someone Else"}}, existing.Contribs, "contribs are all-or-nothing")
	assert.Equal(t, []fatcat2.ExternalID{
		{Type: "doi", Value: "10.1/other"},
		{Type: "hdl", Value: "10554/1457"},
	}, existing.ExternalIDs, "a second DOI is a conflict, not a blank; the missing hdl is added")
	assert.Equal(t, map[string]any{"x": 1.0}, existing.Extra["crossref"], "other extra keys survive")
	assert.Equal(t, incoming.Extra["etd"], existing.Extra["etd"])

	assert.Empty(t, fillBlanks(&existing, incoming), "a second pass finds nothing to do")

	empty := fatcat2.Release{Title: "t"}
	assert.NotEmpty(t, fillBlanks(&empty, incoming))
	assert.Equal(t, incoming.Contribs, empty.Contribs)
}

// After a GET, extra.etd has lists as []any. fillBlanks must still see it as
// unchanged, or every rerun would PUT every matched release.
func TestFillBlanks_NoChangeAfterJSONRoundTrip(t *testing.T) {
	recs, _ := sampleRecords(t)
	incoming := recs[2].ToRelease("etd-test")
	stored := incoming
	fillBlanks(&stored, incoming)

	bs, err := json.Marshal(&stored)
	require.NoError(t, err)
	var fetched fatcat2.Release
	require.NoError(t, json.Unmarshal(bs, &fetched))
	assert.Empty(t, fillBlanks(&fetched, incoming))
}

// Records with no DOI, handle or QID still carry their OAI identifier, in
// extra.etd.key (and any OAI aliases), so they stay traceable to the source.
func TestRecord_OAIIdentifierKeptWithoutExtIDs(t *testing.T) {
	recs, _ := sampleRecords(t)
	rec := recs[3]
	rec.Landing = "" // drop the handle so nothing is an fc2 external ID
	r := rec.ToRelease("etd-test")
	assert.Empty(t, r.ExternalIDs)
	assert.Equal(t, "oai:archive.org:deustoteka.deusto.es:oai:deustoteka.deusto.es:20.500.14454/457",
		r.Extra["etd"].(map[string]any)["key"])
}

// A language that cannot be normalized is left blank: not sent to fc2, and
// never filled into an existing release.
func TestRecord_UnknownLanguageLeftBlank(t *testing.T) {
	rec := Record{Title: "t", Languages: []string{"other", "und", "dc.language.iso"}}
	r := rec.ToRelease("etd-test")
	assert.Empty(t, r.Language)

	bs, err := json.Marshal(&r)
	require.NoError(t, err)
	var body map[string]any
	require.NoError(t, json.Unmarshal(bs, &body))
	assert.NotContains(t, body, "language")

	existing := fatcat2.Release{Title: "t"}
	assert.NotContains(t, fillBlanks(&existing, r), "language")
	assert.Empty(t, existing.Language)
}
