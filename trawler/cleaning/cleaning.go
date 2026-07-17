package cleaning

import (
	"bytes"
	"fmt"
	"strings"
	"unicode"

	"github.com/PuerkitoBio/goquery"
)

const MinAbstractLength = 75

// NormalizeOrcid converts various ORCID formats to the canonical dashed form.
func NormalizeOrcid(orcid string) string {
	orcid = strings.TrimPrefix(orcid, "https://orcid.org/")
	orcid = strings.TrimPrefix(orcid, "http://orcid.org/")
	orcid = strings.TrimSpace(orcid)
	if strings.Contains(orcid, "-") {
		return orcid
	}
	// 16-digit undashed form
	if len(orcid) == 16 {
		return fmt.Sprintf("%s-%s-%s-%s", orcid[0:4], orcid[4:8], orcid[8:12], orcid[12:16])
	}
	return orcid
}

// NormalizeDOI strips common DOI URL prefixes, trims whitespace, and returns
// a lowercase DOI string. Returns an empty string if the result does not look
// like a DOI (i.e. does not start with "10.").
func NormalizeDOI(raw string) string {
	if raw == "" {
		return ""
	}
	d := strings.TrimSpace(raw)
	d = strings.ToLower(strings.Split(d, " ")[0])
	d = strings.TrimPrefix(d, "doi:")
	d = strings.TrimPrefix(d, "https://doi.org/")
	d = strings.TrimPrefix(d, "http://doi.org/")
	d = strings.TrimPrefix(d, "https://dx.doi.org/")
	d = strings.TrimPrefix(d, "http://dx.doi.org/")
	if strings.HasPrefix(d, "10.") {
		return d
	}
	return ""
}

// DeTag takes a string, parses it as HTML, then returns just its rendered text
func DeTag(s string) string {
	doc, err := goquery.NewDocumentFromReader(bytes.NewBufferString(s))
	if err != nil {
		// TODO fallback to a naive regex
		return s
	}
	return doc.Text()
}

var singleQuotes = []string{"`", "‘", "’", "‛", "⸂", "⸃", "⸌", "⸍", "⸜", "⸝"}

func CleanString(s string) string {
	// i wouldn't be this inefficient in python but shrug
	s = strings.ReplaceAll(s, "…", "...")
	for _, sq := range singleQuotes {
		s = strings.ReplaceAll(s, sq, "'")
	}
	s = strings.ReplaceAll(s, "„", "\"")
	s = strings.ReplaceAll(s, "\u201c", "\"")
	s = strings.ReplaceAll(s, "\u201d", "\"")
	s = strings.ReplaceAll(s, "''", "\"")
	s = strings.ReplaceAll(s, ",,", "\"")

	return s
}

// https://stackoverflow.com/questions/53069040/checking-a-string-contains-only-ascii-characters
func IsAscii(s string) bool {
	for _, c := range s {
		if c > unicode.MaxASCII {
			return false
		}
	}

	return true
}
