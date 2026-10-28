package issn

import (
	"embed"
	"encoding/csv"
	"errors"
	"io"
	"slices"
	"strings"
)

//go:embed 2026.issn2issnl.tsv
var ff embed.FS

var issn2issnl map[string]string

func init() {
	issn2issnl = map[string]string{}
	f, err := ff.Open("2026.issn2issnl.tsv")
	if err != nil {
		panic(err)
	}

	r := csv.NewReader(f)
	r.Comma = '\t'

	for {
		record, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			panic(err)
		}
		issn2issnl[record[0]] = record[1]
	}
}

func ISSN2ISSNL(i string) string {
	v, ok := issn2issnl[i]
	if !ok {
		return ""
	}
	return v
}

// LinkingISSNs returns every distinct ISSN-L that issns map to, in the order
// first seen. Empty ISSNs, and ISSNs too new for the table, contribute nothing:
// an ISSN-L is never guessed. Pass ISSNs normalized (see Normalize).
func LinkingISSNs(issns ...string) []string {
	var out []string
	for _, i := range issns {
		l := ISSN2ISSNL(i)
		if l == "" || slices.Contains(out, l) {
			continue
		}
		out = append(out, l)
	}
	return out
}

// Normalize returns i in canonical NNNN-NNNC form, or empty string if it is not
// a well-formed ISSN with a valid check digit. Upstreams hand us ISSNs with and
// without the hyphen and with a lowercase check digit; fatcat stores, and looks
// up by, the canonical form only.
func Normalize(i string) string {
	i = strings.ToUpper(strings.TrimSpace(i))
	if len(i) == 8 {
		i = i[:4] + "-" + i[4:]
	}
	if len(i) != 9 || i[4] != '-' {
		return ""
	}

	sum := 0
	for n, ch := range i[:4] + i[5:8] {
		if ch < '0' || ch > '9' {
			return ""
		}
		sum += int(ch-'0') * (8 - n)
	}

	check := (11 - sum%11) % 11
	want := byte('0' + check)
	if check == 10 {
		want = 'X'
	}
	if i[8] != want {
		return ""
	}

	return i
}
