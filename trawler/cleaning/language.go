package cleaning

import "strings"

// iso6392to1 maps ISO 639-2/B and MARC three-letter language codes to ISO
// 639-1 two-letter codes. This is the union of codes observed across all
// upstream sources (DataCite, DOAJ, PubMed). MARC-specific deprecated codes
// are included for PubMed compatibility.
var iso6392to1 = map[string]string{
	"eng": "en", "fre": "fr", "ger": "de", "spa": "es", "ita": "it",
	"por": "pt", "rus": "ru", "jpn": "ja", "chi": "zh", "kor": "ko",
	"dut": "nl", "pol": "pl", "swe": "sv", "dan": "da", "nor": "no",
	"fin": "fi", "ara": "ar", "heb": "he", "tur": "tr", "cze": "cs",
	"hun": "hu", "rum": "ro", "gre": "el", "ukr": "uk", "hrv": "hr",
	"slv": "sl", "bul": "bg", "cat": "ca", "vie": "vi", "ind": "id",
	"per": "fa", "hin": "hi", "lat": "la", "wel": "cy", "geo": "ka",
	"afr": "af", "slo": "sk", "lit": "lt", "lav": "lv", "est": "et",
	"alb": "sq", "ice": "is", "mac": "mk", "ser": "sr", "bos": "bs",
	"mon": "mn", "ben": "bn", "tam": "ta", "glg": "gl",
	"scr": "hr", // deprecated MARC code for Croatian
	"scc": "sr", // deprecated MARC code for Serbian
}

// NormalizeLanguage converts a language tag to a two-letter ISO 639-1 code.
// It accepts:
//   - ISO 639-1 two-letter codes (passed through as-is after lowercasing)
//   - ISO 639-2/B and MARC three-letter codes (e.g. "eng" → "en")
//   - BCP 47 language tags (e.g. "en-US" → "en")
//
// Returns an empty string if the input cannot be mapped.
func NormalizeLanguage(lang string) string {
	lang = strings.ToLower(strings.TrimSpace(lang))
	if lang == "" {
		return ""
	}
	if len(lang) == 2 {
		return lang
	}
	if len(lang) == 3 {
		return iso6392to1[lang]
	}
	// BCP 47 tags like "en-us" or "zh-tw"
	if len(lang) > 2 && lang[2] == '-' {
		return lang[:2]
	}
	return ""
}
