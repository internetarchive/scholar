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
		{
			Name:     "jcancer html",
			Url:      "https://www.jcancer.org/v16p1684.html",
			Expected: "https://www.jcancer.org/v16p1684.pdf",
		},
		{
			Name:     "jcancer htm",
			Url:      "https://www.jcancer.org/v16p1684.html",
			Expected: "https://www.jcancer.org/v16p1684.pdf",
		},
		{
			Name:     "tandfonline",
			Url:      "https://www.tandfonline.com/doi/full/10.1080/19491247.2019.1682234",
			Expected: "https://www.tandfonline.com/doi/pdf/10.1080/19491247.2019.1682234",
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
		{
			Name:              "elifesciences",
			HtmlPath:          "elifesciences.html",
			Url:               "https://elifesciences.org/articles/59841",
			ExpectedURL:       "https://elifesciences.org/download/aHR0cHM6Ly9jZG4uZWxpZmVzY2llbmNlcy5vcmcvYXJ0aWNsZXMvNTk4NDEvZWxpZmUtNTk4NDEtdjEucGRmP2Nhbm9uaWNhbFVyaT1odHRwczovL2VsaWZlc2NpZW5jZXMub3JnL2FydGljbGVzLzU5ODQx/elife-59841-v1.pdf?_hash=%2BEZ2CH%2FifGiXeDp5cSOT92ExFSGAjdYcDH%2FlRlOLLE0%3D",
			ExpectedTechnique: "elifesciences",
		},
		{
			Name:              "citation pdf url",
			HtmlPath:          "unsw.html",
			Url:               "https://unsworks.unsw.edu.au/entities/publication/fd08fc25-48dc-40bc-b673-deb232f31faa",
			ExpectedURL:       "https://unsworks.unsw.edu.au/bitstreams/474505c1-89eb-407c-9793-fd4ffeabd6a2/download",
			ExpectedTechnique: "citation_pdf_url",
		},
		{
			Name:              "bepress citation pdf url",
			HtmlPath:          "aisnet.html",
			Url:               "https://aisel.aisnet.org/sjis/vol25/iss2/1/",
			ExpectedURL:       "https://aisel.aisnet.org/cgi/viewcontent.cgi?article=1298&context=sjis",
			ExpectedTechnique: "bepress_citation_pdf_url",
		},
		{
			Name:              "eprints document url",
			HtmlPath:          "utas.html",
			Url:               "https://eprints.utas.edu.au/16016/",
			ExpectedURL:       "https://eprints.utas.edu.au/16016/1/wilson-tasmanian-lichens-1892.pdf",
			ExpectedTechnique: "eprints-document_url",
		},
		{
			Name:              "a.pdf style link",
			HtmlPath:          "eurosurveillance.org.html",
			Url:               "https://www.eurosurveillance.org/content/10.2807/1560-7917.ES.2025.30.43.2500793",
			ExpectedURL:       "https://www.eurosurveillance.org/deliver/fulltext/eurosurveillance/30/43/eurosurv-30-43-3.pdf?itemId=%2Fcontent%2F10.2807%2F1560-7917.ES.2025.30.43.2500793&mimeType=pdf&containerItemId=content/eurosurveillance",
			ExpectedTechnique: "a.pdf_link",
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

func Test_absolutize(t *testing.T) {
	cs := []struct {
		name        string
		pageUrl     string
		pdfUrl      string
		expectedUrl string
		err         error
	}{
		{
			name:        "full pdf url",
			pageUrl:     "https://jill.valentine/squamous/landing",
			pdfUrl:      "https://claire.redfield/pdf/download?cool=1&hi=there",
			expectedUrl: "https://claire.redfield/pdf/download?cool=1&hi=there",
		},
		{
			name:        "relative pdf url",
			pageUrl:     "https://barry.burton/article/cool?ok=sure",
			pdfUrl:      "/download/pdf?why=not",
			expectedUrl: "https://barry.burton/download/pdf?why=not",
		},
		{
			name:        "schemaless pdf url",
			pageUrl:     "https://barry.burton/article/cool?ok=sure",
			pdfUrl:      "cool.com/download/pdf?why=not",
			expectedUrl: "https://cool.com/download/pdf?why=not",
		},
	}

	for _, c := range cs {
		t.Run(c.name, func(t *testing.T) {
			out, err := absolutize(c.pageUrl, c.pdfUrl)
			if err != nil {
				if c.err == nil {
					t.Errorf("%s: did not expect error but got %s", c.name, err.Error())
				} else if c.err.Error() != err.Error() {
					t.Errorf("%s: expected error '%s', got error '%s'", c.name, c.err, err)
				}
				return
			}

			if c.err != nil {
				t.Errorf("%s: expected error but saw none", c.name)
				return
			}

			if out != c.expectedUrl {
				t.Errorf("%s: expected %s, got %s", c.name, c.expectedUrl, out)
			}
		})
	}
}
