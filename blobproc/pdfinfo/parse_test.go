package pdfinfo

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

func TestParsePageSize(t *testing.T) {
	var cases = []struct {
		info *Info
		dim  Dim
	}{
		{
			info: nil,
			dim:  Dim{},
		},
		{
			info: &Info{
				PageSize: "",
			},
			dim: Dim{},
		},
		{
			info: &Info{
				PageSize: "garbage",
			},
			dim: Dim{},
		},
		{
			info: &Info{
				PageSize: "100 garbage",
			},
			dim: Dim{},
		},
		{
			info: &Info{
				PageSize: "100 garbage",
			},
			dim: Dim{},
		},
		{
			info: &Info{
				PageSize: "100 100 ambiguous string",
			},
			dim: Dim{},
		},
		{
			info: &Info{
				PageSize: "612 x 792 pts (letter)",
			},
			dim: Dim{
				Width:  612.0,
				Height: 792.0,
			},
		},
		{
			info: &Info{
				PageSize: "595.32 x 841.92 pts (A4)",
			},
			dim: Dim{
				Width:  595.32,
				Height: 841.92,
			},
		},
	}
	for _, c := range cases {
		dim := c.info.PageDim()
		if !cmp.Equal(dim, c.dim) {
			t.Fatalf("got %v, want %v, diff: %v", dim, c.dim, cmp.Diff(dim, c.dim))
		}
	}
}

func TestParseFile(t *testing.T) {
	var cases = []struct {
		filename string
		err      error
		info     *Info
	}{
		{
			filename: "../testdata/pdf/1906.02444.pdf",
			err:      nil,
			info: &Info{
				Creator:        "LaTeX with hyperref package",
				Producer:       "pdfTeX-1.40.17",
				CreationDate:   "Fri Jun  7 02:39:17 2019 CEST",
				ModDate:        "Fri Jun  7 02:39:17 2019 CEST",
				CustomMetadata: true,
				Form:           "none",
				Pages:          8,
				PageSize:       "595.276 x 841.89 pts (A4)",
				PageRot:        0,
				FileSize:       633850,
				PDFVersion:     "1.5",
			},
		},
	}
	ignoreTimeFields := cmpopts.IgnoreFields(Info{}, "CreationDate", "ModDate") // CEST, UTC, ...
	for _, c := range cases {
		info, err := runPdfInfo(context.Background(), c.filename)
		if err != c.err {
			t.Fatalf("got %v, want %v", err, c.err)
		}
		if !cmp.Equal(info, c.info, ignoreTimeFields) {
			t.Fatalf("got %v, want %v, diff: %v", info, c.info, cmp.Diff(info, c.info, ignoreTimeFields))
		}
	}
}

func TestParse(t *testing.T) {
	var cases = []struct {
		s    string
		info *Info
	}{
		{s: ``, info: &Info{}},
		{
			s: `
			Title:
			Subject:
			Keywords:
			Author:
			Creator:         LaTeX with hyperref package
			Producer:        pdfTeX-1.40.17
			CreationDate:    Fri Jun  7 02:39:17 2019 CEST
			ModDate:         Fri Jun  7 02:39:17 2019 CEST
			Custom Metadata: yes
			Metadata Stream: no
			Tagged:          no
			UserProperties:  no
			Suspects:        no
			Form:            none
			JavaScript:      no
			Pages:           8
			Encrypted:       no
			Page size:       595.276 x 841.89 pts (A4)
			Page rot:        0
			File size:       633850 bytes
			Optimized:       no
			PDF version:     1.5
			`,
			info: &Info{
				Creator:        "LaTeX with hyperref package",
				Producer:       "pdfTeX-1.40.17",
				CreationDate:   "Fri Jun  7 02:39:17 2019 CEST",
				ModDate:        "Fri Jun  7 02:39:17 2019 CEST",
				CustomMetadata: true,
				Form:           "none",
				Pages:          8,
				PageSize:       "595.276 x 841.89 pts (A4)",
				PageRot:        0,
				FileSize:       633850,
				PDFVersion:     "1.5",
			},
		},
		{
			s: `
			Title:           Choose the red pill <i>and</i> the blue pill: a position paper
			Subject:
			Keywords:        authentication, authorization, blue pill, grey goo, nebuchadnezzar, red pill, rotating shield harmonics, scooby doo, secure operating system, the matrix, trusted path
			Author:          Ben Laurie, Abe Singer
			Creator:         Microsoft Word
			Producer:        Mac OS X 10.5.5 Quartz PDFContext
			CreationDate:    Mon Nov 24 23:24:37 2008 CET
			ModDate:         Sat Apr 18 16:57:15 2009 CEST
			Custom Metadata: yes
			Metadata Stream: yes
			Tagged:          no
			UserProperties:  no
			Suspects:        no
			Form:            none
			JavaScript:      no
			Pages:           7
			Encrypted:       no
			Page size:       612 x 792 pts (letter)
			Page rot:        0
			File size:       419698 bytes
			Optimized:       yes
			PDF version:     1.3
			`,
			info: &Info{
				Title:          "Choose the red pill <i>and</i> the blue pill: a position paper",
				Keywords:       "authentication, authorization, blue pill, grey goo, nebuchadnezzar, red pill, rotating shield harmonics, scooby doo, secure operating system, the matrix, trusted path",
				Author:         "Ben Laurie, Abe Singer",
				Creator:        "Microsoft Word",
				Producer:       "Mac OS X 10.5.5 Quartz PDFContext",
				CreationDate:   "Mon Nov 24 23:24:37 2008 CET",
				ModDate:        "Sat Apr 18 16:57:15 2009 CEST",
				CustomMetadata: true,
				MetadataStream: true,
				Form:           "none",
				Pages:          7,
				PageSize:       "612 x 792 pts (letter)",
				PageRot:        0,
				FileSize:       419698,
				Optimized:      true,
				PDFVersion:     "1.3",
			},
		},
	}
	for _, c := range cases {
		info := ParseInfo(c.s)
		if !cmp.Equal(info, c.info) {
			t.Fatalf("got %v, want %v, diff: %v", info, c.info, cmp.Diff(info, c.info))
		}
	}
}
