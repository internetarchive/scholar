package pdfinfo

import (
	"context"
	"os"
	"testing"

	"github.com/google/go-cmp/cmp"
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

func TestParseBlob(t *testing.T) {
	blob, err := os.ReadFile("../testdata/pdf/1906.02444.pdf")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	md, err := ParseBlob(context.Background(), blob)
	if err != nil {
		t.Fatalf("ParseBlob: %v", err)
	}
	if md.PDFInfo == nil {
		t.Fatal("PDFInfo is nil")
	}
	if md.PDFCPU == nil || len(md.PDFCPU.Infos) == 0 {
		t.Fatal("PDFCPU.Infos is empty")
	}
	// Spot-check fields the fitz path is expected to populate. Date fields
	// are skipped: fitz returns the raw PDF date string (e.g.
	// "D:20190607003917+00'00'") which differs from Poppler's localized
	// "Fri Jun  7 02:39:17 2019 CEST".
	got := md.PDFInfo
	if got.Creator != "LaTeX with hyperref package" {
		t.Errorf("Creator: got %q, want %q", got.Creator, "LaTeX with hyperref package")
	}
	if got.Producer != "pdfTeX-1.40.17" {
		t.Errorf("Producer: got %q, want %q", got.Producer, "pdfTeX-1.40.17")
	}
	if got.Pages != 8 {
		t.Errorf("Pages: got %d, want 8", got.Pages)
	}
	if got.PDFVersion != "1.5" {
		t.Errorf("PDFVersion: got %q, want %q", got.PDFVersion, "1.5")
	}
	if got.FileSize != 633850 {
		t.Errorf("FileSize: got %d, want 633850", got.FileSize)
	}
	if dim := got.PageDim(); dim != (Dim{Width: 595.276, Height: 841.89}) {
		t.Errorf("PageDim: got %+v, want {Width:595.276 Height:841.89}", dim)
	}
	// Spot-check the pdfcpu side too.
	cpu := md.PDFCPU.Infos[0]
	if cpu.PageCount != 8 {
		t.Errorf("pdfcpu PageCount: got %d, want 8", cpu.PageCount)
	}
	if len(cpu.PageSizes) == 0 {
		t.Error("pdfcpu PageSizes is empty")
	}
}
