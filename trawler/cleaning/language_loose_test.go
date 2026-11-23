package cleaning

import "testing"

// Cases are real values from the etd-fulltext metadata.
func TestNormalizeLanguageLoose(t *testing.T) {
	cases := map[string]string{
		"en": "en", "spa": "es", "eng": "en", "por": "pt", "deu": "de", "fra": "fr",
		"English": "en", "Turkish": "tr", "Čeština": "cs", "Português": "pt", "Ελληνικά": "el",
		"en_US": "en", "cs_CZ": "cs", "pt_BR": "pt", "en_USen": "en", "de-DE": "de", "EN": "en",
		"English [eng]": "en", "English (en)": "en", "en;": "en", "en en": "en",
		"Português Brasil": "pt", "Português (Brasil)": "pt", "sr (latin script)": "sr",
		"Idioma::Português:portuguese:pt": "pt", "eesti keeles": "et", "inglise keeles": "en",
		"saksa keeles": "de", "Turkish and English": "tr", "eng deu": "en",
		"pol/eng": "pl", "Texto em português e inglês": "pt", "<p>English</p>": "en",
		"ENG - Inglés": "en", "American English": "en", "fi, ru": "fi", "ua": "uk", "gr": "el",
		"Bahasa Indonesia": "id", "Greek (Modern)": "el", "Castilian [spa]": "es",
		// not languages
		"und": "", "mul": "", "other": "", "Undetermined": "", "N/A": "", "": "",
		"Stellenbosch : University of Stellenbosch": "",
	}
	for in, want := range cases {
		if got := NormalizeLanguageLoose(in); got != want {
			t.Errorf("NormalizeLanguageLoose(%q) = %q, want %q", in, got, want)
		}
	}
}
