package convert

import (
	"fmt"
	"time"

	"github.com/internetarchive/scholar/scholkit/schema/datacite"
	"github.com/internetarchive/scholar/scholkit/schema/fatcat"
)

func DataCiteToFatcatRelease(doc *datacite.Document) (*fatcat.Release, error) {
	var release fatcat.Release
	release.ID = fmt.Sprintf("datacite-%s", hashString(doc.Attributes.DOI)) // datacite-sha1(DOI)
	release.Source = "datacite"
	// Set title
	if len(doc.Attributes.Titles) > 0 {
		release.Title = doc.Attributes.Titles[0].Title
	}
	// Set contributors
	for _, contributor := range doc.Attributes.Contributors {
		release.Contribs = append(release.Contribs, fatcat.Contrib{
			GivenName: contributor.GivenName,
			Surname:   contributor.FamilyName,
			RawName:   contributor.GivenName + " " + contributor.FamilyName,
			Role:      contributor.ContributorType,
		})
	}
	// Set DOI
	release.ExtIDs.DOI = doc.Attributes.DOI
	// Set release date
	if doc.Attributes.Published != "" {
		t, err := time.Parse(time.RFC3339, doc.Attributes.Published)
		if err == nil {
			release.ReleaseDate = t.Format("2006-01-02")
		}
	}
	return &release, nil
}
