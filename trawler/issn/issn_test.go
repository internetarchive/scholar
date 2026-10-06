package issn

import "testing"

func Test_Normalize(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"0915-2245", "0915-2245"},
		{" 0915-2245 ", "0915-2245"},
		{"09152245", "0915-2245"},
		// X check digits come in either case.
		{"2434-561x", "2434-561X"},
		{"2434-561X", "2434-561X"},
		// Wrong check digit.
		{"0915-2246", ""},
		{"0915-224", ""},
		{"0915_2245", ""},
		{"abcd-efgh", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := Normalize(c.in); got != c.want {
			t.Errorf("Normalize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
