package convert

import (
	"fmt"
	"testing"
)

func TestCleanDOI(t *testing.T) {
	testCases := []struct {
		raw    string
		result string
	}{

		{"10.1234/asdf ", "10.1234/asdf"},
		{"10.1037//0002-9432.72.1.50", "10.1037/0002-9432.72.1.50"},
		{"10.1037/0002-9432.72.1.50", "10.1037/0002-9432.72.1.50"},
		{"10.1026//1616-1041.3.2.86", "10.1026//1616-1041.3.2.86"},
		{"10.23750/abm.v88i2 -s.6506", ""},
		{"10.17167/mksz.2017.2.129–155", ""},
		{"http://doi.org/10.1234/asdf ", "10.1234/asdf"},
		{"https://dx.doi.org/10.1234/asdf ", "10.1234/asdf"},
		{"doi:10.1234/asdf ", "10.1234/asdf"},
		{"doi:10.1234/ asdf ", ""},
		{"10.4149/gpb¬_2017042", ""},
		{"10.6002/ect.2020.häyry", ""},
		{"10.30466/vrf.2019.98547.2350\u200e", ""},
		{"10.12016/j.issn.2096⁃1456.2017.06.014", ""},
		{"10.4025/diálogos.v17i2.36030", ""},
		{"10.19027/jai.10.106‒115", ""},
		{"10.15673/атбп2312-3125.17/2014.26332", ""},
		{"10.7326/M20-6817", "10.7326/m20-6817"},
	}
	for _, tc := range testCases {
		t.Run(fmt.Sprintf("testing DOI: %s", tc.raw), func(t *testing.T) {
			cleaned := cleanDOI(tc.raw)
			if cleaned != tc.result {
				t.Errorf("want %s, but got %s", tc.result, cleaned)
			}
		})
	}
}
