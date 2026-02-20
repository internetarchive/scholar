package pubmed

import (
	"strconv"
	"testing"
	"time"

	"github.com/internetarchive/scholar/pubmed2json"
)

// helpers to build minimal PubmedArticle values without noise

func articleWithPMID(pmid string) pubmed2json.PubmedArticle {
	return pubmed2json.PubmedArticle{
		MedlineCitation: pubmed2json.MedlineCitation{
			PMID: pubmed2json.PMID{Value: pmid},
			Article: pubmed2json.Article{
				ArticleTitle: "Some Valid Title",
			},
		},
	}
}

// ── skipReason ────────────────────────────────────────────────────────────────

func TestSkipReason_NoPMID(t *testing.T) {
	a := pubmed2json.PubmedArticle{}
	if got := skipReason(a); got != "no-pmid" {
		t.Errorf("want no-pmid, got %q", got)
	}
}

func TestSkipReason_EmptyTitle(t *testing.T) {
	a := pubmed2json.PubmedArticle{
		MedlineCitation: pubmed2json.MedlineCitation{
			PMID: pubmed2json.PMID{Value: "12345"},
		},
	}
	if got := skipReason(a); got != "empty-title" {
		t.Errorf("want empty-title, got %q", got)
	}
}

func TestSkipReason_VernacularTitleFallback(t *testing.T) {
	// ArticleTitle is empty but VernacularTitle is present — should not skip.
	a := pubmed2json.PubmedArticle{
		MedlineCitation: pubmed2json.MedlineCitation{
			PMID: pubmed2json.PMID{Value: "12345"},
			Article: pubmed2json.Article{
				VernacularTitle: "Título en español",
			},
		},
	}
	if got := skipReason(a); got != "" {
		t.Errorf("want no skip reason, got %q", got)
	}
}

func TestSkipReason_BothTitlesEmpty(t *testing.T) {
	a := pubmed2json.PubmedArticle{
		MedlineCitation: pubmed2json.MedlineCitation{
			PMID: pubmed2json.PMID{Value: "12345"},
			Article: pubmed2json.Article{
				ArticleTitle:    "",
				VernacularTitle: "",
			},
		},
	}
	if got := skipReason(a); got != "empty-title" {
		t.Errorf("want empty-title, got %q", got)
	}
}

func TestSkipReason_StubTitles(t *testing.T) {
	stubs := []string{
		"In Process Citation",
		"in process citation",
		"Not Available",
		"OUP Accepted Manuscript",
		// with a trailing period that gets stripped before comparison
		"In Process Citation.",
		// wrapped in brackets that get stripped
		"[In Process Citation]",
	}
	for _, title := range stubs {
		a := pubmed2json.PubmedArticle{
			MedlineCitation: pubmed2json.MedlineCitation{
				PMID: pubmed2json.PMID{Value: "12345"},
				Article: pubmed2json.Article{
					ArticleTitle: pubmed2json.MarkupString(title),
				},
			},
		}
		if got := skipReason(a); got != "stub-title" {
			t.Errorf("title %q: want stub-title, got %q", title, got)
		}
	}
}

func TestSkipReason_TooManyAuthors(t *testing.T) {
	authors := make([]pubmed2json.Author, 2001)
	a := articleWithPMID("12345")
	a.MedlineCitation.Article.AuthorList = &pubmed2json.AuthorList{Authors: authors}
	if got := skipReason(a); got != "too-many-authors" {
		t.Errorf("want too-many-authors, got %q", got)
	}
}

func TestSkipReason_AuthorCountAtLimit(t *testing.T) {
	// exactly 2000 authors should still be processed
	authors := make([]pubmed2json.Author, 2000)
	a := articleWithPMID("12345")
	a.MedlineCitation.Article.AuthorList = &pubmed2json.AuthorList{Authors: authors}
	if got := skipReason(a); got != "" {
		t.Errorf("want no skip reason for 2000 authors, got %q", got)
	}
}

func TestSkipReason_TooManyRefs(t *testing.T) {
	refs := make([]pubmed2json.Reference, 5001)
	a := articleWithPMID("12345")
	a.PubmedData = &pubmed2json.PubmedData{ReferenceList: refs}
	if got := skipReason(a); got != "too-many-refs" {
		t.Errorf("want too-many-refs, got %q", got)
	}
}

func TestSkipReason_RefCountAtLimit(t *testing.T) {
	// exactly 5000 refs should still be processed
	refs := make([]pubmed2json.Reference, 5000)
	a := articleWithPMID("12345")
	a.PubmedData = &pubmed2json.PubmedData{ReferenceList: refs}
	if got := skipReason(a); got != "" {
		t.Errorf("want no skip reason for 5000 refs, got %q", got)
	}
}

func TestSkipReason_ValidArticle(t *testing.T) {
	a := articleWithPMID("12345678")
	if got := skipReason(a); got != "" {
		t.Errorf("want no skip reason, got %q", got)
	}
}

// ── parsePubDate ──────────────────────────────────────────────────────────────

func TestParsePubDate_YearOnly(t *testing.T) {
	year, isoDate := parsePubDate(pubmed2json.PubDate{Year: "2021"})
	if year != 2021 {
		t.Errorf("want year 2021, got %d", year)
	}
	if isoDate != "" {
		t.Errorf("want empty isoDate, got %q", isoDate)
	}
}

func TestParsePubDate_YearMonthDay_Numeric(t *testing.T) {
	year, isoDate := parsePubDate(pubmed2json.PubDate{Year: "2021", Month: "03", Day: "15"})
	if year != 2021 {
		t.Errorf("want year 2021, got %d", year)
	}
	if isoDate != "2021-03-15" {
		t.Errorf("want 2021-03-15, got %q", isoDate)
	}
}

func TestParsePubDate_YearMonthDay_MonthAbbr(t *testing.T) {
	cases := []struct {
		abbr    string
		wantMon string
	}{
		{"Jan", "01"}, {"Feb", "02"}, {"Mar", "03"}, {"Apr", "04"},
		{"May", "05"}, {"Jun", "06"}, {"Jul", "07"}, {"Aug", "08"},
		{"Sep", "09"}, {"Oct", "10"}, {"Nov", "11"}, {"Dec", "12"},
	}
	for _, c := range cases {
		year, isoDate := parsePubDate(pubmed2json.PubDate{Year: "2020", Month: c.abbr, Day: "1"})
		if year != 2020 {
			t.Errorf("month %s: want year 2020, got %d", c.abbr, year)
		}
		want := "2020-" + c.wantMon + "-01"
		if isoDate != want {
			t.Errorf("month %s: want %s, got %q", c.abbr, want, isoDate)
		}
	}
}

func TestParsePubDate_YearMonth_NoDay(t *testing.T) {
	// Month present but no Day — should return year only, no isoDate
	year, isoDate := parsePubDate(pubmed2json.PubDate{Year: "2019", Month: "Jun"})
	if year != 2019 {
		t.Errorf("want year 2019, got %d", year)
	}
	if isoDate != "" {
		t.Errorf("want empty isoDate when day is missing, got %q", isoDate)
	}
}

func TestParsePubDate_InvalidYear_NonNumeric(t *testing.T) {
	year, isoDate := parsePubDate(pubmed2json.PubDate{Year: "abcd"})
	if year != 0 {
		t.Errorf("want year 0, got %d", year)
	}
	if isoDate != "" {
		t.Errorf("want empty isoDate, got %q", isoDate)
	}
}

func TestParsePubDate_InvalidYear_TooOld(t *testing.T) {
	year, isoDate := parsePubDate(pubmed2json.PubDate{Year: "1299"})
	if year != 0 {
		t.Errorf("want year 0 for year < 1300, got %d", year)
	}
	if isoDate != "" {
		t.Errorf("want empty isoDate, got %q", isoDate)
	}
}

func TestParsePubDate_InvalidYear_TooFarFuture(t *testing.T) {
	future := strconv.Itoa(time.Now().Year() + 6)
	year, isoDate := parsePubDate(pubmed2json.PubDate{Year: future})
	if year != 0 {
		t.Errorf("want year 0 for far-future year, got %d", year)
	}
	if isoDate != "" {
		t.Errorf("want empty isoDate, got %q", isoDate)
	}
}

func TestParsePubDate_FutureYearWithinBound(t *testing.T) {
	// up to Now()+5 is allowed
	future := time.Now().Year() + 5
	year, _ := parsePubDate(pubmed2json.PubDate{Year: strconv.Itoa(future)})
	if year != future {
		t.Errorf("want year %d, got %d", future, year)
	}
}

func TestParsePubDate_Empty(t *testing.T) {
	year, isoDate := parsePubDate(pubmed2json.PubDate{})
	if year != 0 {
		t.Errorf("want year 0, got %d", year)
	}
	if isoDate != "" {
		t.Errorf("want empty isoDate, got %q", isoDate)
	}
}

func TestParsePubDate_MedlineDate_YearOnly(t *testing.T) {
	year, isoDate := parsePubDate(pubmed2json.PubDate{MedlineDate: "2005"})
	if year != 2005 {
		t.Errorf("want year 2005, got %d", year)
	}
	if isoDate != "" {
		t.Errorf("want empty isoDate from MedlineDate, got %q", isoDate)
	}
}

func TestParsePubDate_MedlineDate_Range(t *testing.T) {
	// Common PubMed format: "2005 Jan-Feb" — year extracted from prefix
	year, isoDate := parsePubDate(pubmed2json.PubDate{MedlineDate: "2005 Jan-Feb"})
	if year != 2005 {
		t.Errorf("want year 2005, got %d", year)
	}
	if isoDate != "" {
		t.Errorf("want empty isoDate from MedlineDate range, got %q", isoDate)
	}
}

func TestParsePubDate_MedlineDate_InvalidPrefix(t *testing.T) {
	year, isoDate := parsePubDate(pubmed2json.PubDate{MedlineDate: "Spring 2005"})
	if year != 0 {
		t.Errorf("want year 0 for non-numeric MedlineDate prefix, got %d", year)
	}
	if isoDate != "" {
		t.Errorf("want empty isoDate, got %q", isoDate)
	}
}

func TestParsePubDate_MedlineDate_TooOld(t *testing.T) {
	year, isoDate := parsePubDate(pubmed2json.PubDate{MedlineDate: "1200 Jan-Feb"})
	if year != 0 {
		t.Errorf("want year 0, got %d", year)
	}
	if isoDate != "" {
		t.Errorf("want empty isoDate, got %q", isoDate)
	}
}

func TestParsePubDate_YearTakesPrecedenceOverMedlineDate(t *testing.T) {
	// When both Year and MedlineDate are set, Year wins
	year, _ := parsePubDate(pubmed2json.PubDate{Year: "2010", MedlineDate: "2005 Jan-Feb"})
	if year != 2010 {
		t.Errorf("want year 2010 (from Year field), got %d", year)
	}
}
