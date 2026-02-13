package arxiv

import (
	"encoding/xml"
	"regexp"
	"strings"
)

const prefix = "oai:arXiv.org:"

// TODO: move this out
var issnPattern = regexp.MustCompile(`[0-9]{4,4}-[0-9X]{4,4}`)

// Record was generated 2024-03-15 14:27:46 by tir on k9 with zek 0.1.22.
type Record struct {
	XMLName xml.Name `xml:"record"`
	Text    string   `xml:",chardata"`
	Xmlns   string   `xml:"xmlns,attr"`
	Header  struct {
		Text       string   `xml:",chardata"`
		Status     string   `xml:"status,attr"`
		Identifier string   `xml:"identifier"` // oai:arXiv.org:0704.0004, ...
		Datestamp  string   `xml:"datestamp"`  // 2007-05-23, 2007-05-23, 2...
		SetSpec    []string `xml:"setSpec"`    // math, math, math, physics...
	} `xml:"header"`
	Metadata struct {
		Text string `xml:",chardata"`
		Dc   struct {
			Text           string   `xml:",chardata"`
			OaiDc          string   `xml:"oai_dc,attr"`
			Dc             string   `xml:"dc,attr"`
			Xsi            string   `xml:"xsi,attr"`
			SchemaLocation string   `xml:"schemaLocation,attr"`
			Title          string   `xml:"title"`       // A determinant of Stirling...
			Creator        []string `xml:"creator"`     // Callan, David, Ovchinniko...
			Subject        []string `xml:"subject"`     // Mathematics - Combinatori...
			Description    []string `xml:"description"` // We show that a determinan...
			Date           []string `xml:"date"`        // 2007-03-30, 2007-03-31, 2...
			Type           string   `xml:"type"`        // text, text, text, text, t...
			Identifier     []string `xml:"identifier"`  // http://arxiv.org/abs/0704...
			Language       string   `xml:"language"`    // fr, fr, de, fr, ru, it, p...
		} `xml:"dc"`
	} `xml:"metadata"`
	About string `xml:"about"`
}

func (r *Record) DOI() string {
	for _, f := range r.Metadata.Dc.Identifier {
		if strings.HasPrefix(f, "10.") {
			return f
		}
		if strings.HasPrefix(f, "doi:") {
			return strings.Replace(f, "doi:", "", 1)
		}
	}
	return ""
}

// URL returns the first URL found.
func (r *Record) URL() string {
	for _, f := range r.Metadata.Dc.Identifier {
		if strings.HasPrefix(f, "http") {
			return f
		}
	}
	return ""
}

func (r *Record) ID() string {
	return strings.Replace(r.Header.Identifier, prefix, "", 1)
}

func (r *Record) ISSN() string {
	for _, f := range r.Metadata.Dc.Identifier {
		if issnPattern.MatchString(f) {
			return f
		}
	}
	return ""
}
