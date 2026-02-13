package convert

import (
	"errors"
	"fmt"

	"github.com/miku/scholkit/schema/fatcat"
	"github.com/miku/scholkit/schema/pubmed"
)

var ErrMissingPMID = errors.New("missing pmid")

func PubmedArticleToFatcatRelease(doc *pubmed.Article) (*fatcat.Release, error) {
	var release fatcat.Release
	if doc.MedlineCitation.PMID.Text == "" {
		return nil, ErrMissingPMID
	}
	release.ID = fmt.Sprintf("pubmed-%s", hashString(doc.MedlineCitation.PMID.Text))
	release.Source = "pubmed"
	// Set title
	release.Title = doc.MedlineCitation.Article.ArticleTitle.Text
	// Set contributors
	for _, author := range doc.MedlineCitation.Article.AuthorList.Author {
		release.Contribs = append(release.Contribs, fatcat.Contrib{
			GivenName: author.ForeName,
			Surname:   author.LastName,
			RawName:   fmt.Sprintf("%s %s", author.ForeName, author.LastName),
		})
	}
	// Set DOI and other ids
	for _, articleID := range doc.PubmedData.ArticleIdList.ArticleId {
		switch articleID.IdType {
		case "doi":
			release.ExtIDs.DOI = articleID.Text
		case "pubmed":
			release.ExtIDs.PMID = articleID.Text
		case "pmc":
			release.ExtIDs.PMCID = articleID.Text
		case "mid":
			release.ExtIDs.MID = articleID.Text
		case "pii":
			release.ExtIDs.PII = articleID.Text
		}
	}
	// Set release date
	release.ReleaseDate = doc.ReleaseDate()
	// Set ident
	release.ExtIDs.PMID = doc.MedlineCitation.PMID.Text
	return &release, nil
}
