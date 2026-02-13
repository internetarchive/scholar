package convert

import (
	"fmt"
	"strconv"

	"github.com/miku/scholkit/schema/dblp"
	"github.com/miku/scholkit/schema/fatcat"
)

func DBLPArticleToFatcatRelease(doc *dblp.Article) (*fatcat.Release, error) {
	var rel fatcat.Release
	rel.ID = fmt.Sprintf("dblp-%s", hashString(doc.Key)) // dblp-sha1(key)
	rel.Source = "dblp"
	rel.ExtIDs = fatcat.ExtID{
		DBLP: doc.Key,
		DOI:  doc.DOI(),
	}
	rel.Title = doc.Title.Text
	if v, err := strconv.Atoi(doc.Year); err == nil {
		rel.ReleaseYear = int64(v)
	}
	rel.Volume = doc.Volume
	rel.Publisher = doc.Publisher
	rel.Pages = doc.Pages
	for _, ee := range doc.Ee {
		var edition = struct {
			Type string `json:"type,omitempty"`
			Text string `json:"text,omitempty"`
		}{
			Type: ee.Type,
			Text: ee.Text,
		}
		rel.Extra.DBLP.EE = append(rel.Extra.DBLP.EE, edition)
	}
	// Other fields mapping here...
	return &rel, nil
}
