package oaiscrape

import (
	"encoding/xml"
	"regexp"
	"strings"
)

// TODO: move this out
var issnPattern = regexp.MustCompile(`[0-9]{4,4}-[0-9X]{4,4}`)

// doiPreceding are some possible strings preceding a DOI
var doiPreceding = []string{
	"doi:",
	"http://doi.org/",
	"http://dx.doi.org/",
	"https://doi.org/",
	"https://dx.doi.org/",
}

type Record struct {
	XMLName xml.Name `xml:"record"`
	Text    string   `xml:",chardata"`
	Xmlns   string   `xml:"xmlns,attr"`
	Header  struct {
		Text       string   `xml:",chardata"`
		Status     string   `xml:"status,attr"`
		Identifier string   `xml:"identifier"` // oai:arXiv.org:1007.4032, ...
		Datestamp  string   `xml:"datestamp"`  // 2011-04-12, 2011-04-04, 2...
		SetSpec    []string `xml:"setSpec"`    // physics:physics, physics:...
	} `xml:"header"`
	Metadata struct {
		Text string `xml:",chardata"`
		Dc   struct {
			Text           string   `xml:",chardata"`
			OaiDc          string   `xml:"oai_dc,attr"`
			Dc             string   `xml:"dc,attr"`
			Xsi            string   `xml:"xsi,attr"`
			SchemaLocation string   `xml:"schemaLocation,attr"`
			Title          string   `xml:"title"`       // Cascading of Liquid Cryst...
			Creator        []string `xml:"creator"`     // Dawson, Nathan J., Kuzyk,...
			Subject        []string `xml:"subject"`     // Physics - Optics, Quantum...
			Description    []string `xml:"description"` // Photomechanical actuation...
			Date           []string `xml:"date"`        // 2010-07-22, 2010-12-08, 2...
			Type           string   `xml:"type"`        // text, text, text, text, t...
			Identifier     []string `xml:"identifier"`  // http://arxiv.org/abs/1007...
			Language       string   `xml:"language"`    // ru, pt, pt, fr, ru, fr, r...
		} `xml:"dc"`
	} `xml:"metadata"`
	About string `xml:"about"`
}

// URL returns the first URL found.
func (record *Record) URL() (result []string) {
	for _, f := range record.Metadata.Dc.Identifier {
		if strings.HasPrefix(f, "http") {
			result = append(result, strings.TrimSpace(f))
		}
	}
	return result
}

func (record *Record) DOI() string {
	for _, f := range record.Metadata.Dc.Identifier {
		if strings.HasPrefix(f, "10.") {
			return f
		}
		for _, p := range doiPreceding {
			if strings.HasPrefix(f, p) {
				return strings.TrimSpace(strings.Replace(f, p, "", 1))
			}
		}
	}
	return ""
}

// Document is a minimal JSON document already converted from XML through other means.
type Document struct {
	Creators     []string `json:"creators"`
	Datestamp    string   `json:"datestamp"`
	Descriptions []string `json:"descriptions"`
	IDs          []string `json:"ids"`
	Languages    []string `json:"languages"`
	OAI          string   `json:"oai"`
	Rights       []string `json:"rights"`
	Sets         []string `json:"sets"`
	Titles       []string `json:"titles"`
	Types        []string `json:"types"`
	URLs         []string `json:"urls"`
}

// DOI returns the first DOI, will attempt slight guessing at various strings.
func (doc *Document) DOI() string {
	for _, f := range doc.IDs {
		if strings.HasPrefix(f, "10.") {
			return f
		}
		for _, p := range doiPreceding {
			if strings.HasPrefix(f, p) {
				return strings.Replace(f, p, "", 1)
			}
		}
	}
	return ""
}

// URL returns the first URL found.
func (doc *Document) URL() string {
	for _, f := range doc.IDs {
		if strings.HasPrefix(f, "http") {
			return f
		}
	}
	return ""
}

// ISSN returns the first ISSN found.
func (doc *Document) ISSN() string {
	for _, f := range doc.IDs {
		if issnPattern.MatchString(f) {
			return f
		}
	}
	return ""
}
