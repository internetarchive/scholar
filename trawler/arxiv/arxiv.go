package arxiv

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

const maxAuthors = 2000

// arxivRecord is the deserializable flat representation of an arXiv OAI-PMH
// record as written by scholkit's ArxivHarvester. Each ndjson line is one
// arxivRecord.
type arxivRecord struct {
	Identifier string        `json:"identifier"` // "oai:arXiv.org:2301.12345"
	Status     string        `json:"status"`     // "deleted" or ""
	Datestamp  string        `json:"datestamp"`  // "2023-01-15"
	SetSpec    []string      `json:"set_spec"`
	ID         string        `json:"id"` // "2301.12345" (no version suffix)
	Title      string        `json:"title"`
	Authors    []arxivAuthor `json:"authors"`
	Categories string        `json:"categories"` // space-separated "cs.AI cs.LG"
	Abstract   string        `json:"abstract"`
	DOI        string        `json:"doi"`
	Created    string        `json:"created"` // "2007-04-02"
	Updated    string        `json:"updated"`
	Comments   string        `json:"comments"`
	License    string        `json:"license"`
	JournalRef string        `json:"journal_ref"`
	ReportNo   string        `json:"report_no"`
}

type arxivAuthor struct {
	KeyName     string `json:"keyname"`
	ForeName    string `json:"forename"`
	Suffix      string `json:"suffix"`
	Affiliation string `json:"affiliation"`
}

// arxivID returns the base arxiv ID without "oai:arXiv.org:" prefix or version suffix.
func arxivID(rec *arxivRecord) string {
	if rec.ID != "" {
		return rec.ID
	}
	return strings.TrimPrefix(rec.Identifier, "oai:arXiv.org:")
}

// versionedArxivID returns the arxiv ID with a version suffix. The arXiv OAI
// feed provides base IDs only (no version info), so we default to "v1". This
// satisfies the fatcat2 requirement that stored arxiv IDs be versioned.
func versionedArxivID(rec *arxivRecord) string {
	id := arxivID(rec)
	if id == "" {
		return ""
	}
	// If the ID already has a version suffix (e.g. from a future data source), keep it.
	if i := strings.LastIndex(id, "v"); i > 0 {
		suffix := id[i+1:]
		if suffix != "" && strings.IndexFunc(suffix, func(r rune) bool { return r < '0' || r > '9' }) == -1 {
			return id
		}
	}
	return id + "v1"
}

// skipReason returns a non-empty string explaining why this record should be
// skipped, or empty string if it should be processed.
func skipReason(rec *arxivRecord) string {
	if arxivID(rec) == "" {
		return "no-arxiv-id"
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

// releaseType determines the fatcat release type from arxiv metadata.
// Defaults to "article-journal"; detects conference papers from journal-ref
// and reports from report-no.
func releaseType(rec *arxivRecord) string {
	jr := strings.ToLower(rec.JournalRef)
	if strings.Contains(jr, "conf.") || strings.Contains(jr, "proc.") ||
		strings.Contains(jr, "proceedings") || strings.Contains(jr, "workshop") {
		return "paper-conference"
	}
	if rec.ReportNo != "" {
		return "report"
	}
	return "article-journal"
}

// releaseDate parses an arxiv date string ("2006-01-02") into a fatcat2.ReleaseDate.
func releaseDate(s string) *fatcat2.ReleaseDate {
	if s == "" {
		return nil
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return nil
	}
	rd := fatcat2.ReleaseDate(t)
	return &rd
}

// releaseYear extracts the year from a date string, returning 0 on failure.
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

// contribs converts arxiv author records to fatcat2 contribs. There is no
// ORCID in the arXiv OAI format.
func contribs(authors []arxivAuthor) []fatcat2.ReleaseContrib {
	out := make([]fatcat2.ReleaseContrib, 0, len(authors))
	for i, a := range authors {
		c := fatcat2.ReleaseContrib{
			Role:     "author",
			Position: i,
			Extra:    map[string]any{},
		}
		c.Surname = a.KeyName
		c.GivenName = a.ForeName
		switch {
		case a.ForeName != "" && a.KeyName != "":
			c.RawName = fmt.Sprintf("%s %s", a.ForeName, a.KeyName)
		case a.KeyName != "":
			c.RawName = a.KeyName
		}
		if a.Affiliation != "" {
			c.RawAffiliation = a.Affiliation
		}
		if a.Suffix != "" {
			c.Extra["suffix"] = a.Suffix
		}
		out = append(out, c)
	}
	return out
}

// abstract builds a fatcat2.Abstract from the record's abstract text if it
// meets the minimum length.
func abstract(text string) []fatcat2.Abstract {
	text = strings.TrimSpace(text)
	if len(text) < cleaning.MinAbstractLength {
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

// categories returns the list of arxiv subject categories.
func categories(rec *arxivRecord) []string {
	cats := strings.Fields(rec.Categories)
	if len(cats) == 0 {
		cats = rec.SetSpec
	}
	return cats
}

// arxivToFc transforms an arxivRecord into a fatcat2.Release.
func arxivToFc(rec *arxivRecord, source string) *fatcat2.Release {
	release := &fatcat2.Release{
		Contribs:    []fatcat2.ReleaseContrib{},
		ExternalIDs: []fatcat2.ExternalID{},
		Extra:       map[string]any{},
		Abstracts:   []fatcat2.Abstract{},
		Refs:        []fatcat2.RawRef{},
		Source:      source,
	}

	// --- Identifiers ---

	// fatcat2 requires versioned arxiv IDs (e.g. "2301.12345v1"). The arXiv
	// OAI feed only provides the base ID, so we append "v1" by default.
	vid := versionedArxivID(rec)
	release.ExternalIDs = append(release.ExternalIDs, fatcat2.ExternalID{
		Type:  "arxiv",
		Value: vid,
	})

	doi := cleaning.NormalizeDOI(rec.DOI)
	if doi != "" {
		release.ExternalIDs = append(release.ExternalIDs, fatcat2.ExternalID{
			Type:  "doi",
			Value: doi,
		})
	}

	// --- Type and stage ---

	release.Type = releaseType(rec)
	release.Stage = "submitted"

	// --- Title ---

	release.Title = cleaning.CleanString(rec.Title)

	// --- Language ---

	// arXiv OAI "arXiv" prefix does not include language; default to English.
	release.Language = "en"

	// --- Date ---

	dateStr := rec.Created
	if dateStr == "" {
		dateStr = rec.Datestamp
	}
	release.ReleaseDate = releaseDate(dateStr)
	release.ReleaseYear = releaseYear(dateStr)

	// --- Abstract ---

	release.Abstracts = abstract(rec.Abstract)

	// --- Contribs ---

	release.Contribs = contribs(rec.Authors)

	// --- License ---

	release.LicenseSlug = cleaning.LicenseSlugLookup(rec.License)

	// --- Extra ---

	cats := categories(rec)
	arxivExtra := map[string]any{}
	if len(cats) > 0 {
		arxivExtra["categories"] = cats
	}
	if rec.Comments != "" {
		arxivExtra["comments"] = rec.Comments
	}
	if rec.JournalRef != "" {
		arxivExtra["journal_ref"] = rec.JournalRef
	}
	if rec.ReportNo != "" {
		arxivExtra["report_no"] = rec.ReportNo
	}
	if len(arxivExtra) > 0 {
		release.Extra["arxiv"] = arxivExtra
	}

	return release
}

// createRelease creates the release in fatcat2 and indexes it to Elasticsearch.
func createRelease(client *http.Client, cs *counts.Counts, release fatcat2.Release) (*fatcat2.Release, error) {
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

	var rec arxivRecord
	if err := json.Unmarshal(lineb, &rec); err != nil {
		return out, release, fmt.Errorf("arxiv unmarshal: %w", err)
	}

	id := arxivID(&rec)
	vid := versionedArxivID(&rec)

	if reason := skipReason(&rec); reason != "" {
		l.Info("arxiv: skipping record", "id", id, "reason", reason)
		out.Releases.Skipped++
		return out, release, nil
	}

	release = arxivToFc(&rec, source)

	// Lookup by versioned arxiv ID first, then DOI as fallback.
	foundID, err := fatcat2.LookupArxiv(client, vid)
	if err != nil {
		return out, release, fmt.Errorf("arxiv lookup failed for %q: %w", vid, err)
	}

	if foundID == nil && release.DOI() != "" {
		foundID, err = fatcat2.LookupDoi(client, release.DOI())
		if err != nil {
			return out, release, fmt.Errorf("doi lookup failed for %q: %w", release.DOI(), err)
		}
	}

	if foundID != nil {
		l.Debug("arxiv: found existing release", "id", vid, "release_id", foundID)
		r, err := fatcat2.GetRelease(client, *foundID)
		if err != nil {
			return out, release, fmt.Errorf("could not fetch existing release: %w", err)
		}
		release = &r
		out.Releases.Ignored++
	} else {
		release, err = createRelease(client, &out, *release)
		if err != nil {
			return out, release, fmt.Errorf("could not create release for arxiv %q: %w", vid, err)
		}
		l.Debug("arxiv: created release", "id", vid, "release_id", release.ID)
		out.Releases.Added++
	}

	return out, release, nil
}
