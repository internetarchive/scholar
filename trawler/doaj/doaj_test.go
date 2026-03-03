package doaj

import (
	"strings"
	"testing"
	"time"
)

func Test_doajID(t *testing.T) {
	cases := []struct {
		name       string
		rec        doajRecord
		expected   string
	}{
		{
			name:     "ID field takes priority",
			rec:      doajRecord{ID: "abc123", Identifier: "oai:doaj.org/article:xyz"},
			expected: "abc123",
		},
		{
			name:     "falls back to stripping OAI prefix",
			rec:      doajRecord{Identifier: "oai:doaj.org/article:abc123def456"},
			expected: "abc123def456",
		},
		{
			name:     "identifier without expected prefix is returned as-is",
			rec:      doajRecord{Identifier: "something-else"},
			expected: "something-else",
		},
		{
			name:     "empty record yields empty string",
			rec:      doajRecord{},
			expected: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := doajID(&c.rec)
			if got != c.expected {
				t.Errorf("expected %q, got %q", c.expected, got)
			}
		})
	}
}

func Test_skipReason(t *testing.T) {
	manyAuthors := make([]doajAuthor, maxAuthors+1)

	cases := []struct {
		name     string
		rec      doajRecord
		expected string
	}{
		{
			name:     "valid record passes",
			rec:      doajRecord{ID: "abc123", Title: "A Title", Authors: []doajAuthor{{Name: "Smith, Jane"}}},
			expected: "",
		},
		{
			name:     "no ID",
			rec:      doajRecord{Title: "A Title"},
			expected: "no-doaj-id",
		},
		{
			name:     "deleted status",
			rec:      doajRecord{ID: "abc123", Status: "deleted", Title: "A Title"},
			expected: "deleted",
		},
		{
			name:     "empty title",
			rec:      doajRecord{ID: "abc123", Title: ""},
			expected: "empty-title",
		},
		{
			name:     "whitespace-only title",
			rec:      doajRecord{ID: "abc123", Title: "   "},
			expected: "empty-title",
		},
		{
			name:     "too many authors",
			rec:      doajRecord{ID: "abc123", Title: "A Title", Authors: manyAuthors},
			expected: "too-many-authors",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := skipReason(&c.rec)
			if got != c.expected {
				t.Errorf("expected %q, got %q", c.expected, got)
			}
		})
	}
}

func Test_licenseSlug(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{"https://creativecommons.org/licenses/by/4.0/", "cc-by"},
		{"http://creativecommons.org/licenses/by/4.0/", "cc-by"},
		{"https://creativecommons.org/licenses/by-nc/4.0/", "cc-by-nc"},
		{"https://creativecommons.org/licenses/by-nc-nd/4.0/", "cc-by-nc-nd"},
		{"https://creativecommons.org/licenses/by-sa/4.0/", "cc-by-sa"},
		{"https://creativecommons.org/licenses/by/4.0", "cc-by"},
		{"HTTPS://CREATIVECOMMONS.ORG/LICENSES/BY/4.0/", "cc-by"},
		{"https://example.com/some-license", ""},
		{"", ""},
	}
	for _, c := range cases {
		t.Run(c.input, func(t *testing.T) {
			got := licenseSlug(c.input)
			if got != c.expected {
				t.Errorf("input %q: expected %q, got %q", c.input, c.expected, got)
			}
		})
	}
}

func Test_releaseDate(t *testing.T) {
	mustTime := func(s string) time.Time {
		t, err := time.Parse("2006-01-02", s)
		if err != nil {
			panic(err)
		}
		return t
	}

	cases := []struct {
		input    string
		wantNil  bool
		expected time.Time
	}{
		{"2023-06-15", false, mustTime("2023-06-15")},
		{"2023", false, mustTime("2023-01-01")},
		{"", true, time.Time{}},
		{"not-a-date", true, time.Time{}},
		{"2023-13-01", true, time.Time{}},
	}
	for _, c := range cases {
		t.Run(c.input, func(t *testing.T) {
			got := releaseDate(c.input)
			if c.wantNil {
				if got != nil {
					t.Errorf("expected nil, got %v", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("expected non-nil ReleaseDate for %q", c.input)
			}
			if time.Time(*got) != c.expected {
				t.Errorf("expected %v, got %v", c.expected, time.Time(*got))
			}
		})
	}
}

func Test_releaseYear(t *testing.T) {
	cases := []struct {
		input    string
		expected int
	}{
		{"2023-06-15", 2023},
		{"2023", 2023},
		{"199", 0},
		{"", 0},
		{"abcd", 0},
	}
	for _, c := range cases {
		t.Run(c.input, func(t *testing.T) {
			got := releaseYear(c.input)
			if got != c.expected {
				t.Errorf("input %q: expected %d, got %d", c.input, c.expected, got)
			}
		})
	}
}

func Test_pages(t *testing.T) {
	cases := []struct {
		start, end, expected string
	}{
		{"1", "10", "1-10"},
		{"1", "", "1"},
		{"", "10", ""},
		{"", "", ""},
	}
	for _, c := range cases {
		t.Run(c.start+"/"+c.end, func(t *testing.T) {
			got := pages(c.start, c.end)
			if got != c.expected {
				t.Errorf("pages(%q, %q): expected %q, got %q", c.start, c.end, c.expected, got)
			}
		})
	}
}

func Test_contribs(t *testing.T) {
	t.Run("empty input", func(t *testing.T) {
		got := contribs(nil)
		if len(got) != 0 {
			t.Errorf("expected empty, got %v", got)
		}
	})

	t.Run("skips authors with no name", func(t *testing.T) {
		got := contribs([]doajAuthor{{Name: ""}, {Name: "Smith, Jane"}})
		if len(got) != 1 {
			t.Fatalf("expected 1 contrib, got %d", len(got))
		}
		if got[0].RawName != "Smith, Jane" {
			t.Errorf("unexpected RawName: %q", got[0].RawName)
		}
	})

	t.Run("comma-separated name is split", func(t *testing.T) {
		got := contribs([]doajAuthor{{Name: "Smith, Jane"}})
		if len(got) != 1 {
			t.Fatalf("expected 1 contrib, got %d", len(got))
		}
		c := got[0]
		if c.Surname != "Smith" {
			t.Errorf("expected Surname %q, got %q", "Smith", c.Surname)
		}
		if c.GivenName != "Jane" {
			t.Errorf("expected GivenName %q, got %q", "Jane", c.GivenName)
		}
	})

	t.Run("name without comma sets only RawName", func(t *testing.T) {
		got := contribs([]doajAuthor{{Name: "Jane Smith"}})
		if len(got) != 1 {
			t.Fatalf("expected 1 contrib, got %d", len(got))
		}
		c := got[0]
		if c.RawName != "Jane Smith" {
			t.Errorf("unexpected RawName: %q", c.RawName)
		}
		if c.Surname != "" || c.GivenName != "" {
			t.Errorf("expected empty Surname/GivenName, got %q / %q", c.Surname, c.GivenName)
		}
	})

	t.Run("affiliation is passed through", func(t *testing.T) {
		got := contribs([]doajAuthor{{Name: "Smith, Jane", Affiliation: "MIT"}})
		if got[0].RawAffiliation != "MIT" {
			t.Errorf("expected RawAffiliation %q, got %q", "MIT", got[0].RawAffiliation)
		}
	})

	t.Run("position reflects original index", func(t *testing.T) {
		got := contribs([]doajAuthor{
			{Name: ""},
			{Name: "Smith, Jane"},
			{Name: "Doe, John"},
		})
		if len(got) != 2 {
			t.Fatalf("expected 2 contribs, got %d", len(got))
		}
		if got[0].Position != 1 || got[1].Position != 2 {
			t.Errorf("unexpected positions: %d, %d", got[0].Position, got[1].Position)
		}
	})

	t.Run("role is always author", func(t *testing.T) {
		got := contribs([]doajAuthor{{Name: "Smith, Jane"}})
		if got[0].Role != "author" {
			t.Errorf("expected role %q, got %q", "author", got[0].Role)
		}
	})
}

func Test_doajToFc(t *testing.T) {
	rec := doajRecord{
		ID:              "abc123def456abc123def456abc12345",
		Title:           "A Test Article",
		DOI:             "https://doi.org/10.1234/test",
		Language:        "ENG",
		PublicationDate: "2023-06-15",
		Volume:          "10",
		Issue:           "2",
		StartPage:       "100",
		EndPage:         "110",
		Publisher:       "Test Publisher",
		Abstract:        strings.Repeat("x", minAbstractLength),
		Authors: []doajAuthor{
			{Name: "Smith, Jane", Affiliation: "MIT"},
			{Name: "Doe, John"},
		},
		Keywords:    []string{"foo", "bar"},
		FullTextURL: "https://example.com/paper.pdf",
		LicenseRef:  "https://creativecommons.org/licenses/by/4.0/",
	}

	r := doajToFc(&rec, "test-source")

	if r.Type != "article-journal" {
		t.Errorf("Type: expected %q, got %q", "article-journal", r.Type)
	}
	if r.Stage != "published" {
		t.Errorf("Stage: expected %q, got %q", "published", r.Stage)
	}
	if r.Title != "A Test Article" {
		t.Errorf("Title: expected %q, got %q", "A Test Article", r.Title)
	}
	if r.Language != "eng" {
		t.Errorf("Language: expected %q (lowercased), got %q", "eng", r.Language)
	}
	if r.ReleaseYear != 2023 {
		t.Errorf("ReleaseYear: expected 2023, got %d", r.ReleaseYear)
	}
	if r.Volume != "10" || r.Issue != "2" {
		t.Errorf("Volume/Issue: expected 10/2, got %s/%s", r.Volume, r.Issue)
	}
	if r.Pages != "100-110" {
		t.Errorf("Pages: expected %q, got %q", "100-110", r.Pages)
	}
	if r.Publisher != "Test Publisher" {
		t.Errorf("Publisher: expected %q, got %q", "Test Publisher", r.Publisher)
	}
	if r.LicenseSlug != "cc-by" {
		t.Errorf("LicenseSlug: expected %q, got %q", "cc-by", r.LicenseSlug)
	}
	if r.Source != "test-source" {
		t.Errorf("Source: expected %q, got %q", "test-source", r.Source)
	}

	// external IDs
	if r.DoajID() != rec.ID {
		t.Errorf("DoajID: expected %q, got %q", rec.ID, r.DoajID())
	}
	if r.DOI() != "10.1234/test" {
		t.Errorf("DOI: expected %q, got %q", "10.1234/test", r.DOI())
	}

	// abstract
	if len(r.Abstracts) != 1 {
		t.Fatalf("expected 1 abstract, got %d", len(r.Abstracts))
	}
	if r.Abstracts[0].MIMEType != "text/plain" {
		t.Errorf("abstract MIMEType: expected %q, got %q", "text/plain", r.Abstracts[0].MIMEType)
	}

	// contribs
	if len(r.Contribs) != 2 {
		t.Fatalf("expected 2 contribs, got %d", len(r.Contribs))
	}
	if r.Contribs[0].Surname != "Smith" {
		t.Errorf("contrib[0] Surname: expected %q, got %q", "Smith", r.Contribs[0].Surname)
	}

	// extra
	doajExtra, ok := r.Extra["doaj"].(map[string]any)
	if !ok {
		t.Fatalf("expected extra[\"doaj\"] to be a map")
	}
	kws, ok := doajExtra["keywords"].([]string)
	if !ok || len(kws) != 2 {
		t.Errorf("expected 2 keywords in extra, got %v", doajExtra["keywords"])
	}
	if doajExtra["full_text_url"] != "https://example.com/paper.pdf" {
		t.Errorf("extra full_text_url: expected %q, got %v", "https://example.com/paper.pdf", doajExtra["full_text_url"])
	}

	// no extra set when nothing to put there
	t.Run("no extra when no keywords or full_text_url", func(t *testing.T) {
		bare := doajRecord{ID: "abc", Title: "T"}
		r2 := doajToFc(&bare, "src")
		if _, ok := r2.Extra["doaj"]; ok {
			t.Error("expected no extra[\"doaj\"] key for bare record")
		}
	})
}

func Test_doajToFc_noExternalDOI(t *testing.T) {
	rec := doajRecord{ID: "abc123", Title: "No DOI Article"}
	r := doajToFc(&rec, "src")

	var hasDOI bool
	for _, eid := range r.ExternalIDs {
		if eid.Type == "doi" {
			hasDOI = true
		}
	}
	if hasDOI {
		t.Error("expected no DOI external ID when record has no DOI")
	}

	extids := r.ExternalIDs
	if len(extids) != 1 || extids[0].Type != "doaj" {
		t.Errorf("expected exactly one extid (doaj), got %v", extids)
	}
}

func Test_doajToFc_shortAbstractOmitted(t *testing.T) {
	rec := doajRecord{
		ID:       "abc123",
		Title:    "Short Abstract Article",
		Abstract: "Too short.",
	}
	r := doajToFc(&rec, "src")
	if len(r.Abstracts) != 0 {
		t.Errorf("expected no abstracts for short text, got %d", len(r.Abstracts))
	}
}

func Test_doajToFc_yearOnlyDate(t *testing.T) {
	rec := doajRecord{ID: "abc123", Title: "Year Only", PublicationDate: "2021"}
	r := doajToFc(&rec, "src")
	if r.ReleaseYear != 2021 {
		t.Errorf("ReleaseYear: expected 2021, got %d", r.ReleaseYear)
	}
	if r.ReleaseDate == nil {
		t.Error("expected non-nil ReleaseDate for year-only date")
	}
}

func Test_doajToFc_identifierFallback(t *testing.T) {
	rec := doajRecord{
		Identifier: "oai:doaj.org/article:fallbackid123",
		Title:      "Fallback ID Article",
	}
	r := doajToFc(&rec, "src")
	if r.DoajID() != "fallbackid123" {
		t.Errorf("expected DoajID %q, got %q", "fallbackid123", r.DoajID())
	}
}

func Test_doajToFc_extraOnlySetWhenPopulated(t *testing.T) {
	cases := []struct {
		name        string
		rec         doajRecord
		expectExtra bool
	}{
		{"keywords only", doajRecord{ID: "x", Title: "T", Keywords: []string{"kw"}}, true},
		{"full_text_url only", doajRecord{ID: "x", Title: "T", FullTextURL: "https://example.com"}, true},
		{"neither", doajRecord{ID: "x", Title: "T"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := doajToFc(&c.rec, "src")
			_, has := r.Extra["doaj"]
			if has != c.expectExtra {
				t.Errorf("expectExtra=%v but got has=%v", c.expectExtra, has)
			}
		})
	}
}

func Test_doajToFc_typeAndStageFixed(t *testing.T) {
	r := doajToFc(&doajRecord{ID: "x", Title: "T"}, "src")
	if r.Type != "article-journal" {
		t.Errorf("Type: expected article-journal, got %q", r.Type)
	}
	if r.Stage != "published" {
		t.Errorf("Stage: expected published, got %q", r.Stage)
	}
}

// Verify ExternalID helper round-trips through fatcat2.Release correctly.
func Test_doajToFc_externalIDTypes(t *testing.T) {
	rec := doajRecord{
		ID:    "abc123",
		Title: "ID Types Test",
		DOI:   "10.9999/xyz",
	}
	r := doajToFc(&rec, "src")

	seen := map[string]string{}
	for _, eid := range r.ExternalIDs {
		seen[eid.Type] = eid.Value
	}
	if seen["doaj"] != "abc123" {
		t.Errorf("doaj extid: expected %q, got %q", "abc123", seen["doaj"])
	}
	if seen["doi"] != "10.9999/xyz" {
		t.Errorf("doi extid: expected %q, got %q", "10.9999/xyz", seen["doi"])
	}

	// fatcat2 helpers should agree
	if r.DoajID() != "abc123" {
		t.Errorf("DoajID(): expected %q, got %q", "abc123", r.DoajID())
	}
	if r.DOI() != "10.9999/xyz" {
		t.Errorf("DOI(): expected %q, got %q", "10.9999/xyz", r.DOI())
	}
}
