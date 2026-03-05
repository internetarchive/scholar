package datacite

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"git.archive.org/webgroup/scholar/trawler/counts"
	"git.archive.org/webgroup/scholar/trawler/fatcat2"
	"go.temporal.io/sdk/activity"
)

type dataciteTitle struct {
	Title     string `json:"title"`
	TitleType string `json:"titleType"`
	Lang      string `json:"lang"`
}

type dataciteTypes struct {
	ResourceTypeGeneral string `json:"resourceTypeGeneral"`
	Citeproc            string `json:"citeproc"`
	Ris                 string `json:"ris"`
	Bibtex              string `json:"bibtex"`
	SchemaOrg           string `json:"schemaOrg"`
}

type dataciteDoc struct {
	ID         string `json:"id"`
	Attributes struct {
		DOI             string          `json:"doi"`
		Titles          []dataciteTitle `json:"titles"`
		Types           dataciteTypes   `json:"types"`
		PublicationYear int             `json:"publicationYear"`
		Published       string          `json:"published"`
		Publisher       string          `json:"publisher"`
		Language        string          `json:"language"`
	} `json:"attributes"`
}

// skipReason returns a non-empty string explaining why this record should be
// skipped, or empty string if it should be processed.
func skipReason(doc *dataciteDoc) string {
	if doc.Attributes.DOI == "" {
		return "no-doi"
	}

	if !isASCII(doc.Attributes.DOI) {
		return "non-ascii-doi"
	}

	title := mainTitle(doc.Attributes.Titles)
	if strings.TrimSpace(title) == "" {
		return "no-title"
	}

	return ""
}

func isASCII(s string) bool {
	for i := range len(s) {
		if s[i] > 127 {
			return false
		}
	}
	return true
}

// mainTitle returns the primary title from a list of DataCite titles,
// preferring entries with no titleType (the main title).
func mainTitle(titles []dataciteTitle) string {
	for _, t := range titles {
		if t.TitleType == "" {
			return t.Title
		}
	}
	if len(titles) > 0 {
		return titles[0].Title
	}
	return ""
}

// dataciteToFc transforms a dataciteDoc into a fatcat2.Release.
// TODO: implement full transformation.
func dataciteToFc(doc *dataciteDoc, source string) *fatcat2.Release {
	release := &fatcat2.Release{
		Contribs:    []fatcat2.ReleaseContrib{},
		ExternalIDs: []fatcat2.ExternalID{},
		Extra:       map[string]any{},
		Abstracts:   []fatcat2.Abstract{},
		Refs:        []fatcat2.RawRef{},
		Source:      source,
	}

	release.ExternalIDs = append(release.ExternalIDs, fatcat2.ExternalID{
		Type:  "doi",
		Value: strings.ToLower(doc.Attributes.DOI),
	})

	release.Title = mainTitle(doc.Attributes.Titles)
	release.Publisher = doc.Attributes.Publisher
	release.Language = doc.Attributes.Language

	return release
}

func ProcessLine(ctx context.Context, client *http.Client, source string, lineb []byte) (counts.Counts, *fatcat2.Release, error) {
	out := counts.Counts{}
	l := activity.GetLogger(ctx)

	var release *fatcat2.Release

	var doc dataciteDoc
	if err := json.Unmarshal(lineb, &doc); err != nil {
		return out, release, fmt.Errorf("datacite unmarshal: %w", err)
	}

	if reason := skipReason(&doc); reason != "" {
		l.Info("datacite: skipping record", "doi", doc.Attributes.DOI, "reason", reason)
		out.Releases.Skipped++
		return out, release, nil
	}

	release = dataciteToFc(&doc, source)

	foundID, err := fatcat2.LookupDoi(client, strings.ToLower(doc.Attributes.DOI))
	if err != nil {
		return out, release, fmt.Errorf("doi lookup failed for %q: %w", doc.Attributes.DOI, err)
	}

	if foundID != nil {
		l.Debug("datacite: found existing release", "doi", doc.Attributes.DOI, "release_id", foundID)
		r, err := fatcat2.GetRelease(client, *foundID)
		if err != nil {
			return out, release, fmt.Errorf("could not fetch existing release: %w", err)
		}
		release = &r
		out.Releases.Ignored++
	} else {
		// TODO: implement createRelease (container lookup, ES indexing) once
		// dataciteToFc is fully implemented.
		l.Info("datacite: would create release", "doi", doc.Attributes.DOI)
		out.Releases.Skipped++
	}

	return out, release, nil
}
