package cleaning

import "testing"

func TestCleanString(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{"no change", "no change"},
		{"hello…world", "hello...world"},
		{"`backtick`", "'backtick'"},
		{"'left single'", "'left single'"},
		{"'right single'", "'right single'"},
		{"‛reversed'", "'reversed'"},
		{"„bottom double", "\"bottom double"},
		{"\u201cleft double\u201d", "\"left double\""},
		{"''two singles''", "\"two singles\""},
		{",,comma quote", "\"comma quote"},
	}
	for _, c := range cases {
		t.Run(c.input, func(t *testing.T) {
			got := CleanString(c.input)
			if got != c.expected {
				t.Errorf("CleanString(%q) = %q, want %q", c.input, got, c.expected)
			}
		})
	}
}

func TestLicenseSlugLookup(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		// empty / unrecognized
		{"", ""},
		{"https://example.com/unknown-license", ""},
		{"not a url !!!", ""},

		// http and https both work
		{"https://creativecommons.org/licenses/by/4.0", "CC-BY"},
		{"http://creativecommons.org/licenses/by/4.0", "CC-BY"},

		// trailing slash stripped
		{"https://creativecommons.org/licenses/by/4.0/", "CC-BY"},

		// /legalcode suffix stripped for creativecommons
		{"https://creativecommons.org/licenses/by/4.0/legalcode", "CC-BY"},

		// /uk suffix stripped for creativecommons
		{"https://creativecommons.org/licenses/by/4.0/uk", "CC-BY"},

		// case insensitive
		{"HTTPS://CREATIVECOMMONS.ORG/LICENSES/BY/4.0", "CC-BY"},

		// CC variants
		{"https://creativecommons.org/licenses/by-sa/4.0", "CC-BY-SA"},
		{"https://creativecommons.org/licenses/by-nd/4.0", "CC-BY-ND"},
		{"https://creativecommons.org/licenses/by-nc/4.0", "CC-BY-NC"},
		{"https://creativecommons.org/licenses/by-nc-sa/4.0", "CC-BY-NC-SA"},
		{"https://creativecommons.org/licenses/by-nc-nd/4.0", "CC-BY-NC-ND"},
		{"https://creativecommons.org/publicdomain/zero/1.0", "CC-0"},
		{"https://creativecommons.org/publicdomain/mark/1.0", "CC-0"},

		// SPDX
		{"https://spdx.org/licenses/CC0-1.0.json", "CC-0"},
		{"https://spdx.org/licenses/CC-BY-4.0.json", "CC-BY"},

		// other known licenses
		{"https://arxiv.org/licenses/nonexclusive-distrib/1.0", "ARXIV-1.0"},
		{"https://www.gnu.org/licenses/gpl-3.0.en.html", "GPLv3"},
	}
	for _, c := range cases {
		t.Run(c.input, func(t *testing.T) {
			got := LicenseSlugLookup(c.input)
			if got != c.expected {
				t.Errorf("LicenseSlugLookup(%q) = %q, want %q", c.input, got, c.expected)
			}
		})
	}
}

func TestDeTag(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{"", ""},
		{"plain text", "plain text"},
		{"<b>bold</b>", "bold"},
		{"<p>hello <em>world</em></p>", "hello world"},
		{"<br/>", ""},
		{"text with <a href=\"x\">link</a> inside", "text with link inside"},
	}
	for _, c := range cases {
		t.Run(c.input, func(t *testing.T) {
			got := DeTag(c.input)
			if got != c.expected {
				t.Errorf("DeTag(%q) = %q, want %q", c.input, got, c.expected)
			}
		})
	}
}

func TestNormalizeLanguage(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		// empty
		{"", ""},

		// 2-char ISO 639-1: passed through as-is (lowercased)
		{"en", "en"},
		{"fr", "fr"},
		{"EN", "en"},

		// 3-char ISO 639-2/B and MARC codes
		{"eng", "en"},
		{"fre", "fr"},
		{"ger", "de"},
		{"ENG", "en"}, // case-insensitive

		// deprecated MARC codes included for PubMed compat
		{"scr", "hr"}, // deprecated Croatian
		{"scc", "sr"}, // deprecated Serbian

		// BCP 47 tags: extract the primary subtag
		{"en-US", "en"},
		{"zh-TW", "zh"},
		{"en-us", "en"},

		// whitespace trimmed
		{"  eng  ", "en"},

		// unknown codes return empty string
		{"und", ""},
		{"xyz", ""},
		{"zzz", ""},
	}
	for _, c := range cases {
		t.Run(c.input, func(t *testing.T) {
			got := NormalizeLanguage(c.input)
			if got != c.expected {
				t.Errorf("NormalizeLanguage(%q) = %q, want %q", c.input, got, c.expected)
			}
		})
	}
}

func TestNormalizeDOI(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{"10.1234/test", "10.1234/test"},
		{"10.1234/TEST", "10.1234/test"},
		{"10.1234/foo", "10.1234/foo"},
		{"10.1234/FOO", "10.1234/foo"},
		{"doi:10.1234/test", "10.1234/test"},
		{"https://doi.org/10.1234/test", "10.1234/test"},
		{"http://doi.org/10.1234/test", "10.1234/test"},
		{"https://dx.doi.org/10.1234/test", "10.1234/test"},
		{"http://dx.doi.org/10.1234/test", "10.1234/test"},
		{"  10.1234/foo  ", "10.1234/foo"},
		{"not-a-doi", ""},
		{"", ""},
		{"https://arxiv.org/abs/2301.12345", ""},
		{"https://example.com/10.1234/foo", ""},
	}
	for _, c := range cases {
		t.Run(c.input, func(t *testing.T) {
			got := NormalizeDOI(c.input)
			if got != c.expected {
				t.Errorf("NormalizeDOI(%q) = %q, want %q", c.input, got, c.expected)
			}
		})
	}
}
