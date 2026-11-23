package periodic

import (
	"crypto/sha1"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"git.archive.org/webgroup/scholar/trawler/cleaning"
	"git.archive.org/webgroup/scholar/trawler/fatcat2"
	"github.com/google/uuid"
	"github.com/miku/grobidclient/tei"
)

// grobidToRelease converts what metadata we extract from a PDF into a fatcat2
// release. This is currently unused as the quality of this release is stubby
// and at this time we aren't handling release updates in the daily crawl. If
// the daily crawl is modified to notice when a release could benefit from the
// addition of new metadata from an upstream source we can make use of this
// function when ingesting CDX.
func grobidToRelease(client *http.Client, source string, gdoc *tei.GrobidDocument) (fatcat2.Release, error) {
	rid, err := uuid.NewV7()
	if err != nil {
		return fatcat2.Release{}, fmt.Errorf("failed making uuid: %w", err)
	}

	out := fatcat2.Release{
		ID:          rid,
		Contribs:    []fatcat2.ReleaseContrib{},
		ExternalIDs: []fatcat2.ExternalID{},
		Extra:       map[string]any{},
		Language:    cleaning.NormalizeLanguage(gdoc.LanguageCode),
		Publisher:   gdoc.Header.Publisher,
		Volume:      gdoc.Header.Volume,
		Pages:       gdoc.Header.Pages,
		Issue:       gdoc.Header.Issue,
		Title:       gdoc.Header.Title,
		Type:        "article-journal",
		Source:      source,
	}
	if gdoc.Header.DOI != "" {
		out.ExternalIDs = append(out.ExternalIDs, fatcat2.ExternalID{
			Type: "doi", Value: gdoc.Header.DOI,
		})
	}
	if gdoc.Header.PMID != "" {
		out.ExternalIDs = append(out.ExternalIDs, fatcat2.ExternalID{
			Type: "pmid", Value: gdoc.Header.PMID,
		})
	}
	if gdoc.Header.PMCID != "" {
		out.ExternalIDs = append(out.ExternalIDs, fatcat2.ExternalID{
			Type: "PMCID", Value: gdoc.Header.PMCID,
		})
	}
	if gdoc.Header.ArxivID != "" {
		out.ExternalIDs = append(out.ExternalIDs, fatcat2.ExternalID{
			Type: "arxiv", Value: gdoc.Header.ArxivID,
		})
		out.Stage = "submitted"
	}

	if out.Stage == "" {
		out.Stage = "published"
	}

	// TODO should i handle ISSN in header?

	if len(gdoc.Header.Date) == 10 {
		d, err := time.Parse("2006-01-02", gdoc.Header.Date)
		if err == nil {
			rd := fatcat2.ReleaseDate(d)
			out.ReleaseDate = &rd
		}
	} else if len(gdoc.Header.Date) > 4 {
		yearPrefixRe := regexp.MustCompile("^[0-9]{4}-")
		if yearPrefixRe.MatchString(gdoc.Header.Date) {
			year, err := strconv.Atoi(gdoc.Header.Date[0:4])
			if err == nil {
				out.ReleaseYear = year
			}
		}
	}

	out.Extra["raw_date"] = gdoc.Header.Date

	for i, author := range gdoc.Header.Authors {
		var aid *uuid.UUID
		var err error
		if author.ORCID != "" {
			aid, err = fatcat2.LookupOrcid(client, author.ORCID)
			if err != nil {
				return out, fmt.Errorf("failed to look up author '%s': %w", author.ORCID, err)
			}
			if aid == nil {
				creator := fatcat2.Creator{
					DisplayName: author.FullName,
					GivenName:   author.GivenName,
					Surname:     author.Surname,
					Source:      source,
					Orcid:       cleaning.NormalizeOrcid(author.ORCID),
				}
				aid, err = fatcat2.CreateCreator(client, &creator)
				if err != nil {
					return out, fmt.Errorf("could not create creator '%s': %w", creator.Orcid, err)
				}
				creator.ID = *aid
			}
		}
		rawAffiliation := author.Affiliation.Institution
		if author.Affiliation.Department != "" {
			rawAffiliation += " " + author.Affiliation.Department
		}
		contrib := fatcat2.ReleaseContrib{
			ReleaseID:      &out.ID,
			CreatorID:      aid,
			RawName:        author.FullName,
			GivenName:      author.GivenName,
			Surname:        author.Surname,
			Position:       new(i),
			Role:           "author",
			RawAffiliation: rawAffiliation,
		}
		out.Contribs = append(out.Contribs, contrib)
	}

	if gdoc.Abstract != "" {
		abs := cleaning.CleanString(cleaning.DeTag(gdoc.Abstract))
		if len(abs) > cleaning.MinAbstractLength {
			h := sha1.Sum([]byte(abs))
			out.Abstracts = append(out.Abstracts, fatcat2.Abstract{
				MIMEType: "application/xml+jats",
				Content:  abs,
				Language: cleaning.NormalizeLanguage(gdoc.LanguageCode),
				SHA1:     fmt.Sprintf("%x", h),
			})
		}
	}

	for _, citation := range gdoc.Citations {
		rawRef := fatcat2.RawRef{
			Index:   citation.Index,
			Locator: citation.FirstPage,
			Title:   citation.Title,
			Extra:   map[string]any{},
		}
		if len(citation.Date) >= 4 {
			year, _ := strconv.Atoi(citation.Date[0:4])
			rawRef.Year = year
		}

		rawRef.ContainerName = citation.Journal

		if citation.Unstructured != "" {
			rawRef.Extra["unstructured"] = citation.Unstructured
		}

		authorNames := []string{}
		for _, author := range citation.Authors {
			authorNames = append(authorNames, author.FullName)
		}
		if len(authorNames) > 0 {
			rawRef.Extra["authors"] = authorNames
		}

		if citation.Volume != "" {
			rawRef.Extra["volume"] = citation.Volume
		}

		if citation.Pages != "" {
			rawRef.Extra["pages"] = citation.Pages
		}

		if citation.LastPage != "" {
			rawRef.Extra["last_page"] = citation.LastPage
		}
	}

	out.Extra["grobid_version"] = gdoc.GrobidVersion
	if gdoc.Annex != "" {
		out.Extra["annex"] = gdoc.Annex
	}

	// grobid doesn't seem to try and extract license information :(

	// TODO can anything be done about container?

	return out, nil
}
