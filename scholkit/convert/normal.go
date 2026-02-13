package convert

import (
	"regexp"
	"strings"
)

var doiRegex = regexp.MustCompile(`^10\.\d{4,}/\S+$`)

func cleanDOI(raw string) string {
	if raw == "" {
		return ""
	}
	raw = strings.TrimSpace(strings.ToLower(raw))
	if strings.Contains(raw, "\u2013") {
		// Do not attempt to normalize "en dash" and since FC does not allow
		// unicode in DOI, treat this as invalid.
		return ""
	}
	if strings.Count(raw, " ") != 0 {
		return ""
	}
	if strings.HasPrefix(raw, "doi:") {
		raw = raw[4:]
	}
	if strings.HasPrefix(raw, "http://") {
		raw = raw[7:]
	}
	if strings.HasPrefix(raw, "https://") {
		raw = raw[8:]
	}
	if strings.HasPrefix(raw, "doi.org/") {
		raw = raw[8:]
	}
	if strings.HasPrefix(raw, "dx.doi.org/") {
		raw = raw[11:]
	}
	if len(raw) > 9 && raw[7:9] == "//" && strings.Contains(raw, "10.1037//") {
		raw = raw[:8] + raw[9:]
	}
	for _, c := range "¬" {
		if strings.ContainsRune(raw, c) {
			return ""
		}
	}
	if !strings.HasPrefix(raw, "10.") {
		return ""
	}
	if !doiRegex.MatchString(raw) {
		return ""
	}
	if !isASCII(raw) {
		return ""
	}
	return raw
}

func isASCII(s string) bool {
	for _, r := range s {
		if r > 127 {
			return false
		}
	}
	return true
}
