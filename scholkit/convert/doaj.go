package convert

import (
	"errors"
	"fmt"
	"strings"

	"github.com/miku/scholkit/schema/doaj"
	"github.com/miku/scholkit/schema/fatcat"
)

var (
	ErrMissingDOAJIdentifier = errors.New("missing DOAJ ID")
	ErrNilRecord             = errors.New("nil DOAJ record")
)

// NameParts represents the parsed components of a person's name
type NameParts struct {
	GivenName string
	Surname   string
}

// parseFullName splits a full name into given name and surname
// It handles edge cases and provides more reliable name parsing
func parseFullName(fullName string) NameParts {
	names := strings.Fields(fullName)
	if len(names) == 0 {
		return NameParts{}
	}

	if len(names) == 1 {
		return NameParts{Surname: names[0]}
	}

	return NameParts{
		GivenName: strings.Join(names[:len(names)-1], " "),
		Surname:   names[len(names)-1],
	}
}

// DOAJRecordToFatcatRelease converts a DOAJ record to a Fatcat release
// It includes validation and more robust error handling
func DOAJRecordToFatcatRelease(record *doaj.Record) (*fatcat.Release, error) {
	if record == nil {
		return nil, ErrNilRecord
	}
	id := record.ID()
	if id == "" {
		return nil, ErrMissingDOAJIdentifier
	}
	release := &fatcat.Release{
		ID:          fmt.Sprintf("doaj-%s", id),
		Source:      "doaj",
		Title:       record.Metadata.Dc.Title,
		ReleaseDate: record.Metadata.Dc.Date,
		Ident:       id,
		ExtIDs: fatcat.ExtID{
			OAI:  record.Header.Identifier,
			DOI:  record.DOI(),
			DOAJ: id,
		},
	}
	// Handle contributors
	release.Contribs = make([]fatcat.Contrib, 0, len(record.Metadata.Dc.Creator))
	for _, creator := range record.Metadata.Dc.Creator {
		if creator.Text == "" {
			continue // Skip empty creator names
		}
		nameParts := parseFullName(creator.Text)
		release.Contribs = append(release.Contribs, fatcat.Contrib{
			GivenName: nameParts.GivenName,
			Surname:   nameParts.Surname,
		})
	}
	// Set alternative URL if available
	if url := record.URL(); url != "" {
		release.Extra.Crossref.AlternativeId = []string{url}
	}
	return release, nil
}
