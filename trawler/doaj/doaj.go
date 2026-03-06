package doaj

import (
	"context"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"git.archive.org/webgroup/scholar/trawler/cleaning"
	"git.archive.org/webgroup/scholar/trawler/counts"
	"git.archive.org/webgroup/scholar/trawler/fatcat2"
	"git.archive.org/webgroup/scholar/trawler/indexing"
	"github.com/spf13/viper"
	"go.temporal.io/sdk/activity"
)

const minAbstractLength = 75
const maxAuthors = 2000

// doajRecord is the deserializable flat representation of a DOAJ OAI-PMH
// article record as written by scholkit's DOAJHarvester. Each ndjson line is
// one doajRecord.
type doajRecord struct {
	Identifier      string       `json:"identifier"`
	Status          string       `json:"status"`
	Datestamp       string       `json:"datestamp"`
	SetSpec         []string     `json:"set_spec"`
	ID              string       `json:"id"`
	Title           string       `json:"title"`
	Publisher       string       `json:"publisher"`
	JournalTitle    string       `json:"journal_title"`
	ISSN            string       `json:"issn"`
	EISSN           string       `json:"eissn"`
	PublicationDate string       `json:"publication_date"`
	Volume          string       `json:"volume"`
	Issue           string       `json:"issue"`
	StartPage       string       `json:"start_page"`
	EndPage         string       `json:"end_page"`
	DOI             string       `json:"doi"`
	Language        string       `json:"language"`
	Abstract        string       `json:"abstract"`
	Authors         []doajAuthor `json:"authors"`
	Keywords        []string     `json:"keywords"`
	FullTextURL     string       `json:"full_text_url"`
	FullTextFormat  string       `json:"full_text_format"`
	LicenseRef      string       `json:"license_ref"`
}

type doajAuthor struct {
	Name        string `json:"name"`
	Affiliation string `json:"affiliation"`
	OrcidID     string `json:"orcid_id"`
}

// doajID returns the DOAJ article ID, falling back to stripping the OAI prefix.
func doajID(rec *doajRecord) string {
	if rec.ID != "" {
		return rec.ID
	}
	const prefix = "oai:doaj.org/article:"
	return strings.TrimPrefix(rec.Identifier, prefix)
}

// skipReason returns a non-empty string explaining why this record should be
// skipped, or empty string if it should be processed.
func skipReason(rec *doajRecord) string {
	if doajID(rec) == "" {
		return "no-doaj-id"
	}
	if rec.Status == "deleted" {
		return "deleted"
	}
	if strings.TrimSpace(rec.Title) == "" {
		return "empty-title"
	}
	if len(rec.Authors) > maxAuthors {
		return "too-many-authors"
	}
	return ""
}

// releaseDate parses a DOAJ date string into a fatcat2.ReleaseDate.
func releaseDate(s string) *fatcat2.ReleaseDate {
	if s == "" {
		return nil
	}
	// DOAJ dates may be "YYYY-MM-DD" or just "YYYY"
	var t time.Time
	var err error
	switch len(s) {
	case 10:
		t, err = time.Parse("2006-01-02", s)
	case 4:
		t, err = time.Parse("2006", s)
	default:
		t, err = time.Parse("2006-01-02", s)
	}
	if err != nil {
		return nil
	}
	rd := fatcat2.ReleaseDate(t)
	return &rd
}

// releaseYear extracts the year from a date string.
func releaseYear(s string) int {
	if len(s) < 4 {
		return 0
	}
	var y int
	if _, err := fmt.Sscanf(s[:4], "%d", &y); err != nil {
		return 0
	}
	return y
}


// licenseSlug converts a CC license URL to a fatcat license slug.
// e.g. "https://creativecommons.org/licenses/by/4.0/" → "cc-by"
func licenseSlug(ref string) string {
	ref = strings.ToLower(ref)
	// match creativecommons.org/licenses/<type>/
	const ccPrefix = "creativecommons.org/licenses/"
	idx := strings.Index(ref, ccPrefix)
	if idx == -1 {
		return ""
	}
	rest := ref[idx+len(ccPrefix):]
	// rest is like "by/4.0/" or "by-nc/4.0/"
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) == 0 || parts[0] == "" {
		return ""
	}
	return "cc-" + parts[0]
}

// contribs converts DOAJ author records to fatcat2 contribs.
func contribs(authors []doajAuthor) []fatcat2.ReleaseContrib {
	out := make([]fatcat2.ReleaseContrib, 0, len(authors))
	for i, a := range authors {
		if a.Name == "" {
			continue
		}
		c := fatcat2.ReleaseContrib{
			Role:     "author",
			Position: i,
			Extra:    map[string]any{},
		}
		c.RawName = a.Name
		// Attempt to split "Surname, Given" or "Given Surname"
		if comma := strings.Index(a.Name, ","); comma > 0 {
			c.Surname = strings.TrimSpace(a.Name[:comma])
			c.GivenName = strings.TrimSpace(a.Name[comma+1:])
		}
		if a.Affiliation != "" {
			c.RawAffiliation = a.Affiliation
		}
		out = append(out, c)
	}
	return out
}

// abstract builds a fatcat2.Abstract if the text meets the minimum length.
func abstract(text string) []fatcat2.Abstract {
	text = strings.TrimSpace(text)
	if len(text) < minAbstractLength {
		return nil
	}
	h := sha1.Sum([]byte(text))
	return []fatcat2.Abstract{
		{
			Content:  text,
			MIMEType: "text/plain",
			SHA1:     fmt.Sprintf("%x", h),
		},
	}
}

// pages formats start/end page into a single pages string.
func pages(start, end string) string {
	if start != "" && end != "" {
		return start + "-" + end
	}
	return start
}

// doajToFc transforms a doajRecord into a fatcat2.Release.
func doajToFc(rec *doajRecord, source string) *fatcat2.Release {
	release := &fatcat2.Release{
		Contribs:    []fatcat2.ReleaseContrib{},
		ExternalIDs: []fatcat2.ExternalID{},
		Extra:       map[string]any{},
		Abstracts:   []fatcat2.Abstract{},
		Refs:        []fatcat2.RawRef{},
		Source:      source,
	}

	// --- Type and stage ---

	release.Type = "article-journal"
	release.Stage = "published"

	// --- Identifiers ---

	id := doajID(rec)
	release.ExternalIDs = append(release.ExternalIDs, fatcat2.ExternalID{
		Type:  "doaj",
		Value: id,
	})

	doi := cleaning.NormalizeDOI(rec.DOI)
	if doi != "" {
		release.ExternalIDs = append(release.ExternalIDs, fatcat2.ExternalID{
			Type:  "doi",
			Value: doi,
		})
	}

	// --- Title ---

	release.Title = cleaning.CleanString(rec.Title)

	// --- Language ---

	if rec.Language != "" {
		release.Language = cleaning.NormalizeLanguage(rec.Language)
	}

	// --- Date ---

	release.ReleaseDate = releaseDate(rec.PublicationDate)
	release.ReleaseYear = releaseYear(rec.PublicationDate)

	// --- Volume / Issue / Pages ---

	release.Volume = rec.Volume
	release.Issue = rec.Issue
	release.Pages = pages(rec.StartPage, rec.EndPage)
	release.Publisher = rec.Publisher

	// --- License ---

	// TODO not seeing license data on anything from DOAJ's oaipmh endpoint
	release.LicenseSlug = licenseSlug(rec.LicenseRef)

	// --- Abstract ---

	release.Abstracts = abstract(rec.Abstract)

	// --- Contribs ---

	release.Contribs = contribs(rec.Authors)

	// --- Extra ---

	doajExtra := map[string]any{}
	if len(rec.Keywords) > 0 {
		doajExtra["keywords"] = rec.Keywords
	}
	if rec.FullTextURL != "" {
		doajExtra["full_text_url"] = rec.FullTextURL
	}
	if len(doajExtra) > 0 {
		release.Extra["doaj"] = doajExtra
	}

	return release
}

// createRelease creates the release in fatcat2 and indexes it to Elasticsearch.
func createRelease(client *http.Client, release fatcat2.Release) (*fatcat2.Release, error) {
	id, err := fatcat2.CreateRelease(client, release)
	if err != nil {
		return nil, fmt.Errorf("release creation failed: %w", err)
	}

	r, err := fatcat2.GetRelease(client, *id)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch new release %q: %w", id, err)
	}

	releaseDoc, err := indexing.PrepareFatcatReleaseDoc(client, r)
	if err != nil {
		return nil, fmt.Errorf("failed to transform release into ES doc: %w", err)
	}

	bs, err := json.Marshal(releaseDoc)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal release ES doc: %w", err)
	}

	err = indexing.DoElasticIndex(client,
		viper.GetString("indexing.fatcat_release_ix"), releaseDoc.LegacyIdent, bs)
	if err != nil {
		return nil, fmt.Errorf("failed to index release: %w", err)
	}

	return &r, nil
}

func ProcessLine(ctx context.Context, client *http.Client, source string, lineb []byte) (counts.Counts, *fatcat2.Release, error) {
	out := counts.Counts{}
	l := activity.GetLogger(ctx)

	var release *fatcat2.Release

	var rec doajRecord
	if err := json.Unmarshal(lineb, &rec); err != nil {
		return out, release, fmt.Errorf("doaj unmarshal: %w", err)
	}

	id := doajID(&rec)

	if reason := skipReason(&rec); reason != "" {
		l.Info("doaj: skipping record", "id", id, "reason", reason)
		out.Releases.Skipped++
		return out, release, nil
	}

	release = doajToFc(&rec, source)

	// Lookup by DOAJ article ID first, then DOI as fallback.
	foundID, err := fatcat2.LookupDoaj(client, id)
	if err != nil {
		return out, release, fmt.Errorf("doaj lookup failed for %q: %w", id, err)
	}

	if foundID == nil && release.DOI() != "" {
		foundID, err = fatcat2.LookupDoi(client, release.DOI())
		if err != nil {
			return out, release, fmt.Errorf("doi lookup failed for %q: %w", release.DOI(), err)
		}
	}

	if foundID != nil {
		l.Debug("doaj: found existing release", "id", id, "release_id", foundID)
		r, err := fatcat2.GetRelease(client, *foundID)
		if err != nil {
			return out, release, fmt.Errorf("could not fetch existing release: %w", err)
		}
		release = &r
		out.Releases.Ignored++
	} else {
		release, err = createRelease(client, *release)
		if err != nil {
			return out, release, fmt.Errorf("could not create release for doaj %q: %w", id, err)
		}
		l.Debug("doaj: created release", "id", id, "release_id", release.ID)
		out.Releases.Added++
	}

	return out, release, nil
}
