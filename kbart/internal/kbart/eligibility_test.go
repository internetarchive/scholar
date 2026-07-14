package kbart

import (
	"testing"

	"github.com/internetarchive/scholar/kbart/internal/fatcat"
)

// eligibleInfo returns a fully-populated Info that passes every check.
func eligibleInfo() *Info {
	return &Info{
		Ident: "aaaaaaaaaaaaaaaaaaaaaaaaaa",
		Container: &fatcat.Container{
			Name:      "Test Journal",
			ISSNL:     "1234-5678",
			ISSNE:     "1234-5678",
			Publisher: "Test Publisher",
		},
		Stats: &fatcat.Stats{
			Total:        100,
			Preservation: fatcat.Preservation{Bright: 100, Total: 100},
			ReleaseType:  map[string]int{"article-journal": 100},
		},
		ByType: []fatcat.TypeBucket{
			{ReleaseType: "article-journal", Bright: 100, Total: 100},
		},
		ByYear: []fatcat.YearBucket{
			{Year: 2010, Bright: 50},
			{Year: 2011, Bright: 50},
		},
		ByVolume: []fatcat.VolumeBucket{
			{Volume: "1", Bright: 50},
			{Volume: "2", Bright: 50},
		},
	}
}

func status(t *testing.T, info *Info) string {
	t.Helper()
	s, err := Evaluate(NewStaticSource(info))
	if err != nil {
		t.Fatalf("Evaluate error: %v", err)
	}
	return s
}

func TestEligibleSuccess(t *testing.T) {
	if s := status(t, eligibleInfo()); s != StatusSuccess {
		t.Fatalf("status = %q, want success", s)
	}
}

func TestEligibilityRejections(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Info)
		want   string
	}{
		{"container-type", func(i *Info) { i.Container.ContainerType = "magazine" }, "container-type"},
		{"missing-issnl", func(i *Info) { i.Container.ISSNL = "" }, "missing-issnl"},
		{"missing-issn", func(i *Info) { i.Container.ISSNE = ""; i.Container.ISSNP = "" }, "missing-issn"},
		{"few-releases", func(i *Info) { i.Stats.Total = 10 }, "few-releases"},
		{"low-overall-preservation", func(i *Info) { i.Stats.Preservation = fatcat.Preservation{Bright: 70, Total: 100} }, "low-overall-preservation-fraction"},
		{"few-papers", func(i *Info) { i.Stats.ReleaseType = map[string]int{"article-journal": 10, "dataset": 90} }, "few-papers"},
		{"low-paper-fraction", func(i *Info) { i.Stats.ReleaseType = map[string]int{"article-journal": 50, "dataset": 50} }, "low-paper-fraction"},
		{"few-preserved-papers", func(i *Info) {
			i.ByType = []fatcat.TypeBucket{{ReleaseType: "article-journal", Bright: 10, Total: 100}}
		}, "few-preserved-papers"},
		{"low-paper-preservation", func(i *Info) {
			i.ByType = []fatcat.TypeBucket{{ReleaseType: "article-journal", Bright: 70, Total: 100}}
		}, "low-paper-preservation-fraction"},
		{"no-year-spans", func(i *Info) { i.ByYear = nil }, "no-year-spans"},
		{"no-volume-spans", func(i *Info) { i.ByVolume = nil }, "no-volume-spans"},
		{"short-preserved-year-spans", func(i *Info) {
			i.ByYear = []fatcat.YearBucket{{Year: 2010, Bright: 100}}
		}, "short-preserved-year-spans"},
		{"non-contiguous-years", func(i *Info) {
			i.ByYear = []fatcat.YearBucket{{Year: 2010, Bright: 40}, {Year: 2012, Bright: 60}}
		}, "non-contiguous-years"},
		{"non-integer-volumes", func(i *Info) {
			i.ByVolume = []fatcat.VolumeBucket{{Volume: "1", Bright: 40}, {Volume: "2A", Bright: 60}}
		}, "non-integer-volumes"},
		{"non-contiguous-volumes", func(i *Info) {
			i.ByVolume = []fatcat.VolumeBucket{{Volume: "1", Bright: 40}, {Volume: "3", Bright: 60}}
		}, "non-contiguous-volumes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := eligibleInfo()
			tt.mutate(info)
			if s := status(t, info); s != tt.want {
				t.Errorf("status = %q, want %q", s, tt.want)
			}
		})
	}
}

// A dark/none release in a year or volume bucket drops that whole bucket from
// the fully-bright set (filter_preservation_histogram behavior).
func TestPartialBucketDropped(t *testing.T) {
	info := eligibleInfo()
	// Make 2011 partially dark: it drops out, leaving only one preserved year.
	info.ByYear = []fatcat.YearBucket{
		{Year: 2010, Bright: 50},
		{Year: 2011, Bright: 40, Dark: 10},
	}
	if s := status(t, info); s != "short-preserved-year-spans" {
		t.Errorf("status = %q, want short-preserved-year-spans", s)
	}
}

func TestISSNFallbackFromExtra(t *testing.T) {
	info := eligibleInfo()
	info.Container.ISSNE = ""
	info.Container.ISSNP = ""
	info.Container.Extra = map[string]any{"issnp": "9999-0000"}
	if s := status(t, info); s != StatusSuccess {
		t.Fatalf("status = %q, want success (issnp from extra)", s)
	}
	// Evaluate copies it to the canonical field, which then feeds ToRow.
	if info.Container.ISSNP != "9999-0000" {
		t.Errorf("ISSNP = %q, want 9999-0000", info.Container.ISSNP)
	}
}

func TestToRow(t *testing.T) {
	info := eligibleInfo()
	src := NewStaticSource(info)
	if s, _ := Evaluate(src); s != StatusSuccess {
		t.Fatalf("not eligible")
	}
	row, err := ToRow(src, 2020) // no ongoing-year adjustment
	if err != nil {
		t.Fatalf("ToRow: %v", err)
	}
	want := Row{
		PublicationType:      "serial",
		PublicationTitle:     "Test Journal",
		OnlineIdentifier:     "1234-5678",
		DateFirstIssueOnline: "2010",
		NumFirstVolOnline:    "1",
		DateLastIssueOnline:  "2011",
		NumLastVolOnline:     "2",
		TitleID:              "container_aaaaaaaaaaaaaaaaaaaaaaaaaa",
		CoverageDepth:        "fulltext",
		PublisherName:        "Test Publisher",
		LinkingISSN:          "1234-5678",
	}
	if row != want {
		t.Errorf("row mismatch:\n got %+v\nwant %+v", row, want)
	}
}

func TestToRowOngoingYearDecrement(t *testing.T) {
	info := eligibleInfo()
	src := NewStaticSource(info)
	Evaluate(src)
	row, err := ToRow(src, 2011) // last span year == thisYear -> drop it
	if err != nil {
		t.Fatalf("ToRow: %v", err)
	}
	if row.DateLastIssueOnline != "2010" || row.NumLastVolOnline != "1" {
		t.Errorf("expected decremented last year/vol 2010/1, got %s/%s",
			row.DateLastIssueOnline, row.NumLastVolOnline)
	}
}
