package arxiv

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func makeRecord(id, status, title string, authors []arxivAuthor) *arxivRecord {
	return &arxivRecord{
		Identifier: "oai:arXiv.org:" + id,
		ID:         id,
		Status:     status,
		Title:      title,
		Authors:    authors,
		Datestamp:  "2024-01-15",
	}
}

func TestSkipReason(t *testing.T) {
	tests := []struct {
		name   string
		rec    *arxivRecord
		reason string
	}{
		{
			name:   "no arxiv id",
			rec:    &arxivRecord{Identifier: "", ID: "", Title: "Some Title"},
			reason: "no-arxiv-id",
		},
		{
			name:   "deleted record",
			rec:    makeRecord("2301.12345", "deleted", "Some Title", nil),
			reason: "deleted",
		},
		{
			name:   "empty title",
			rec:    makeRecord("2301.12345", "", "", nil),
			reason: "empty-title",
		},
		{
			name:   "whitespace-only title",
			rec:    makeRecord("2301.12345", "", "   ", nil),
			reason: "empty-title",
		},
		{
			name: "too many authors",
			rec: func() *arxivRecord {
				r := makeRecord("2301.12345", "", "Valid Title", nil)
				r.Authors = make([]arxivAuthor, maxAuthors+1)
				return r
			}(),
			reason: "too-many-authors",
		},
		{
			name: "exactly at author limit",
			rec: func() *arxivRecord {
				r := makeRecord("2301.12345", "", "Valid Title", nil)
				r.Authors = make([]arxivAuthor, maxAuthors)
				return r
			}(),
			reason: "",
		},
		{
			name:   "valid record",
			rec:    makeRecord("2301.12345", "", "A Valid Title", []arxivAuthor{{KeyName: "Smith", ForeName: "John"}}),
			reason: "",
		},
		{
			name:   "old-format arxiv id",
			rec:    makeRecord("hep-th/9901001", "", "Theoretical Physics Paper", nil),
			reason: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.reason, skipReason(tt.rec))
		})
	}
}

func TestArxivID(t *testing.T) {
	tests := []struct {
		name       string
		rec        *arxivRecord
		expectedID string
	}{
		{
			name:       "id field takes precedence",
			rec:        &arxivRecord{ID: "2301.12345", Identifier: "oai:arXiv.org:2301.99999"},
			expectedID: "2301.12345",
		},
		{
			name:       "fallback to identifier stripping",
			rec:        &arxivRecord{ID: "", Identifier: "oai:arXiv.org:2301.12345"},
			expectedID: "2301.12345",
		},
		{
			name:       "old format",
			rec:        &arxivRecord{ID: "hep-th/9901001", Identifier: "oai:arXiv.org:hep-th/9901001"},
			expectedID: "hep-th/9901001",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expectedID, arxivID(tt.rec))
		})
	}
}

func TestVersionedArxivID(t *testing.T) {
	tests := []struct {
		name     string
		rec      *arxivRecord
		expected string
	}{
		{
			name:     "new format base id gets v1",
			rec:      &arxivRecord{ID: "2301.12345"},
			expected: "2301.12345v1",
		},
		{
			name:     "old format base id gets v1",
			rec:      &arxivRecord{ID: "hep-th/9901001"},
			expected: "hep-th/9901001v1",
		},
		{
			name:     "already versioned id is unchanged",
			rec:      &arxivRecord{ID: "2301.12345v2"},
			expected: "2301.12345v2",
		},
		{
			name:     "v1 suffix already present is unchanged",
			rec:      &arxivRecord{ID: "2301.12345v1"},
			expected: "2301.12345v1",
		},
		{
			name:     "fallback from identifier",
			rec:      &arxivRecord{ID: "", Identifier: "oai:arXiv.org:2301.12345"},
			expected: "2301.12345v1",
		},
		{
			name:     "empty id returns empty",
			rec:      &arxivRecord{ID: "", Identifier: ""},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, versionedArxivID(tt.rec))
		})
	}
}

func TestDoiFromRecord(t *testing.T) {
	tests := []struct {
		name     string
		doi      string
		expected string
	}{
		{"bare doi", "10.1234/test", "10.1234/test"},
		{"doi prefix", "doi:10.1234/test", "10.1234/test"},
		{"https doi.org", "https://doi.org/10.1234/test", "10.1234/test"},
		{"http dx.doi.org", "http://dx.doi.org/10.1234/test", "10.1234/test"},
		{"https dx.doi.org", "https://dx.doi.org/10.1234/test", "10.1234/test"},
		{"empty", "", ""},
		{"url not doi", "https://arxiv.org/abs/2301.12345", ""},
		{"uppercased", "10.1234/TEST", "10.1234/test"},
		{"with whitespace", "  10.1234/test  ", "10.1234/test"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &arxivRecord{DOI: tt.doi}
			assert.Equal(t, tt.expected, doiFromRecord(rec))
		})
	}
}

func TestReleaseDate(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		isNil    bool
		expected string
	}{
		{"valid date", "2007-04-02", false, "2007-04-02"},
		{"empty", "", true, ""},
		{"invalid", "not-a-date", true, ""},
		{"year only", "2007", true, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rd := releaseDate(tt.input)
			if tt.isNil {
				assert.Nil(t, rd)
			} else {
				assert.NotNil(t, rd)
			}
		})
	}
}

func TestReleaseType(t *testing.T) {
	tests := []struct {
		name       string
		journalRef string
		reportNo   string
		expected   string
	}{
		{"default", "", "", "article-journal"},
		{"conference from proc.", "Proc. IEEE Conf. 2023", "", "paper-conference"},
		{"conference from conf.", "conf. on Neural Networks", "", "paper-conference"},
		{"conference from proceedings", "Proceedings of NeurIPS", "", "paper-conference"},
		{"conference from workshop", "workshop on ML", "", "paper-conference"},
		{"report from report-no", "", "arXiv-TR-2023-001", "report"},
		{"journal ref wins over report", "Proc. of some conf.", "RPT-001", "paper-conference"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &arxivRecord{JournalRef: tt.journalRef, ReportNo: tt.reportNo}
			assert.Equal(t, tt.expected, releaseType(rec))
		})
	}
}

func TestArxivToFc(t *testing.T) {
	rec := &arxivRecord{
		ID:         "2301.12345",
		Identifier: "oai:arXiv.org:2301.12345",
		Title:      "A Test Paper Title",
		Abstract:   "This is a long enough abstract that meets the minimum length requirement for inclusion in the release record.",
		DOI:        "10.1234/test.paper",
		Created:    "2023-01-15",
		Categories: "cs.AI cs.LG",
		Authors: []arxivAuthor{
			{KeyName: "Smith", ForeName: "John"},
			{KeyName: "Doe", ForeName: "Jane", Affiliation: "MIT"},
		},
		Comments:   "10 pages, 3 figures",
		JournalRef: "Nature 2023",
		ReportNo:   "",
		License:    "http://arxiv.org/licenses/nonexclusive-distrib/1.0/",
	}

	release := arxivToFc(rec, "arxiv-2023-01-15-abc123")

	// Identifiers — stored ID must be versioned
	assert.Equal(t, "arxiv", release.ExternalIDs[0].Type)
	assert.Equal(t, "2301.12345v1", release.ExternalIDs[0].Value)
	assert.Equal(t, "doi", release.ExternalIDs[1].Type)
	assert.Equal(t, "10.1234/test.paper", release.ExternalIDs[1].Value)

	// Type and stage
	assert.Equal(t, "article-journal", release.Type)
	assert.Equal(t, "submitted", release.Stage)

	// Title
	assert.Equal(t, "A Test Paper Title", release.Title)

	// Language default
	assert.Equal(t, "en", release.Language)

	// Date
	assert.Equal(t, 2023, release.ReleaseYear)
	assert.NotNil(t, release.ReleaseDate)

	// Abstract
	assert.Len(t, release.Abstracts, 1)
	assert.Equal(t, "text/plain", release.Abstracts[0].MIMEType)
	assert.NotEmpty(t, release.Abstracts[0].SHA1)

	// Contribs
	assert.Len(t, release.Contribs, 2)
	assert.Equal(t, "John Smith", release.Contribs[0].RawName)
	assert.Equal(t, "Smith", release.Contribs[0].Surname)
	assert.Equal(t, "John", release.Contribs[0].GivenName)
	assert.Equal(t, "author", release.Contribs[0].Role)
	assert.Equal(t, 0, release.Contribs[0].Position)
	assert.Equal(t, "MIT", release.Contribs[1].RawAffiliation)

	// Extra
	arxivExtra, ok := release.Extra["arxiv"].(map[string]any)
	assert.True(t, ok)
	cats, ok := arxivExtra["categories"].([]string)
	assert.True(t, ok)
	assert.Equal(t, []string{"cs.AI", "cs.LG"}, cats)
	assert.Equal(t, "10 pages, 3 figures", arxivExtra["comments"])

	// License
	assert.Equal(t, "ARXIV-1.0", release.LicenseSlug)

	// Source
	assert.Equal(t, "arxiv-2023-01-15-abc123", release.Source)
}

func TestArxivToFcCCLicense(t *testing.T) {
	rec := &arxivRecord{
		ID:         "2301.99999",
		Identifier: "oai:arXiv.org:2301.99999",
		Title:      "A CC Licensed Paper",
		Abstract:   "This is a long enough abstract that meets the minimum length requirement for inclusion in the release record.",
		Created:    "2023-01-15",
		Authors:    []arxivAuthor{{KeyName: "Smith", ForeName: "John"}},
		License:    "https://creativecommons.org/licenses/by/4.0/",
	}
	release := arxivToFc(rec, "arxiv-2023-01-15-abc123")
	assert.Equal(t, "CC-BY", release.LicenseSlug)
}

func TestArxivToFcNoLicense(t *testing.T) {
	rec := &arxivRecord{
		ID:         "2301.88888",
		Identifier: "oai:arXiv.org:2301.88888",
		Title:      "A Paper Without a License",
		Abstract:   "This is a long enough abstract that meets the minimum length requirement for inclusion in the release record.",
		Created:    "2023-01-15",
		Authors:    []arxivAuthor{{KeyName: "Smith", ForeName: "John"}},
	}
	release := arxivToFc(rec, "arxiv-2023-01-15-abc123")
	assert.Equal(t, "", release.LicenseSlug)
}

func TestAbstract(t *testing.T) {
	long := "This abstract is longer than the minimum required length of 75 characters for inclusion."
	short := "Too short."

	assert.Len(t, abstract(long), 1)
	assert.Len(t, abstract(short), 0)
	assert.Len(t, abstract(""), 0)
	assert.Len(t, abstract("   "), 0)
}

func TestCategories(t *testing.T) {
	tests := []struct {
		name     string
		rec      *arxivRecord
		expected []string
	}{
		{
			name:     "from categories field",
			rec:      &arxivRecord{Categories: "cs.AI cs.LG math.ST"},
			expected: []string{"cs.AI", "cs.LG", "math.ST"},
		},
		{
			name:     "fallback to set_spec",
			rec:      &arxivRecord{Categories: "", SetSpec: []string{"cs", "math"}},
			expected: []string{"cs", "math"},
		},
		{
			name:     "empty",
			rec:      &arxivRecord{},
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, categories(tt.rec))
		})
	}
}
