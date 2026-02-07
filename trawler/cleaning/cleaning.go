package cleaning

import (
	"bytes"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

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
	s = strings.ReplaceAll(s, "“", "\"")
	s = strings.ReplaceAll(s, "''", "\"")
	s = strings.ReplaceAll(s, ",,", "\"")

	return s
}
