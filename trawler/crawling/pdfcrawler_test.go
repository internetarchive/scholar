package crawling

import (
	"bytes"
	"embed"
	"testing"
)

//go:embed htmlsamples/*.html
var samples embed.FS

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

func Test_findPDFLink(t *testing.T) {
	crawler := PDFCrawler{}
	cs := []struct {
		Name              string
		HtmlPath          string
		Url               string
		ExpectedURL       string
		ExpectedTechnique string
		Err               error
	}{
		{
			Name:              "revistas",
			HtmlPath:          "revistas.html",
			Url:               "https://www.revistas.unam.mx/index.php/rep/article/view/35503/32336",
			ExpectedURL:       "https://www.revistas.unam.mx/index.php/rep/article/download/35503/32336/85134",
			ExpectedTechnique: "jspdfurl",
		},
	}

	for _, c := range cs {
		t.Run(c.Name, func(t *testing.T) {
			bs, err := samples.ReadFile("htmlsamples/" + c.HtmlPath)
			if err != nil {
				panic(err)
			}

			result, err := crawler.findPDFLink(c.Url, bytes.NewReader(bs))
			if err != nil {
				if c.Err == nil {
					t.Errorf("%s: did not expect error but got %s", c.Name, err.Error())
				} else if c.Err.Error() != err.Error() {
					t.Errorf("%s: expected error '%s', got error '%s'", c.Name, c.Err, err)
				}
				return
			}

			if c.Err != nil {
				t.Errorf("%s: expected error but saw none", c.Name)
				return
			}

			if result.Technique != c.ExpectedTechnique {
				t.Errorf("%s: expected technique '%s', got '%s'",
					c.Name, c.ExpectedTechnique, result.Technique)
			}

			if result.URL != c.ExpectedURL {
				t.Errorf("%s: expected url '%s', got '%s'",
					c.Name, c.ExpectedURL, result.URL)
			}
		})
	}
}
