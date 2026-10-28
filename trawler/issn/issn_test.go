package issn

import (
	"slices"
	"testing"
)

func Test_LinkingISSNs(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"one group", []string{"0915-2245", "1881-3933"}, []string{"0915-2245"}},
		{"two groups, in order", []string{"2355-7826", "0854-3844"}, []string{"2355-7826", "0854-3844"}},
		// 9876-5434 is well-formed but unassigned.
		{"unknown and empty contribute nothing", []string{"", "9876-5434"}, nil},
	}
	for _, c := range cases {
		if got := LinkingISSNs(c.in...); !slices.Equal(got, c.want) {
			t.Errorf("%s: LinkingISSNs(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

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
