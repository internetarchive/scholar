package openalex

// OpenAlex source import: turn OpenAlex source records
// (https://developers.openalex.org/api-entities/sources) into fatcat2
// containers, so that article importers resolving a container by ISSN-L find a
// journal OpenAlex knows about instead of dropping the link.
//
// Only containers that fatcat does not already have are created; see package
// containers.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"git.archive.org/webgroup/scholar/trawler/containers"
	"git.archive.org/webgroup/scholar/trawler/fatcat2"
	"git.archive.org/webgroup/scholar/trawler/issn"
)

// sourceRecord is the subset of an OpenAlex source record we use. The full
// record carries yearly counts, topics and the like, none of which belongs on
// a container.
type sourceRecord struct {
	ID                   string   `json:"id"`
	ISSNL                string   `json:"issn_l"`
	ISSN                 []string `json:"issn"`
	DisplayName          string   `json:"display_name"`
	HostOrganizationName string   `json:"host_organization_name"`
	HomepageURL          string   `json:"homepage_url"`
	CountryCode          string   `json:"country_code"`
	Type                 string   `json:"type"`
}

// parseSources reads OpenAlex source records, one JSON object per line. A
// day's partition is at most a few hundred thousand records, and only the
// fields above are kept, so holding them all is cheap; having every record up
// front is what lets an import resume from a record index.
func parseSources(r io.Reader) ([]sourceRecord, error) {
	dec := json.NewDecoder(r)
	out := []sourceRecord{}
	for {
		var rec sourceRecord
		err := dec.Decode(&rec)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("could not read record %d: %w", len(out)+1, err)
		}
		out = append(out, rec)
	}
	return out, nil
}

// containerTypes maps the OpenAlex source types we import to fatcat container
// types. The rest (repository, ebook platform, metadata, other) are not
// containers in fatcat's sense and are skipped.
var containerTypes = map[string]string{
	"journal":     "journal",
	"conference":  "conference",
	"book series": "book-series",
}

// openAlexID strips the URL prefix from an OpenAlex ID, leaving e.g. S123.
func openAlexID(id string) string {
	return strings.TrimPrefix(id, "https://openalex.org/")
}

// sourceISSNs is a record's ISSNs, normalized, plus the ISSN-L they resolve to.
type sourceISSNs struct {
	ISSNL string
	// ISSNs is every valid ISSN the record lists, in its order.
	ISSNs []string
	// LinkingISSNs is every distinct ISSN-L the record's ISSNs map to. Usually
	// one; either may be the one fatcat already has.
	LinkingISSNs []string
}

// resolveISSNs normalizes a record's ISSNs and works out its ISSN-L from our
// ISSN-L table.
//
// OpenAlex's own issn_l is not trusted on its own: it disagrees with the table
// for a handful of journals a day, and a journal too new for the table would
// get an ISSN-L that may later turn out to belong to a different group. It is
// tried first, though, so where it does agree with the table it decides
// between groups.
func resolveISSNs(rec sourceRecord) sourceISSNs {
	out := sourceISSNs{}
	for _, raw := range append([]string{rec.ISSNL}, rec.ISSN...) {
		if i := issn.Normalize(raw); i != "" && !slices.Contains(out.ISSNs, i) {
			out.ISSNs = append(out.ISSNs, i)
		}
	}
	out.LinkingISSNs = issn.LinkingISSNs(out.ISSNs...)
	if len(out.LinkingISSNs) > 0 {
		out.ISSNL = out.LinkingISSNs[0]
	}
	return out
}

// sourceSkipReason returns why a record cannot become a container, or empty
// string if it can.
//
// A record needs an ISSN-L from our table. OpenAlex lists ISSNs without saying
// which is print and which electronic, so unlike DOAJ's journals there is no
// honest way to file one that lacks an ISSN-L: it would be a container nothing
// could find.
func sourceSkipReason(rec sourceRecord, issns sourceISSNs) string {
	if _, ok := containerTypes[rec.Type]; !ok {
		return "unsupported-type"
	}
	if len(issns.ISSNs) == 0 {
		return "no-valid-issn"
	}
	if issns.ISSNL == "" {
		return "no-issnl"
	}
	if containers.CleanName(rec.DisplayName) == "" {
		return "empty-title"
	}
	return ""
}

// sourceToContainer builds the container we would create for a record.
//
// The ISSNs go in extra.openalex rather than issnp/issne, since OpenAlex does
// not say which is which.
func sourceToContainer(rec sourceRecord, issns sourceISSNs, source string) fatcat2.Container {
	oaExtra := map[string]any{}
	if id := openAlexID(rec.ID); id != "" {
		oaExtra["id"] = id
	}
	if len(issns.ISSNs) > 0 {
		oaExtra["issns"] = issns.ISSNs
	}
	extra := map[string]any{"openalex": oaExtra}
	if rec.HomepageURL != "" {
		extra["urls"] = []string{rec.HomepageURL}
	}
	if rec.CountryCode != "" {
		extra["country"] = strings.ToLower(rec.CountryCode)
	}

	return fatcat2.Container{
		Name:      containers.CleanName(rec.DisplayName),
		Type:      containerTypes[rec.Type],
		Publisher: containers.CleanName(rec.HostOrganizationName),
		ISSNL:     issns.ISSNL,
		Source:    source,
		Extra:     extra,
	}
}

// ToContainer implements containers.Row. Lookup is by every ISSN the record
// has, ISSN-Ls first.
func (rec sourceRecord) ToContainer(source string) (fatcat2.Container, []string, string) {
	issns := resolveISSNs(rec)
	if reason := sourceSkipReason(rec, issns); reason != "" {
		return fatcat2.Container{}, nil, reason
	}
	lookup := append(append([]string{}, issns.LinkingISSNs...), issns.ISSNs...)
	return sourceToContainer(rec, issns, source), lookup, ""
}

func (rec sourceRecord) LogValues() []any {
	return []any{"openalex", openAlexID(rec.ID)}
}
