package crawling

import "testing"

func Test_maybeRewrite(t *testing.T) {
	cs := []struct {
		Name     string
		Url      string
		Expected string
	}{
		{
			Name:     "arxiv",
			Url:      "https://arxiv.org/pdf/1234567.pdf",
			Expected: "https://arxiv.org/pdf/1234567",
		},
		{
			Name:     "wiley",
			Url:      "https://onlinelibrary.wiley.com/doi/10.foobar/baz123",
			Expected: "https://onlinelibrary.wiley.com/doi/pdf/10.foobar/baz123",
		},
		{
			Name:     "sagepub",
			Url:      "https://journals.sagepub.com/doi/10.123/wahoo",
			Expected: "https://journals.sagepub.com/doi/10.123/wahoo?download=true",
		},
		{
			Name:     "sagepub (direct pdf)",
			Url:      "https://journals.sagepub.com/doi/pdf/10.123/wahoo",
			Expected: "https://journals.sagepub.com/doi/pdf/10.123/wahoo?download=true",
		},
		{
			Name:     "acs.org",
			Url:      "https://pubs.acs.org/doi/10.123/foobar#",
			Expected: "https://pubs.acs.org/doi/pdf/10.123/foobar?ref=article_openPDF",
		},
	}

	for _, c := range cs {
		crawler := PDFCrawler{}
		t.Run(c.Name, func(t *testing.T) {
			out := crawler.maybeRewrite(c.Url)
			if out != c.Expected {
				t.Errorf("%s: expected %s, got %s", c.Name, c.Expected, out)
			}
		})
	}
}

func TestFindPDFLink(t *testing.T) {
	cs := []struct {
		Name              string
		HtmlPath          string
		ExpectedLink      string
		ExpectedTechnique string
	}{
		{
			Name:              "TODO",
			HtmlPath:          "TODO",
			ExpectedLink:      "TODO",
			ExpectedTechnique: "TODO",
		},
	}

	for _, c := range cs {
		t.Run(c.Name, func(t *testing.T) {
			// TODO
		})
	}
}
