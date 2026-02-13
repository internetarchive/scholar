package convert

import (
	"errors"
	"fmt"
	"strings"

	"github.com/miku/scholkit/schema/fatcat"
	"github.com/miku/scholkit/schema/openalex"
)

var ErrMissingOpenAlexIdentifier = errors.New("missing openalex identifier")

func OpenAlexWorkToFatcatRelease(work *openalex.Work) (*fatcat.Release, error) {
	var release fatcat.Release
	if work.IDs.Openalex == "" {
		return nil, ErrMissingOpenAlexIdentifier
	}
	release.ID = fmt.Sprintf("openalex-%s", hashString(work.IDs.Openalex))
	release.Source = "openalex"
	// Set title
	release.Title = work.Title
	// Set contributors
	for _, authorship := range work.Authorships {
		release.Contribs = append(release.Contribs, fatcat.Contrib{
			GivenName: authorship.RawAuthorName,
			Role:      authorship.AuthorPosition,
		})
	}
	// identifiers
	release.ExtIDs.DOI = cleanDOI(work.DOI)
	release.ExtIDs.Arxiv = work.IDs.Arxiv
	release.ExtIDs.PMID = strings.Replace(work.IDs.PMID, "https://pubmed.ncbi.nlm.nih.gov/", "", 1)
	release.ExtIDs.PMCID = strings.Replace(work.IDs.PMCID, "https://www.ncbi.nlm.nih.gov/pmc/articles/", "", 1)
	release.ExtIDs.OpenAlex = strings.Replace(work.IDs.Openalex, "https://openalex.org/", "", 1)
	if work.IDs.Mag != 0 {
		release.ExtIDs.MAG = fmt.Sprintf("%d", work.IDs.Mag)
	}
	release.Volume = work.Biblio.Volume
	release.Issue = work.Biblio.Issue
	// Set release date
	release.ReleaseDate = work.PublicationDate
	release.ReleaseYear = work.PublicationYear
	release.Language = work.Language
	release.Pages = work.Pages()
	release.Publisher = work.PrimaryLocation.Source.Publisher
	release.Extra.OpenAlex.OpenAccess.AnyRepositoryHasFulltext = work.OpenAccess.AnyRepositoryHasFulltext
	release.Extra.OpenAlex.OpenAccess.IsOa = work.OpenAccess.IsOa
	release.Extra.OpenAlex.OpenAccess.OaStatus = work.OpenAccess.OaStatus
	release.Extra.OpenAlex.OpenAccess.OaUrl = work.OpenAccess.OaUrl
	release.Extra.OpenAlex.PdfUrls = work.PdfUrls()
	return &release, nil
}
