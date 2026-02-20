package orcid

import "testing"

func TestNormalize(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "already dashed",
			input: "0000-0002-1825-0097",
			want:  "0000-0002-1825-0097",
		},
		{
			name:  "undashed 16 digits",
			input: "0000000218250097",
			want:  "0000-0002-1825-0097",
		},
		{
			name:  "https URL",
			input: "https://orcid.org/0000-0002-1825-0097",
			want:  "0000-0002-1825-0097",
		},
		{
			name:  "http URL",
			input: "http://orcid.org/0000-0002-1825-0097",
			want:  "0000-0002-1825-0097",
		},
		{
			name:  "https URL with undashed digits",
			input: "https://orcid.org/0000000218250097",
			want:  "0000-0002-1825-0097",
		},
		{
			name:  "leading and trailing whitespace",
			input: "  0000-0002-1825-0097  ",
			want:  "0000-0002-1825-0097",
		},
		{
			name:  "undashed with checksum X",
			input: "000000021694233X",
			want:  "0000-0002-1694-233X",
		},
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
		{
			name:  "short invalid string passthrough",
			input: "1234",
			want:  "1234",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Normalize(tt.input)
			if got != tt.want {
				t.Errorf("Normalize(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
