package cleaning

import "testing"

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
