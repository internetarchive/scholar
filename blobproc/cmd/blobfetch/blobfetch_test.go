package main

import (
	"testing"
)

func TestExtractItemID(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{
			input:    "https://archive.org/details/OPENALEX-CRAWL-2025-09-20250922085855498-01863-01914-wbgrp-crawl047",
			expected: "OPENALEX-CRAWL-2025-09-20250922085855498-01863-01914-wbgrp-crawl047",
		},
		{
			input:    "OPENALEX-CRAWL-2025-09-20250922085855498-01863-01914-wbgrp-crawl047",
			expected: "OPENALEX-CRAWL-2025-09-20250922085855498-01863-01914-wbgrp-crawl047",
		},
		{
			input:    "http://archive.org/details/SOME-OTHER-ID",
			expected: "SOME-OTHER-ID",
		},
		{
			input:    "just-an-id",
			expected: "just-an-id",
		},
		{
			input:    "https://archive.org/details/ID_WITH_UNDERSCORES",
			expected: "ID_WITH_UNDERSCORES",
		},
	}

	for _, test := range tests {
		result := extractItemID(test.input)
		if result != test.expected {
			t.Errorf("extractItemID(%q) = %q, expected %q", test.input, result, test.expected)
		}
	}
}