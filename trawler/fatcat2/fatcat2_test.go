package fatcat2

import (
	"encoding/json"
	"testing"
	"time"
)

func Test_File_SetMetadata(t *testing.T) {
	f := File{}

	bs := []byte(sample)
	err := f.SetMetadata(bs)
	if err != nil {
		t.Errorf("did not expect error but got '%s'", err.Error())
	}

	if f.Size != len(bs) {
		t.Errorf("expected size %d, got %d", len(bs), f.Size)
	}

	expectedMd5 := "89f73763f13a6200d1ad29a85d82fde9"
	if f.Md5 != expectedMd5 {
		t.Errorf("expected md5 %s, got %s", expectedMd5, f.Md5)
	}

	expectedSha1 := "755c8252201ac4fa37ce4beb9dd1063abbea7985"
	if f.Sha1 != expectedSha1 {
		t.Errorf("expected sha1 %s, got %s", expectedSha1, f.Sha1)
	}

	expectedSha256 := "cb5b552624a1ee8ccc2e46e1950e8bc6972de03807ceeef229541c38ae3fe7c0"
	if f.Sha256 != expectedSha256 {
		t.Errorf("expected sha256 %s, got %s", expectedSha256, f.Sha256)
	}
}

func Test_Release_IsPaperlike(t *testing.T) {
	cs := []struct {
		releaseType string
		expected    bool
	}{
		{"article-journal", true},
		{"book", true},
		{"paper-conference", true},
		{"chapter", true},
		{"report", true},
		{"thesis", true},
		{"dataset", false},
		{"software", false},
		{"", false},
	}

	for _, c := range cs {
		t.Run(c.releaseType, func(t *testing.T) {
			r := Release{Type: c.releaseType}
			if r.IsPaperlike() != c.expected {
				t.Errorf("type %q: expected IsPaperlike()=%v, got %v", c.releaseType, c.expected, r.IsPaperlike())
			}
		})
	}
}

func Test_Release_FulltextURLs(t *testing.T) {
	cs := []struct {
		name     string
		release  Release
		expected []string
	}{
		{
			name:     "no IDs",
			release:  Release{},
			expected: []string{},
		},
		{
			name: "arxiv only",
			release: Release{
				ExternalIDs: []ExternalID{{Type: "arxiv", Value: "2301.00001"}},
			},
			expected: []string{"https://arxiv.org/pdf/2301.00001.pdf"},
		},
		{
			name: "pmcid only",
			release: Release{
				ExternalIDs: []ExternalID{{Type: "pmcid", Value: "PMC1234567"}},
			},
			expected: []string{
				"http://europepmc.org/backend/ptpmcrender.fcgi?accid=PMC1234567&blobtype=pdf",
			},
		},
		{
			name: "doi only",
			release: Release{
				ExternalIDs: []ExternalID{{Type: "doi", Value: "10.1234/foo"}},
			},
			expected: []string{"https://doi.org/10.1234/foo"},
		},
		{
			name: "pmid alone yields no URLs",
			release: Release{
				ExternalIDs: []ExternalID{{Type: "pmid", Value: "12345678"}},
			},
			expected: []string{},
		},
		{
			name: "arxiv + doi: arxiv first",
			release: Release{
				ExternalIDs: []ExternalID{
					{Type: "arxiv", Value: "2301.00001"},
					{Type: "doi", Value: "10.1234/foo"},
				},
			},
			expected: []string{
				"https://arxiv.org/pdf/2301.00001.pdf",
				"https://doi.org/10.1234/foo",
			},
		},
		{
			name: "all three: arxiv, pmcid, doi",
			release: Release{
				ExternalIDs: []ExternalID{
					{Type: "arxiv", Value: "2301.00001"},
					{Type: "pmcid", Value: "PMC9999"},
					{Type: "doi", Value: "10.1234/bar"},
				},
			},
			expected: []string{
				"https://arxiv.org/pdf/2301.00001.pdf",
				"http://europepmc.org/backend/ptpmcrender.fcgi?accid=PMC9999&blobtype=pdf",
				"https://doi.org/10.1234/bar",
			},
		},
		{
			name: "doaj only, no extra",
			release: Release{
				ExternalIDs: []ExternalID{{Type: "doaj", Value: "abc123"}},
			},
			expected: []string{
				"https://doaj.org/article/abc123",
			},
		},
		{
			name: "doaj with full_text_url in extra",
			release: Release{
				ExternalIDs: []ExternalID{{Type: "doaj", Value: "abc123"}},
				Extra: map[string]any{
					"doaj": map[string]any{"full_text_url": "https://example.com/paper.pdf"},
				},
			},
			expected: []string{
				"https://example.com/paper.pdf",
				"https://doaj.org/article/abc123",
			},
		},
		{
			name: "doaj with extra but no full_text_url key",
			release: Release{
				ExternalIDs: []ExternalID{{Type: "doaj", Value: "abc123"}},
				Extra: map[string]any{
					"doaj": map[string]any{"keywords": []string{"foo"}},
				},
			},
			expected: []string{
				"https://doaj.org/article/abc123",
			},
		},
		{
			name: "doaj + doi: doaj before doi",
			release: Release{
				ExternalIDs: []ExternalID{
					{Type: "doaj", Value: "abc123"},
					{Type: "doi", Value: "10.1234/foo"},
				},
				Extra: map[string]any{
					"doaj": map[string]any{"full_text_url": "https://example.com/paper.pdf"},
				},
			},
			expected: []string{
				"https://example.com/paper.pdf",
				"https://doaj.org/article/abc123",
				"https://doi.org/10.1234/foo",
			},
		},
	}

	for _, c := range cs {
		t.Run(c.name, func(t *testing.T) {
			got := c.release.FulltextURLs()
			if len(got) != len(c.expected) {
				t.Fatalf("expected %d URLs, got %d: %v", len(c.expected), len(got), got)
			}
			for i, u := range got {
				if u != c.expected[i] {
					t.Errorf("URL[%d]: expected %q, got %q", i, c.expected[i], u)
				}
			}
		})
	}
}

func Test_ReleaseDate_MarshalJSON(t *testing.T) {
	cs := []struct {
		name     string
		time     time.Time
		expected string
	}{
		{
			name:     "date only",
			time:     time.Date(2023, 5, 15, 0, 0, 0, 0, time.UTC),
			expected: `"2023-05-15"`,
		},
		{
			name:     "datetime with non-zero time is truncated to date",
			time:     time.Date(2023, 5, 15, 14, 30, 0, 0, time.UTC),
			expected: `"2023-05-15"`,
		},
	}
	for _, c := range cs {
		t.Run(c.name, func(t *testing.T) {
			rd := ReleaseDate(c.time)
			got, err := json.Marshal(&rd)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if string(got) != c.expected {
				t.Errorf("expected %s, got %s", c.expected, string(got))
			}
		})
	}
}

const sample = `
A PIECE OF COFFEE.

More of double.

A place in no new table.

A single image is not splendor. Dirty is yellow. A sign of more in not
mentioned. A piece of coffee is not a detainer. The resemblance to
yellow is dirtier and distincter. The clean mixture is whiter and not
coal color, never more coal color than altogether.

The sight of a reason, the same sight slighter, the sight of a simpler
negative answer, the same sore sounder, the intention to wishing, the
same splendor, the same furniture.

The time to show a message is when too late and later there is no
hanging in a blight.

A not torn rose-wood color. If it is not dangerous then a pleasure and
more than any other if it is cheap is not cheaper. The amusing side is
that the sooner there are no fewer the more certain is the necessity
dwindled. Supposing that the case contained rose-wood and a color.
Supposing that there was no reason for a distress and more likely for a
number, supposing that there was no astonishment, is it not necessary to
mingle astonishment.

The settling of stationing cleaning is one way not to shatter scatter
and scattering. The one way to use custom is to use soap and silk for
cleaning. The one way to see cotton is to have a design concentrating
the illusion and the illustration. The perfect way is to accustom the
thing to have a lining and the shape of a ribbon and to be solid, quite
solid in standing and to use heaviness in morning. It is light enough in
that. It has that shape nicely. Very nicely may not be exaggerating.
Very strongly may be sincerely fainting. May be strangely flattering.
May not be strange in everything. May not be strange to.
`
