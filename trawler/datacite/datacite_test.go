package datacite

import (
	"encoding/json"
	"testing"
	"time"

	"git.archive.org/webgroup/scholar/trawler/fatcat2"
	"github.com/google/uuid"
)

func newUUID() uuid.UUID {
	uid, err := uuid.NewV7()
	if err != nil {
		panic(err)
	}

	return uid
}

// formatDate renders a *fatcat2.ReleaseDate as "YYYY-MM-DD" for comparison,
// or "" if nil.
func formatDate(rd *fatcat2.ReleaseDate) string {
	if rd == nil {
		return ""
	}
	return time.Time(*rd).Format("2006-01-02")
}

// releaseWithDOI builds a minimal Release with the given DOI as an ExternalID.
func releaseWithDOI(doi string) *fatcat2.Release {
	return &fatcat2.Release{
		ExternalIDs: []fatcat2.ExternalID{{Type: "doi", Value: doi}},
		Extra:       map[string]any{},
	}
}

// docWithRelations builds a dataciteDoc whose RelatedIdentifiers contains one
// entry with the given relationType.
func docWithRelations(relationType string) *dataciteDoc {
	return &dataciteDoc{
		Attributes: struct {
			DOI                string                      `json:"doi"`
			Titles             []dataciteTitle             `json:"titles"`
			Types              dataciteTypes               `json:"types"`
			Creators           []dataciteCreator           `json:"creators"`
			Contributors       []dataciteCreator           `json:"contributors"`
			Descriptions       []dataciteDescription       `json:"descriptions"`
			Dates              []dataciteDate              `json:"dates"`
			RelatedIdentifiers []dataciteRelatedIdentifier `json:"relatedIdentifiers"`
			RightsList         []dataciteRights            `json:"rightsList"`
			Subjects           []dataciteSubject           `json:"subjects"`
			Container          dataciteContainer           `json:"container"`
			PublicationYear    int                         `json:"publicationYear"`
			Published          string                      `json:"published"`
			Publisher          string                      `json:"publisher"`
			Language           string                      `json:"language"`
			Version            string                      `json:"version"`
			MetadataVersion    int                         `json:"metadataVersion"`
			State              string                      `json:"state"`
		}{
			RelatedIdentifiers: []dataciteRelatedIdentifier{
				{RelationType: relationType},
			},
		},
	}
}

var emptyDoc = &dataciteDoc{}

// --- parseDates ---

func TestParseDates(t *testing.T) {
	cases := []struct {
		name        string
		dates       []dataciteDate
		wantYear    int
		wantMonth   int
		wantDateStr string // "YYYY-MM-DD" or ""
	}{
		{
			name:  "empty",
			dates: nil,
		},
		{
			name:      "year only",
			dates:     []dataciteDate{{Date: "2021", DateType: "Issued"}},
			wantYear:  2021,
			wantMonth: 0,
		},
		{
			name:      "year-month",
			dates:     []dataciteDate{{Date: "2021-06", DateType: "Issued"}},
			wantYear:  2021,
			wantMonth: 6,
		},
		{
			name:        "full date",
			dates:       []dataciteDate{{Date: "2021-06-15", DateType: "Issued"}},
			wantYear:    2021,
			wantMonth:   6,
			wantDateStr: "2021-06-15",
		},
		{
			name:        "ISO 8601 datetime",
			dates:       []dataciteDate{{Date: "2021-06-15T00:00:00Z", DateType: "Issued"}},
			wantYear:    2021,
			wantMonth:   6,
			wantDateStr: "2021-06-15",
		},
		{
			name: "priority: Valid beats Updated",
			dates: []dataciteDate{
				{Date: "2019-01-01", DateType: "Updated"},
				{Date: "2021-03-10", DateType: "Valid"},
			},
			wantYear:    2021,
			wantMonth:   3,
			wantDateStr: "2021-03-10",
		},
		{
			name: "priority: Available beats Submitted",
			dates: []dataciteDate{
				{Date: "2018-05-01", DateType: "Submitted"},
				{Date: "2020-11-20", DateType: "Available"},
			},
			wantYear:    2020,
			wantMonth:   11,
			wantDateStr: "2020-11-20",
		},
		{
			name: "fallback to unlisted type when nothing in priority list matches",
			dates: []dataciteDate{
				{Date: "2022-07-04", DateType: "Collected"},
			},
			wantYear:    2022,
			wantMonth:   7,
			wantDateStr: "2022-07-04",
		},
		{
			name: "bogus far-future date skipped, lower priority used",
			dates: []dataciteDate{
				{Date: "2999-01-01", DateType: "Valid"},
				{Date: "2020-04-01", DateType: "Accepted"},
			},
			wantYear:    2020,
			wantMonth:   4,
			wantDateStr: "2020-04-01",
		},
		{
			name:  "ancient date skipped",
			dates: []dataciteDate{{Date: "0999-01-01", DateType: "Valid"}},
		},
		{
			name:  "unparseable date",
			dates: []dataciteDate{{Date: "not-a-date", DateType: "Issued"}},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseDates(c.dates)
			if got.releaseYear != c.wantYear {
				t.Errorf("releaseYear: got %d, want %d", got.releaseYear, c.wantYear)
			}
			if got.releaseMonth != c.wantMonth {
				t.Errorf("releaseMonth: got %d, want %d", got.releaseMonth, c.wantMonth)
			}
			if gotStr := formatDate(got.releaseDate); gotStr != c.wantDateStr {
				t.Errorf("releaseDate: got %q, want %q", gotStr, c.wantDateStr)
			}
		})
	}
}

// --- parseTitles ---

func TestParseTitles(t *testing.T) {
	cases := []struct {
		name         string
		titles       []dataciteTitle
		wantTitle    string
		wantOriginal string
		wantSubtitle string
	}{
		{
			name: "empty",
		},
		{
			name:      "single title",
			titles:    []dataciteTitle{{Title: "A Study"}},
			wantTitle: "A Study",
		},
		{
			name:      "single title with whitespace",
			titles:    []dataciteTitle{{Title: "  A Study  "}},
			wantTitle: "A Study",
		},
		{
			name: "main and subtitle",
			titles: []dataciteTitle{
				{Title: "Main Title"},
				{Title: "The Subtitle", TitleType: "Subtitle"},
			},
			wantTitle:    "Main Title",
			wantSubtitle: "The Subtitle",
		},
		{
			name: "translated title distinct from main",
			titles: []dataciteTitle{
				{Title: "English Title"},
				{Title: "Título en español", TitleType: "TranslatedTitle"},
			},
			wantTitle:    "English Title",
			wantOriginal: "Título en español",
		},
		{
			name: "translated title same as main is discarded",
			titles: []dataciteTitle{
				{Title: "Same Title"},
				{Title: "Same Title", TitleType: "TranslatedTitle"},
			},
			wantTitle: "Same Title",
		},
		{
			name: "translated title too short is discarded",
			titles: []dataciteTitle{
				{Title: "Main Title"},
				{Title: "Ab", TitleType: "TranslatedTitle"},
			},
			wantTitle: "Main Title",
		},
		{
			name: "translated title with too many question marks is discarded",
			titles: []dataciteTitle{
				{Title: "Main Title"},
				{Title: "???? unknown ????", TitleType: "TranslatedTitle"},
			},
			wantTitle: "Main Title",
		},
		{
			name: "fallback to first title when none has empty titleType",
			titles: []dataciteTitle{
				{Title: "Only Subtitle", TitleType: "Subtitle"},
			},
			wantTitle: "Only Subtitle",
			// subtitle not set: single-title early-return path doesn't check TitleType
		},
		{
			name: "all three fields set",
			titles: []dataciteTitle{
				{Title: "Main Title"},
				{Title: "A Subtitle", TitleType: "Subtitle"},
				{Title: "Titre principal", TitleType: "TranslatedTitle"},
			},
			wantTitle:    "Main Title",
			wantSubtitle: "A Subtitle",
			wantOriginal: "Titre principal",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			title, orig, sub := parseTitles(c.titles)
			if title != c.wantTitle {
				t.Errorf("title: got %q, want %q", title, c.wantTitle)
			}
			if orig != c.wantOriginal {
				t.Errorf("originalTitle: got %q, want %q", orig, c.wantOriginal)
			}
			if sub != c.wantSubtitle {
				t.Errorf("subtitle: got %q, want %q", sub, c.wantSubtitle)
			}
		})
	}
}

// --- resolveReleaseType ---

func TestResolveReleaseType(t *testing.T) {
	cases := []struct {
		name  string
		doi   string
		types dataciteTypes
		want  string
	}{
		{
			name: "all empty",
			want: "",
		},
		{
			name:  "citeproc article-journal",
			types: dataciteTypes{Citeproc: "article-journal"},
			want:  "article-journal",
		},
		{
			name:  "citeproc takes priority over ris",
			types: dataciteTypes{Citeproc: "book", Ris: "DATA"},
			want:  "book",
		},
		{
			name:  "ris when no citeproc",
			types: dataciteTypes{Ris: "THES"},
			want:  "thesis",
		},
		{
			name:  "schemaOrg when no citeproc or ris",
			types: dataciteTypes{SchemaOrg: "Dataset"},
			want:  "dataset",
		},
		{
			name:  "bibtex when no higher-priority schema",
			types: dataciteTypes{Bibtex: "phdthesis"},
			want:  "thesis",
		},
		{
			name:  "resourceTypeGeneral as last resort",
			types: dataciteTypes{ResourceTypeGeneral: "Image"},
			want:  "graphic",
		},
		{
			name:  "schemaOrg Collection not in map, falls through to resourceTypeGeneral",
			types: dataciteTypes{SchemaOrg: "Collection", ResourceTypeGeneral: "Image"},
			want:  "graphic",
		},
		{
			name:  "unknown value returns empty",
			types: dataciteTypes{Citeproc: "not-a-real-type"},
			want:  "",
		},
		{
			name:  "figshare Collection stub",
			doi:   "10.6084/m9.figshare.12345",
			types: dataciteTypes{ResourceType: "Collection"},
			want:  "stub",
		},
		{
			name:  "figshare 10.25384 prefix also triggers stub",
			doi:   "10.25384/sage.12345",
			types: dataciteTypes{ResourceType: "Collection"},
			want:  "stub",
		},
		{
			name:  "non-figshare Collection is not stub",
			doi:   "10.5281/zenodo.12345",
			types: dataciteTypes{ResourceType: "Collection"},
			want:  "",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := resolveReleaseType(c.doi, c.types)
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// --- skipReason ---

func TestSkipReason(t *testing.T) {
	makeDoc := func(doi, title string) *dataciteDoc {
		return &dataciteDoc{
			Attributes: struct {
				DOI                string                      `json:"doi"`
				Titles             []dataciteTitle             `json:"titles"`
				Types              dataciteTypes               `json:"types"`
				Creators           []dataciteCreator           `json:"creators"`
				Contributors       []dataciteCreator           `json:"contributors"`
				Descriptions       []dataciteDescription       `json:"descriptions"`
				Dates              []dataciteDate              `json:"dates"`
				RelatedIdentifiers []dataciteRelatedIdentifier `json:"relatedIdentifiers"`
				RightsList         []dataciteRights            `json:"rightsList"`
				Subjects           []dataciteSubject           `json:"subjects"`
				Container          dataciteContainer           `json:"container"`
				PublicationYear    int                         `json:"publicationYear"`
				Published          string                      `json:"published"`
				Publisher          string                      `json:"publisher"`
				Language           string                      `json:"language"`
				Version            string                      `json:"version"`
				MetadataVersion    int                         `json:"metadataVersion"`
				State              string                      `json:"state"`
			}{
				DOI:    doi,
				Titles: []dataciteTitle{{Title: title}},
			},
		}
	}

	cases := []struct {
		name string
		doc  *dataciteDoc
		want string
	}{
		{
			name: "valid record",
			doc:  makeDoc("10.1234/test", "A Fine Paper"),
			want: "",
		},
		{
			name: "no doi",
			doc:  makeDoc("", "A Fine Paper"),
			want: "no-doi",
		},
		{
			name: "non-ascii doi",
			doc:  makeDoc("10.1234/tëst", "A Fine Paper"),
			want: "non-ascii-doi",
		},
		{
			name: "empty title",
			doc:  makeDoc("10.1234/test", ""),
			want: "no-title",
		},
		{
			name: "whitespace-only title",
			doc:  makeDoc("10.1234/test", "   "),
			want: "no-title",
		},
		{
			name: "spam title with 4+ tokens",
			doc:  makeDoc("10.1234/test", "Watch Full Movie Online Free HD"),
			want: "spam-title",
		},
		{
			name: "title with only 3 spam tokens is not spam",
			doc:  makeDoc("10.1234/test", "Watch Full Movie"),
			want: "",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := skipReason(c.doc)
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// --- biblioHacks ---

func TestBiblioHacks(t *testing.T) {
	t.Run("GBIF occurrence download becomes stub", func(t *testing.T) {
		r := releaseWithDOI("10.15468/dl.abc123")
		r.Title = "GBIF Occurrence Download"
		r.Type = "dataset"
		biblioHacks(r, emptyDoc)
		if r.Type != "stub" {
			t.Errorf("type: got %q, want %q", r.Type, "stub")
		}
	})

	t.Run("GBIF with different title is unchanged", func(t *testing.T) {
		r := releaseWithDOI("10.15468/dl.abc123")
		r.Title = "Some Other Dataset"
		r.Type = "dataset"
		biblioHacks(r, emptyDoc)
		if r.Type != "dataset" {
			t.Errorf("type: got %q, want %q", r.Type, "dataset")
		}
	})

	t.Run("Cambridge Crystallographic becomes entry", func(t *testing.T) {
		r := releaseWithDOI("10.5517/ccxyz")
		r.Type = "dataset"
		biblioHacks(r, emptyDoc)
		if r.Type != "entry" {
			t.Errorf("type: got %q, want %q", r.Type, "entry")
		}
	})

	t.Run("supplement file becomes component", func(t *testing.T) {
		r := releaseWithDOI("10.1234/test")
		r.Title = "Additional file 1: Raw data"
		r.Type = "article-journal"
		biblioHacks(r, emptyDoc)
		if r.Type != "component" {
			t.Errorf("type: got %q, want %q", r.Type, "component")
		}
	})

	t.Run("supplement file with non-article type is unchanged", func(t *testing.T) {
		r := releaseWithDOI("10.1234/test")
		r.Title = "Additional file 1: Raw data"
		r.Type = "dataset"
		biblioHacks(r, emptyDoc)
		if r.Type != "dataset" {
			t.Errorf("type: got %q, want %q", r.Type, "dataset")
		}
	})

	t.Run("figshare version extracted from DOI suffix", func(t *testing.T) {
		r := releaseWithDOI("10.6084/m9.figshare.12345.v3")
		biblioHacks(r, emptyDoc)
		if r.Version != "v3" {
			t.Errorf("version: got %q, want %q", r.Version, "v3")
		}
	})

	t.Run("figshare DOI without version suffix leaves version empty", func(t *testing.T) {
		r := releaseWithDOI("10.6084/m9.figshare.12345")
		biblioHacks(r, emptyDoc)
		if r.Version != "" {
			t.Errorf("version: got %q, want empty", r.Version)
		}
	})

	t.Run("figshare Figure becomes component", func(t *testing.T) {
		r := releaseWithDOI("10.6084/m9.figshare.12345")
		r.Title = "Figure 3 from Some Paper"
		r.Type = "graphic"
		biblioHacks(r, emptyDoc)
		// type is "graphic", which is excluded from component conversion
		if r.Type != "graphic" {
			t.Errorf("type: got %q, want %q", r.Type, "graphic")
		}
	})

	t.Run("figshare Figure with non-protected type becomes component", func(t *testing.T) {
		r := releaseWithDOI("10.6084/m9.figshare.12345")
		r.Title = "Figure 3 from Some Paper"
		r.Type = "dataset"
		biblioHacks(r, emptyDoc)
		if r.Type != "component" {
			t.Errorf("type: got %q, want %q", r.Type, "component")
		}
	})

	t.Run("figshare Table becomes component", func(t *testing.T) {
		r := releaseWithDOI("10.6084/m9.figshare.12345")
		r.Title = "Table S1 from Some Paper"
		r.Type = "dataset"
		biblioHacks(r, emptyDoc)
		if r.Type != "component" {
			t.Errorf("type: got %q, want %q", r.Type, "component")
		}
	})

	t.Run("figshare.com container name set when absent", func(t *testing.T) {
		r := releaseWithDOI("10.6084/m9.figshare.12345")
		biblioHacks(r, emptyDoc)
		if r.Extra["container_name"] != "figshare.com" {
			t.Errorf("container_name: got %v, want %q", r.Extra["container_name"], "figshare.com")
		}
	})

	t.Run("figshare.com container name not overwritten if already set", func(t *testing.T) {
		r := releaseWithDOI("10.6084/m9.figshare.12345")
		r.Extra["container_name"] = "existing"
		biblioHacks(r, emptyDoc)
		if r.Extra["container_name"] != "existing" {
			t.Errorf("container_name: got %v, want %q", r.Extra["container_name"], "existing")
		}
	})

	t.Run("Columbia IR IsVariantFormOf clears container and sets submitted", func(t *testing.T) {
		cid := newUUID()
		r := releaseWithDOI("10.7916/d8-abc-123")
		r.Publisher = "Columbia University"
		r.Stage = "published"
		r.ContainerID = &cid
		doc := docWithRelations("IsVariantFormOf")
		biblioHacks(r, doc)
		if r.ContainerID != nil {
			t.Error("ContainerID: expected nil")
		}
		if r.Stage != "submitted" {
			t.Errorf("Stage: got %q, want %q", r.Stage, "submitted")
		}
	})

	t.Run("Columbia IR without IsVariantFormOf leaves container intact", func(t *testing.T) {
		cid := newUUID()
		r := releaseWithDOI("10.7916/d8-abc-123")
		r.Publisher = "Columbia University"
		r.Stage = "published"
		r.ContainerID = &cid
		doc := docWithRelations("IsCitedBy")
		biblioHacks(r, doc)
		if r.ContainerID == nil {
			t.Error("ContainerID: expected non-nil")
		}
	})

	t.Run("Columbia check requires Columbia University publisher", func(t *testing.T) {
		cid := newUUID()
		r := releaseWithDOI("10.7916/d8-abc-123")
		r.Publisher = "Other Publisher"
		r.ContainerID = &cid
		doc := docWithRelations("IsVariantFormOf")
		biblioHacks(r, doc)
		if r.ContainerID == nil {
			t.Error("ContainerID: expected non-nil for non-Columbia publisher")
		}
	})

	t.Run("IR prefix IsVariantFormOf clears container", func(t *testing.T) {
		cid := newUUID()
		r := releaseWithDOI("10.18154/rwth-2021-12345")
		r.ContainerID = &cid
		doc := docWithRelations("IsVariantFormOf")
		biblioHacks(r, doc)
		if r.ContainerID != nil {
			t.Error("ContainerID: expected nil for IR prefix")
		}
	})

	t.Run("non-IR prefix DOI is unaffected", func(t *testing.T) {
		cid := newUUID()
		r := releaseWithDOI("10.1234/other")
		r.ContainerID = &cid
		doc := docWithRelations("IsVariantFormOf")
		biblioHacks(r, doc)
		if r.ContainerID == nil {
			t.Error("ContainerID: expected non-nil for unrelated DOI prefix")
		}
	})
}

// --- indexFormToDisplayName ---

func TestIndexFormToDisplayName(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"Razis, Panos A", "Panos A Razis"},
		{"Smith, John", "John Smith"},
		// no comma → unchanged
		{"John Smith", "John Smith"},
		// multiple commas → unchanged
		{"Dr. Hina, Dr. Usman, Dr. Khan", "Dr. Hina, Dr. Usman, Dr. Khan"},
		// parens → unchanged
		{"Smith (Jr.), John", "Smith (Jr.), John"},
		// asterisk → unchanged
		{"Smith*, John", "Smith*, John"},
		// organizational stopwords → unchanged
		{"University of Example, The", "University of Example, The"},
		{"Department of Physics, Applied", "Department of Physics, Applied"},
		{"International Organization, The", "International Organization, The"},
		// whitespace trimmed on parts
		{"  Smith  ,  John  ", "John Smith"},
	}

	for _, c := range cases {
		t.Run(c.input, func(t *testing.T) {
			got := indexFormToDisplayName(c.input)
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// --- parseContribs ---

func TestParseContribs(t *testing.T) {
	t.Run("empty list", func(t *testing.T) {
		got := parseContribs(nil, "author")
		if len(got) != 0 {
			t.Errorf("expected empty, got %d contribs", len(got))
		}
	})

	t.Run("personal with given and family name", func(t *testing.T) {
		creators := []dataciteCreator{{GivenName: "Jane", FamilyName: "Doe"}}
		got := parseContribs(creators, "author")
		if len(got) != 1 {
			t.Fatalf("expected 1 contrib, got %d", len(got))
		}
		if got[0].RawName != "Jane Doe" {
			t.Errorf("RawName: got %q, want %q", got[0].RawName, "Jane Doe")
		}
		if got[0].GivenName != "Jane" {
			t.Errorf("GivenName: got %q", got[0].GivenName)
		}
		if got[0].Surname != "Doe" {
			t.Errorf("Surname: got %q", got[0].Surname)
		}
		if got[0].Role != "author" {
			t.Errorf("Role: got %q", got[0].Role)
		}
		if got[0].Position != 0 {
			t.Errorf("Position: got %d", got[0].Position)
		}
	})

	t.Run("index-form name is converted to display form", func(t *testing.T) {
		creators := []dataciteCreator{{Name: "Smith, John"}}
		got := parseContribs(creators, "author")
		if len(got) != 1 {
			t.Fatalf("expected 1 contrib, got %d", len(got))
		}
		if got[0].RawName != "John Smith" {
			t.Errorf("RawName: got %q, want %q", got[0].RawName, "John Smith")
		}
	})

	t.Run("organizational contributor stored in extra", func(t *testing.T) {
		creators := []dataciteCreator{{Name: "CERN", NameType: "Organizational"}}
		got := parseContribs(creators, "author")
		if len(got) != 1 {
			t.Fatalf("expected 1 contrib, got %d", len(got))
		}
		if got[0].Extra["organization"] != "CERN" {
			t.Errorf("Extra[organization]: got %v", got[0].Extra["organization"])
		}
		if got[0].RawName != "" {
			t.Errorf("RawName: expected empty for org, got %q", got[0].RawName)
		}
	})

	t.Run("organizational name too short is skipped", func(t *testing.T) {
		creators := []dataciteCreator{{Name: "AB", NameType: "Organizational"}}
		got := parseContribs(creators, "author")
		if len(got) != 0 {
			t.Errorf("expected 0 contribs, got %d", len(got))
		}
	})

	t.Run("blocked name is skipped", func(t *testing.T) {
		creators := []dataciteCreator{{Name: "Occdownload Gbif.Org"}}
		got := parseContribs(creators, "author")
		if len(got) != 0 {
			t.Errorf("expected 0 contribs, got %d", len(got))
		}
	})

	t.Run("unknown marker name is skipped", func(t *testing.T) {
		creators := []dataciteCreator{{Name: "(:unkn)"}}
		got := parseContribs(creators, "author")
		if len(got) != 0 {
			t.Errorf("expected 0 contribs, got %d", len(got))
		}
	})

	t.Run("ORCID extracted from nameIdentifiers", func(t *testing.T) {
		creators := []dataciteCreator{{
			Name: "Jane Doe",
			NameIdentifiers: []dataciteNameIdentifier{
				{NameIdentifier: "https://orcid.org/0000-0001-2345-6789", NameIdentifierScheme: "ORCID"},
			},
		}}
		got := parseContribs(creators, "author")
		if len(got) != 1 {
			t.Fatalf("expected 1 contrib, got %d", len(got))
		}
		if got[0].Extra["orcid"] != "0000-0001-2345-6789" {
			t.Errorf("ORCID: got %v", got[0].Extra["orcid"])
		}
	})

	t.Run("duplicate same name and role is deduplicated", func(t *testing.T) {
		creators := []dataciteCreator{
			{Name: "Jane Doe"},
			{Name: "Jane Doe"},
		}
		got := parseContribs(creators, "author")
		if len(got) != 1 {
			t.Errorf("expected 1 contrib after dedup, got %d", len(got))
		}
	})

	t.Run("positions are sequential after dedup", func(t *testing.T) {
		creators := []dataciteCreator{
			{Name: "Alice"},
			{Name: "Bob"},
			{Name: "Alice"}, // duplicate
			{Name: "Carol"},
		}
		got := parseContribs(creators, "author")
		if len(got) != 3 {
			t.Fatalf("expected 3 contribs, got %d", len(got))
		}
		for i, c := range got {
			if c.Position != i {
				t.Errorf("contrib %d: Position=%d, want %d", i, c.Position, i)
			}
		}
	})

	t.Run("affiliation stored on contrib", func(t *testing.T) {
		creators := []dataciteCreator{{
			Name:        "Jane Doe",
			Affiliation: []dataciteAffiliation{{Name: "MIT"}},
		}}
		got := parseContribs(creators, "author")
		if len(got) != 1 {
			t.Fatalf("expected 1 contrib, got %d", len(got))
		}
		if got[0].RawAffiliation != "MIT" {
			t.Errorf("RawAffiliation: got %q", got[0].RawAffiliation)
		}
	})

	t.Run("contributor type stored in extra", func(t *testing.T) {
		creators := []dataciteCreator{{Name: "Jane Doe", ContributorType: "DataCurator"}}
		got := parseContribs(creators, "author")
		if len(got) != 1 {
			t.Fatalf("expected 1 contrib, got %d", len(got))
		}
		if got[0].Extra["type"] != "DataCurator" {
			t.Errorf("Extra[type]: got %v", got[0].Extra["type"])
		}
	})

	t.Run("unknown nameType is skipped", func(t *testing.T) {
		creators := []dataciteCreator{{Name: "Jane Doe", NameType: "Robot"}}
		got := parseContribs(creators, "author")
		if len(got) != 0 {
			t.Errorf("expected 0 contribs, got %d", len(got))
		}
	})
}

// --- flexibleString ---

func TestFlexibleString(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"string value", `{"volume":"42"}`, "42"},
		{"number value", `{"volume":42}`, "42"},
		{"float value", `{"volume":3.5}`, "3.5"},
		{"null value", `{"volume":null}`, ""},
		{"missing field", `{}`, ""},
		{"empty string", `{"volume":""}`, ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var container struct {
				Volume flexibleString `json:"volume"`
			}
			if err := json.Unmarshal([]byte(c.input), &container); err != nil {
				t.Fatalf("unmarshal error: %v", err)
			}
			if string(container.Volume) != c.want {
				t.Errorf("got %q, want %q", string(container.Volume), c.want)
			}
		})
	}
}

// --- dataciteLicenseSlug ---

func TestDataciteLicenseSlug(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"", ""},
		// CC handled by cleaning.LicenseSlugLookup
		{"https://creativecommons.org/licenses/by/4.0/", "CC-BY"},
		{"https://creativecommons.org/licenses/by-nc/4.0/", "CC-BY-NC"},
		{"https://creativecommons.org/publicdomain/zero/1.0/", "CC-0"},
		// rightsstatements.org via vocab path
		{"http://rightsstatements.org/vocab/InC/1.0/", "RS-INC"},
		{"http://rightsstatements.org/vocab/CNE/1.0/", "RS-CNE"},
		// rightsstatements.org via page path
		{"http://rightsstatements.org/page/InC/1.0/", "RS-INC"},
		// name too long for rightsstatements → not matched
		{"http://rightsstatements.org/vocab/TooLongNameHere/1.0/", ""},
		// unknown URL
		{"https://example.com/some-license", ""},
	}

	for _, c := range cases {
		t.Run(c.input, func(t *testing.T) {
			got := dataciteLicenseSlug(c.input)
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}
