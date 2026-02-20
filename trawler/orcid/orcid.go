package orcid

import (
	"fmt"
	"strings"
)

// Normalize converts various ORCID formats to the canonical dashed form.
func Normalize(raw string) string {
	raw = strings.TrimPrefix(raw, "https://orcid.org/")
	raw = strings.TrimPrefix(raw, "http://orcid.org/")
	raw = strings.TrimSpace(raw)
	if strings.Contains(raw, "-") {
		return raw
	}
	// 16-digit undashed form
	if len(raw) == 16 {
		return fmt.Sprintf("%s-%s-%s-%s", raw[0:4], raw[4:8], raw[8:12], raw[12:16])
	}
	return raw
}
