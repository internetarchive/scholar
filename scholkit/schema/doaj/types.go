package doaj

import (
	"encoding/xml"
	"regexp"
	"strings"
)

// TODO: move this out
var issnPattern = regexp.MustCompile(`[0-9]{4,4}-[0-9X]{4,4}`)

// Record was generated 2024-03-15 13:52:04 by tir on k9 with zek 0.1.22.
type Record struct {
	XMLName xml.Name `xml:"record"`
	Text    string   `xml:",chardata"`
	Xmlns   string   `xml:"xmlns,attr"`
	Header  struct {
		Text       string   `xml:",chardata"`
		Status     string   `xml:"status,attr"`
		Identifier string   `xml:"identifier"` // oai:doaj.org/article:8280...
		Datestamp  string   `xml:"datestamp"`  // 2020-11-30T23:19:13Z, 202...
		SetSpec    []string `xml:"setSpec"`    // TENDOlBzeWNob2xvZ3k~, TEN...
	} `xml:"header"`
	Metadata struct {
		Text string `xml:",chardata"`
		Dc   struct {
			Text           string   `xml:",chardata"`
			SchemaLocation string   `xml:"schemaLocation,attr"`
			Title          string   `xml:"title"`       // Efektivitas Pelatihan Emo...
			Identifier     []string `xml:"identifier"`  // 2528-0600, 2549-6166, 10....
			Date           string   `xml:"date"`        // 2020-11-01T00:00:00Z, 202...
			Relation       []string `xml:"relation"`    // https://ejournal.iai-trib...
			Description    string   `xml:"description"` // The rise of the case of m...
			Creator        []struct {
				Text string `xml:",chardata"` // Lebda Katodhia, Frikson  ...
				ID   string `xml:"id,attr"`
			} `xml:"creator"`
			Publisher string `xml:"publisher"` // Program Studi Psikologi I...
			Type      string `xml:"type"`      // article, article, article...
			Subject   []struct {
				Text string `xml:",chardata"` // self-injury, emotional in...
				Type string `xml:"type,attr"`
			} `xml:"subject"`
			Language []string `xml:"language"` // ID, ID, ID, ID, ID, ID, I...
			Source   string   `xml:"source"`   // Journal An-Nafs: Kajian P...
		} `xml:"dc"`
	} `xml:"metadata"`
	About string `xml:"about"`
}

func (r *Record) DOI() string {
	for _, f := range r.Metadata.Dc.Identifier {
		if strings.HasPrefix(f, "10.") {
			return f
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
	const prefix = "https://doaj.org/article/"
	for _, f := range r.Metadata.Dc.Identifier {
		if strings.HasPrefix(f, prefix) {
			return strings.Replace(f, prefix, "", 1)
		}
	}
	return ""
}

func (r *Record) ISSN() string {
	for _, f := range r.Metadata.Dc.Identifier {
		if issnPattern.MatchString(f) {
			return f
		}
	}
	return ""
}
