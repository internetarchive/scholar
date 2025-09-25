package issn

import (
	"embed"
	"encoding/csv"
	"errors"
	"io"
)

//go:embed 2025.issn2issnl.tsv
var ff embed.FS

var issn2issnl map[string]string

func init() {
	issn2issnl = map[string]string{}
	f, err := ff.Open("2025.issn2issnl.tsv")
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
