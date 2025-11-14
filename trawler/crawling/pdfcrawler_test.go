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
		name     string
		url      string
		expected string
	}{
		{
			name:     "arxiv",
			url:      "https://arxiv.org/pdf/1234567.pdf",
			expected: "https://arxiv.org/pdf/1234567",
		},
		{
			name:     "wiley",
			url:      "https://onlinelibrary.wiley.com/doi/10.foobar/baz123",
			expected: "https://onlinelibrary.wiley.com/doi/pdf/10.foobar/baz123",
		},
		{
			name:     "sagepub",
			url:      "https://journals.sagepub.com/doi/10.123/wahoo",
			expected: "https://journals.sagepub.com/doi/10.123/wahoo?download=true",
		},
		{
			name:     "sagepub (direct pdf)",
			url:      "https://journals.sagepub.com/doi/pdf/10.123/wahoo",
			expected: "https://journals.sagepub.com/doi/pdf/10.123/wahoo?download=true",
		},
		{
			name:     "acs.org",
			url:      "https://pubs.acs.org/doi/10.123/foobar#",
			expected: "https://pubs.acs.org/doi/pdf/10.123/foobar?ref=article_openPDF",
		},
		{
			name:     "jcancer html",
			url:      "https://www.jcancer.org/v16p1684.html",
			expected: "https://www.jcancer.org/v16p1684.pdf",
		},
		{
			name:     "jcancer htm",
			url:      "https://www.jcancer.org/v16p1684.html",
			expected: "https://www.jcancer.org/v16p1684.pdf",
		},
		{
			name:     "tandfonline",
			url:      "https://www.tandfonline.com/doi/full/10.1080/19491247.2019.1682234",
			expected: "https://www.tandfonline.com/doi/pdf/10.1080/19491247.2019.1682234",
		},
		{
			name:     "icsa-speech",
			url:      "https://www.isca-archive.org/interspeech_2025/pu25_interspeech.html",
			expected: "https://www.isca-archive.org/interspeech_2025/pu25_interspeech.pdf",
		},
		{
			name:     "uchicago",
			url:      "https://www.journals.uchicago.edu/doi/10.14318/hau1.1.008",
			expected: "https://www.journals.uchicago.edu/doi/epdf/10.14318/hau1.1.008",
		},
		{
			name:     "integrityresjournals",
			url:      "https://integrityresjournals.org/journal/JBBD/article-abstract/291855622",
			expected: "https://integrityresjournals.org/journal/JBBD/article-full-text-pdf/291855622",
		},
	}

	for _, c := range cs {
		crawler := PDFCrawler{}
		t.Run(c.name, func(t *testing.T) {
			out := crawler.maybeRewrite(c.url)
			if out != c.expected {
				t.Errorf("%s: expected %s, got %s", c.name, c.expected, out)
			}
		})
	}
}

func Test_findPDFLink(t *testing.T) {
	crawler := PDFCrawler{}
	cs := []struct {
		name              string
		htmlPath          string
		url               string
		expectedURL       string
		expectedTechnique string
		err               error
		hop               bool
	}{
		{
			name:              "revistas",
			htmlPath:          "revistas.html",
			url:               "https://www.revistas.unam.mx/index.php/rep/article/view/35503/32336",
			expectedURL:       "https://www.revistas.unam.mx/index.php/rep/article/download/35503/32336/85134",
			expectedTechnique: "jspdfurl",
		},
		{
			name:              "elifesciences",
			htmlPath:          "elifesciences.html",
			url:               "https://elifesciences.org/articles/59841",
			expectedURL:       "https://elifesciences.org/download/aHR0cHM6Ly9jZG4uZWxpZmVzY2llbmNlcy5vcmcvYXJ0aWNsZXMvNTk4NDEvZWxpZmUtNTk4NDEtdjEucGRmP2Nhbm9uaWNhbFVyaT1odHRwczovL2VsaWZlc2NpZW5jZXMub3JnL2FydGljbGVzLzU5ODQx/elife-59841-v1.pdf?_hash=%2BEZ2CH%2FifGiXeDp5cSOT92ExFSGAjdYcDH%2FlRlOLLE0%3D",
			expectedTechnique: "elifesciences",
		},
		{
			name:              "citation pdf url",
			htmlPath:          "unsw.html",
			url:               "https://unsworks.unsw.edu.au/entities/publication/fd08fc25-48dc-40bc-b673-deb232f31faa",
			expectedURL:       "https://unsworks.unsw.edu.au/bitstreams/474505c1-89eb-407c-9793-fd4ffeabd6a2/download",
			expectedTechnique: "citation_pdf_url",
		},
		{
			name:              "bepress citation pdf url",
			htmlPath:          "aisnet.html",
			url:               "https://aisel.aisnet.org/sjis/vol25/iss2/1/",
			expectedURL:       "https://aisel.aisnet.org/cgi/viewcontent.cgi?article=1298&context=sjis",
			expectedTechnique: "bepress_citation_pdf_url",
		},
		{
			name:              "eprints document url",
			htmlPath:          "utas.html",
			url:               "https://eprints.utas.edu.au/16016/",
			expectedURL:       "https://eprints.utas.edu.au/16016/1/wilson-tasmanian-lichens-1892.pdf",
			expectedTechnique: "eprints-document_url",
		},
		{
			name:              "a.pdf style link",
			htmlPath:          "eurosurveillance.org.html",
			url:               "https://www.eurosurveillance.org/content/10.2807/1560-7917.ES.2025.30.43.2500793",
			expectedURL:       "https://www.eurosurveillance.org/deliver/fulltext/eurosurveillance/30/43/eurosurv-30-43-3.pdf?itemId=%2Fcontent%2F10.2807%2F1560-7917.ES.2025.30.43.2500793&mimeType=pdf&containerItemId=content/eurosurveillance",
			expectedTechnique: "a.pdf_link",
		},
		{
			name:              "pdf embed",
			htmlPath:          "jass.html",
			url:               "https://jasstudies.com/DergiTamDetay.aspx?ID=3401",
			expectedURL:       "https://jasstudies.com/files/jass_makaleler/1359848334_33-Okt.%20Yasemin%20KARADEM%C4%B0R.pdf",
			expectedTechnique: "pdf-embed-type",
		},
		{
			name:              "downloadPdf class",
			htmlPath:          "degruyter.html",
			url:               "https://www.degruyterbrill.com/document/doi/10.1515/zaw-2021-0001/html",
			expectedURL:       "https://www.degruyterbrill.com/document/doi/10.1515/zaw-2021-0001/pdf?licenseType=open-access",
			expectedTechnique: "downloadPdf",
		},
		{
			name:              "unicamp",
			htmlPath:          "unicamp.html",
			url:               "http://www.repositorio.unicamp.br/acervo/detalhe/1509801",
			expectedURL:       "http://www.repositorio.unicamp.br/Busca/Download?codigoArquivo=592674&tipoMidia=0",
			expectedTechnique: "unicamp",
		},
		{
			name:              "ingenta connet",
			htmlPath:          "ingenta.html",
			url:               "https://www.ingentaconnect.com/content/ista/sst/2021/00000049/00000001/art00007",
			expectedURL:       "https://www.ingentaconnect.com/search/download;jsessionid=4gcfk31kgili3.x-ic-live-03?pub=infobike%3a%2f%2fista%2fsst%2f2021%2f00000049%2f00000001%2fart00007&mimetype=application%2fpdf&host=https://www.ingentaconnect.com",
			expectedTechnique: "ingenta",
		},
		{
			name:              "wroc.pl",
			htmlPath:          "wroc.html",
			url:               "https://dbc.wroc.pl/dlibra/docmetadata?showContent=true&id=41031",
			expectedURL:       "https://dbc.wroc.pl//Content/41031/PDF/Raport_M_Adamska_popr.pdf",
			expectedTechnique: "dlibra-iframe",
		},
		{
			name:              "research.tue.nl hack+rewrite",
			htmlPath:          "research.tue.nl.html",
			url:               "https://research.tue.nl/files/1950518/Metis209517.pdf",
			expectedURL:       "https://pure.tue.nl/ws/portalfiles/portal/1950518/Metis209517.pdf",
			expectedTechnique: "research.tue.nl",
		},
		{
			name:              "hal -> arxiv link",
			htmlPath:          "hal.html",
			url:               "https://hal.science/hal-00744951",
			expectedURL:       "http://arxiv.org/pdf/1204.4004",
			expectedTechnique: "hal",
		},
		{
			name:              "invenio record path",
			htmlPath:          "desy.de.html",
			url:               "https://bib-pubdb1.desy.de/record/416556",
			expectedURL:       "https://bib-pubdb1.desy.de/record/416556/files/ILD-PHYS-PROC-2018-005.pdf",
			expectedTechnique: "invenio-record",
		},
		{
			name:              "unipi.it",
			htmlPath:          "unipi.it.html",
			url:               "https://etd.adm.unipi.it/theses/available/etd-05302014-183910/",
			expectedURL:       "https://etd.adm.unipi.it/theses/available/etd-05302014-183910/unrestricted/TESI_DEFINITIVA.pdf",
			expectedTechnique: "unipi.it",
		},
		{
			name:              "islandora",
			htmlPath:          "flvc.org.html",
			url:               "https://fau.digital.flvc.org/islandora/object/fau%3A9804",
			expectedURL:       "https://fau.digital.flvc.org/islandora/object/fau%3A9804/datastream/OBJ/download/Crossing_the_Rainbow_Bridge.pdf",
			expectedTechnique: "islandora",
		},
		{
			name:              "mycore receive",
			htmlPath:          "thueringen.html",
			url:               "https://www.db-thueringen.de/receive/dbt_mods_00005191",
			expectedURL:       "https://www.db-thueringen.de/servlets/MCRFileNodeServlet/dbt_derivate_00007860/2.%20Dissertation.pdf",
			expectedTechnique: "mycore-receive",
		},
		{
			name:              "digibis-media-link",
			htmlPath:          "gva.es.html",
			url:               "https://bivaldi.gva.es/es/consulta/registro.do?id=11740",
			expectedURL:       "https://bivaldi.gva.es/es/catalogo_imagenes/grupo.do?path=1023613",
			expectedTechnique: "digibis-media-link",
		},
		{
			name:              "repository.dri.ie",
			htmlPath:          "drie.ie.html",
			url:               "https://repository.dri.ie/catalog/q524zq043",
			expectedURL:       "https://repository.dri.ie/objects/q524zq043/files/q8120k889/download?type=surrogate",
			expectedTechnique: "dri.ie-download-link",
		},
		{
			name:              "e-manuscripta.ch",
			htmlPath:          "e-manuscripta.ch.html",
			url:               "https://www.e-manuscripta.ch/zut/doi/10.7891/e-manuscripta-112176",
			expectedURL:       "https://www.e-manuscripta.ch/zut/download/pdf/3189359",
			expectedTechnique: "e-manuscripta",
		},
		{
			// this could be a rewrite but I have a hunch this pattern extends across
			// multiple domains (based on the naming in the original code)
			name:              "ojs pdf download",
			htmlPath:          "karazin.ua.html",
			url:               "https://periodicals.karazin.ua/language_teaching/article/view/12543/11957",
			expectedURL:       "https://periodicals.karazin.ua/language_teaching/article/download/12543/11957/",
			expectedTechnique: "ojs-pdf-download",
		},
		{
			// this could be a rewrite but I have a hunch this pattern extends across
			// multiple domains (based on the naming in the original code)
			name:              "ojs pdf embed",
			htmlPath:          "ojs-embed.html",
			url:               "https://periodicals.karazin.ua/language_teaching/article/view/12543/11957",
			expectedURL:       "https://periodicals.karazin.ua/language_teaching/article/download/12543/11957/",
			expectedTechnique: "ojs-pdf-embed",
		},
		{
			name:              "scitemed",
			htmlPath:          "scitemed.com.html",
			url:               "https://scitemed.com/article/4294/scitemed-aohns-2024-00190",
			expectedURL:       "https://scitemed.com/upload/5730/4294/scitemed.aohns.2024.00190.pdf?t=1643",
			expectedTechnique: "scitemed",
		},
		{
			name:              "doaj access link",
			htmlPath:          "doaj.org.html",
			url:               "https://doaj.org/article/000253ec38074062bb23746c2a7d6eb2",
			expectedURL:       "https://doi.org/10.2903/j.efsa.2018.5222",
			expectedTechnique: "doaj-access-link",
			hop:               true,
		},
		{
			name:              "pdf embed, alt",
			htmlPath:          "arkat-usa.html",
			url:               "https://www.arkat-usa.org/browse-arkivoc/browse-arkivoc/ark.5550190.0006.913",
			expectedURL:       "https://www.arkat-usa.org/get-file/18673/",
			expectedTechnique: "pdf-embed-alt",
		},
	}

	for _, c := range cs {
		t.Run(c.name, func(t *testing.T) {
			bs, err := samples.ReadFile("htmlsamples/" + c.htmlPath)
			if err != nil {
				panic(err)
			}

			result, err := crawler.findNextLink(c.url, bytes.NewReader(bs))
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

			if result == nil {
				t.Errorf("%s: nil result", c.name)
				return
			}

			if result.Technique != c.expectedTechnique {
				t.Errorf("%s: expected technique '%s', got '%s'",
					c.name, c.expectedTechnique, result.Technique)
			}

			if result.URL != c.expectedURL {
				t.Errorf("%s: expected url '%s', got '%s'",
					c.name, c.expectedURL, result.URL)
			}

			if result.Hop != c.hop {
				t.Errorf("%s: expected hop '%v', got '%v'",
					c.name, c.hop, result.Hop)
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
		{
			name:        "relative url with dots",
			pageUrl:     "http://lol.cool/sure",
			pdfUrl:      "../pdf/123?ok=cool",
			expectedUrl: "http://lol.cool/pdf/123?ok=cool",
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
