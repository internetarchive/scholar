package convert

import (
	"errors"
	"fmt"
	"strings"

	"github.com/miku/scholkit/schema/fatcat"
	"github.com/miku/scholkit/schema/oaiscrape"
)

var (
	ErrOaiDeleted      = errors.New("oai deleted record")
	ErrOaiMissingTitle = errors.New("oai missing title")
)

func OaiRecordToFatcatRelease(record *oaiscrape.Record) (*fatcat.Release, error) {
	if record.Header.Status == "deleted" {
		return nil, ErrOaiDeleted
	}
	var release fatcat.Release
	release.ID = fmt.Sprintf("oaiscrape-%s", hashString(record.Header.Identifier))
	release.Source = "oaiscrape"
	var dc = record.Metadata.Dc
	// Set title
	if dc.Title == "" {
		return nil, ErrOaiMissingTitle
	}
	release.Title = strings.TrimSpace(dc.Title)
	// Set contributor
	for _, creator := range dc.Creator {
		release.Contribs = append(release.Contribs, fatcat.Contrib{
			RawName: strings.TrimSpace(creator),
		})
	}
	// Set DOI
	release.ExtIDs.DOI = record.DOI()
	if u := record.URL(); len(u) != 0 {
		release.Extra.OAI.URL = u
	}
	release.ExtIDs.OAI = record.Header.Identifier
	release.Language = dc.Language
	// Set release date
	if len(dc.Date) > 0 {
		release.ReleaseDate = dc.Date[0]
	}
	release.Extra.OAI.SetSpec = record.Header.SetSpec
	return &release, nil

}

func OaiScrapeToFatcatRelease(doc *oaiscrape.Document) (*fatcat.Release, error) {
	var release fatcat.Release
	release.ID = fmt.Sprintf("oaiscrape-%s", hashString(doc.OAI))
	release.Source = "oaiscrape"
	// Set title
	if len(doc.Titles) > 0 {
		release.Title = doc.Titles[0]
	}
	// Set contributors
	for _, creator := range doc.Creators {
		names := strings.Split(creator, " ")
		var givenName, surname string
		if len(names) > 0 {
			surname = names[len(names)-1]
		}
		if len(names) > 1 {
			givenName = strings.Join(names[:len(names)-1], " ")
		}
		release.Contribs = append(release.Contribs, fatcat.Contrib{
			GivenName: givenName,
			Surname:   surname,
		})
	}
	// Set DOI
	release.ExtIDs.DOI = doc.DOI()
	release.ExtIDs.OAI = doc.OAI
	// Set release date
	release.ReleaseDate = doc.Datestamp
	release.Extra.OAI.SetSpec = doc.Sets
	return &release, nil
}
