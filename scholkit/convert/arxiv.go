package convert

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/miku/scholkit/schema/arxiv"
	"github.com/miku/scholkit/schema/fatcat"
)

// Consider moving abstract processing to a separate function
func processAbstracts(descriptions []string) []fatcat.Abstract {
	abstracts := make([]fatcat.Abstract, 0, len(descriptions))
	for _, desc := range descriptions {
		if desc = strings.TrimSpace(desc); desc != "" {
			abstracts = append(abstracts, fatcat.Abstract{
				Content:  desc,
				Mimetype: "text/plain",
				SHA1:     hashString(desc),
			})
		}
	}
	return abstracts
}

func processReleaseDate(dateStamp string) string {
	if dateStamp == "" {
		return ""
	}
	t, err := time.Parse("2006-01-02", dateStamp)
	if err != nil {
		log.Printf("arxiv: could not parse date: %v", dateStamp)
	}
	return t.Format("2006-01-02")
}

func processContributors(creators []string) []fatcat.Contrib {
	contribs := make([]fatcat.Contrib, 0, len(creators))
	for _, creator := range creators {
		contribs = append(contribs, fatcat.Contrib{
			RawName: creator,
			Role:    "author",
		})
	}
	return contribs
}

// ArxivRecordToFatcatRelease converts an arXiv record to a Fatcat release.  It
// preserves essential metadata including contributors, identifiers, and
// abstracts.  Returns an error if the input record is nil or if required
// fields are missing.
func ArxivRecordToFatcatRelease(record *arxiv.Record) (*fatcat.Release, error) {
	if record == nil {
		return nil, fmt.Errorf("arxiv record cannot be nil")
	}
	// Release metadata
	rel := fatcat.Release{
		ID: fmt.Sprintf("arxiv-%s", hashString(record.ID())),
		ExtIDs: fatcat.ExtID{
			DOI:   record.DOI(),
			OAI:   record.Header.Identifier,
			Arxiv: record.ID(),
		},
		Source:   "arxiv",
		Title:    record.Metadata.Dc.Title,
		Language: record.Metadata.Dc.Language,
	}
	rel.Contribs = processContributors(record.Metadata.Dc.Creator)
	rel.ReleaseDate = processReleaseDate(record.Header.Datestamp)
	rel.Abstracts = processAbstracts(record.Metadata.Dc.Description)
	rel.Extra.Arxiv.Subjects = record.Metadata.Dc.Subject
	rel.Extra.OAI.SetSpec = record.Header.SetSpec
	return &rel, nil
}
