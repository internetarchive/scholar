package pubmed2json

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"sync"
	"testing"
)

const (
	testInputFile  = "samples/pubmed26n1350.xml.gz"
	testGoldenFile = "samples/output.ndjson.gz"

	wantArticles        = 20447
	wantDeleteCitations = 1
)

// convOnce ensures the expensive conversion runs at most once per test binary execution.
var (
	convOnce   sync.Once
	convOutput []byte
	convStats  ConvertStats
	convErr    error
)

// runConversion returns the cached conversion output and stats, running the
// conversion on first call. Calls t.Fatal if the conversion fails.
func runConversion(t *testing.T) ([]byte, ConvertStats) {
	t.Helper()
	convOnce.Do(func() {
		f, err := os.Open(testInputFile)
		if err != nil {
			convErr = err
			return
		}
		defer f.Close()

		gz, err := gzip.NewReader(f)
		if err != nil {
			convErr = err
			return
		}
		defer gz.Close()

		var buf bytes.Buffer
		convStats, convErr = Convert(gz, &buf)
		convOutput = buf.Bytes()
	})
	if convErr != nil {
		t.Fatalf("conversion failed: %v", convErr)
	}
	return convOutput, convStats
}

// splitLines splits b on newlines, dropping a trailing empty element.
func splitLines(b []byte) [][]byte {
	lines := bytes.Split(b, []byte("\n"))
	if len(lines) > 0 && len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// TestConvertCounts verifies the number of article and deleteCitation records.
func TestConvertCounts(t *testing.T) {
	_, stats := runConversion(t)
	if stats.Articles != wantArticles {
		t.Errorf("articles: got %d, want %d", stats.Articles, wantArticles)
	}
	if stats.DeleteCitations != wantDeleteCitations {
		t.Errorf("deleteCitations: got %d, want %d", stats.DeleteCitations, wantDeleteCitations)
	}
}

// TestConvertGolden compares every output line against the golden output.ndjson
// file, parsing each line as JSON so key order cannot cause spurious failures.
func TestConvertGolden(t *testing.T) {
	got, _ := runConversion(t)

	gf, err := os.Open(testGoldenFile)
	if err != nil {
		t.Fatalf("opening golden file %q: %v", testGoldenFile, err)
	}
	defer gf.Close()
	gfgz, err := gzip.NewReader(gf)
	if err != nil {
		t.Fatalf("creating gzip reader for %q: %v", testGoldenFile, err)
	}
	defer gfgz.Close()
	golden, err := io.ReadAll(gfgz)
	if err != nil {
		t.Fatalf("reading golden file %q: %v", testGoldenFile, err)
	}

	gotLines := splitLines(got)
	wantLines := splitLines(golden)

	if len(gotLines) != len(wantLines) {
		t.Fatalf("line count: got %d, want %d", len(gotLines), len(wantLines))
	}

	const maxReported = 5
	mismatches := 0
	for i, gotLine := range gotLines {
		wantLine := wantLines[i]
		if bytes.Equal(gotLine, wantLine) {
			continue
		}

		// Lines differ; compare as parsed JSON to give a precise error.
		var gotObj, wantObj any
		if err := json.Unmarshal(gotLine, &gotObj); err != nil {
			t.Errorf("line %d: invalid JSON in output: %v", i+1, err)
			mismatches++
		} else if err := json.Unmarshal(wantLine, &wantObj); err != nil {
			t.Errorf("line %d: invalid JSON in golden: %v", i+1, err)
			mismatches++
		} else {
			// Re-marshal both to get canonical representations for diffing.
			gotNorm, _ := json.Marshal(gotObj)
			wantNorm, _ := json.Marshal(wantObj)
			if !bytes.Equal(gotNorm, wantNorm) {
				t.Errorf("line %d mismatch\n got:  %s\n want: %s", i+1, gotNorm, wantNorm)
				mismatches++
			}
		}
		if mismatches >= maxReported {
			t.Errorf("stopping after %d mismatches", maxReported)
			break
		}
	}
}

// TestStripTags verifies that inline XML markup is removed from text content.
func TestStripTags(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "plain text unchanged",
			input: "hello world",
			want:  "hello world",
		},
		{
			name:  "bold tag stripped",
			input: "this is <b>important</b> text",
			want:  "this is important text",
		},
		{
			name:  "subscript stripped",
			input: "CO<sub>2</sub> concentration",
			want:  "CO2 concentration",
		},
		{
			name:  "superscript stripped",
			input: "10<sup>6</sup> cells",
			want:  "106 cells",
		},
		{
			name:  "italic stripped",
			input: "<i>in vitro</i> study",
			want:  "in vitro study",
		},
		{
			name:  "underline stripped",
			input: "see <u>figure 1</u>",
			want:  "see figure 1",
		},
		{
			name:  "mml:math namespace stripped, text preserved",
			input: `K<mml:math xmlns:mml="http://www.w3.org/1998/Math/MathML"><mml:msub><mml:mi>T</mml:mi></mml:msub></mml:math>`,
			want:  "KT",
		},
		{
			name:  "leading and trailing whitespace trimmed",
			input: "  hello  ",
			want:  "hello",
		},
		{
			name:  "nested tags all stripped",
			input: "<b><i>nested</i></b>",
			want:  "nested",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := stripTags(tc.input)
			if got != tc.want {
				t.Errorf("stripTags(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// findArticle scans the conversion output for the Record whose PMID matches.
// Returns nil if not found.
func findArticle(output []byte, pmid string) *Record {
	scanner := bufio.NewScanner(bytes.NewReader(output))
	scanner.Buffer(make([]byte, 4<<20), 4<<20) // 4 MiB line buffer for large records
	for scanner.Scan() {
		var rec Record
		if err := json.Unmarshal(scanner.Bytes(), &rec); err != nil || rec.Article == nil {
			continue
		}
		if rec.Article.MedlineCitation.PMID.Value == pmid {
			return &rec
		}
	}
	return nil
}

// TestSpecificArticles spot-checks key fields on known articles.
func TestSpecificArticles(t *testing.T) {
	output, _ := runConversion(t)

	t.Run("PMID_12238315_simple", func(t *testing.T) {
		rec := findArticle(output, "12238315")
		if rec == nil {
			t.Fatal("article not found")
		}
		mc := rec.Article.MedlineCitation
		art := mc.Article

		if mc.Status != "MEDLINE" {
			t.Errorf("status: got %q, want %q", mc.Status, "MEDLINE")
		}
		wantTitle := "[Guideline of the Austrian Society of Gynecology and Obstetrics on suspected sexual offenses. November 2001 status]."
		if string(art.ArticleTitle) != wantTitle {
			t.Errorf("articleTitle:\n got  %q\n want %q", art.ArticleTitle, wantTitle)
		}
		if art.AuthorList == nil {
			t.Fatal("authorList is nil")
		}
		if len(art.AuthorList.Authors) != 15 {
			t.Errorf("author count: got %d, want 15", len(art.AuthorList.Authors))
		}
		if len(mc.MeshHeadingList) != 8 {
			t.Errorf("mesh headings: got %d, want 8", len(mc.MeshHeadingList))
		}
		if len(art.PublicationTypes) != 2 {
			t.Errorf("publication types: got %d, want 2", len(art.PublicationTypes))
		}
		if art.PublicationTypes[0].Value != "Journal Article" {
			t.Errorf("first pubType: got %q, want %q", art.PublicationTypes[0].Value, "Journal Article")
		}
	})

	t.Run("PMID_24123842_complex", func(t *testing.T) {
		rec := findArticle(output, "24123842")
		if rec == nil {
			t.Fatal("article not found")
		}
		mc := rec.Article.MedlineCitation
		art := mc.Article

		wantTitle := "Association study of 83 candidate genes for bipolar disorder in chromosome 6q selected using an evidence-based prioritization algorithm."
		if string(art.ArticleTitle) != wantTitle {
			t.Errorf("articleTitle:\n got  %q\n want %q", art.ArticleTitle, wantTitle)
		}

		// Abstract with four labeled sections.
		if art.Abstract == nil {
			t.Fatal("abstract is nil")
		}
		if len(art.Abstract.Texts) != 4 {
			t.Errorf("abstract sections: got %d, want 4", len(art.Abstract.Texts))
		}
		if art.Abstract.Texts[0].NlmCategory != "BACKGROUND" {
			t.Errorf("first abstract nlmCategory: got %q, want %q", art.Abstract.Texts[0].NlmCategory, "BACKGROUND")
		}

		// Grant list.
		if art.GrantList == nil {
			t.Fatal("grantList is nil")
		}
		if len(art.GrantList.Grants) != 25 {
			t.Errorf("grant count: got %d, want 25", len(art.GrantList.Grants))
		}

		// Chemicals.
		if len(mc.ChemicalList) != 1 {
			t.Errorf("chemicals: got %d, want 1", len(mc.ChemicalList))
		}

		// MeSH headings.
		if len(mc.MeshHeadingList) != 9 {
			t.Errorf("mesh headings: got %d, want 9", len(mc.MeshHeadingList))
		}

		// DOI eLocationID.
		if len(art.ELocationIDs) != 1 {
			t.Fatalf("eLocationIDs: got %d, want 1", len(art.ELocationIDs))
		}
		if art.ELocationIDs[0].EIdType != "doi" || art.ELocationIDs[0].Value != "10.1002/ajmg.b.32200" {
			t.Errorf("eLocationID: got type=%q value=%q", art.ELocationIDs[0].EIdType, art.ELocationIDs[0].Value)
		}

		// Author count.
		if art.AuthorList == nil || len(art.AuthorList.Authors) != 14 {
			t.Errorf("author count: got %d, want 14", len(art.AuthorList.Authors))
		}

		// PubmedData article IDs include pubmed, doi, pmc, mid.
		if rec.Article.PubmedData == nil {
			t.Fatal("pubmedData is nil")
		}
		idMap := make(map[string]string)
		for _, id := range rec.Article.PubmedData.ArticleIds {
			idMap[id.IdType] = id.Value
		}
		if idMap["pubmed"] != "24123842" {
			t.Errorf("pubmed articleId: got %q, want %q", idMap["pubmed"], "24123842")
		}
		if idMap["doi"] != "10.1002/ajmg.b.32200" {
			t.Errorf("doi articleId: got %q, want %q", idMap["doi"], "10.1002/ajmg.b.32200")
		}
		if idMap["pmc"] != "PMC12888025" {
			t.Errorf("pmc articleId: got %q, want %q", idMap["pmc"], "PMC12888025")
		}

		// References.
		if len(rec.Article.PubmedData.ReferenceList) != 41 {
			t.Errorf("references: got %d, want 41", len(rec.Article.PubmedData.ReferenceList))
		}
	})

	t.Run("PMID_40854155_mml_math_stripped", func(t *testing.T) {
		// This article has mml:math in its abstract; verify the tags are stripped
		// and the subscript letters (K, T) are retained as plain text.
		rec := findArticle(output, "40854155")
		if rec == nil {
			t.Fatal("article not found")
		}
		art := rec.Article.MedlineCitation.Article
		if art.Abstract == nil || len(art.Abstract.Texts) == 0 {
			t.Fatal("abstract is missing")
		}
		// The first section contains a Michaelis-Menten KT term rendered via mml:math.
		text := art.Abstract.Texts[0].Text
		if !bytes.Contains([]byte(text), []byte("KT")) {
			t.Errorf("expected stripped mml:math subscript 'KT' in abstract text, got: %q", text[:min(len(text), 200)])
		}
		// No XML tags should survive.
		if bytes.Contains([]byte(text), []byte("<")) {
			t.Errorf("raw XML tag found in abstract text: %q", text[:min(len(text), 200)])
		}
	})
}

// TestDeleteCitation verifies the deleteCitation record and its PMID list.
func TestDeleteCitation(t *testing.T) {
	output, _ := runConversion(t)

	var delRec *Record
	scanner := bufio.NewScanner(bytes.NewReader(output))
	scanner.Buffer(make([]byte, 4<<20), 4<<20)
	for scanner.Scan() {
		var rec Record
		if err := json.Unmarshal(scanner.Bytes(), &rec); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if rec.Type == "deleteCitation" {
			delRec = &rec
			break
		}
	}

	if delRec == nil {
		t.Fatal("no deleteCitation record found")
	}
	if delRec.DeleteCitation == nil {
		t.Fatal("deleteCitation field is nil")
	}

	pmids := delRec.DeleteCitation.PMIDs
	if len(pmids) == 0 {
		t.Fatal("deleteCitation has no PMIDs")
	}

	// Verify the first few known PMIDs from this file.
	wantFirst := []string{"39379160", "41549876", "41588725"}
	for i, want := range wantFirst {
		if i >= len(pmids) {
			t.Errorf("pmid[%d]: missing, want %q", i, want)
			continue
		}
		if pmids[i].Value != want {
			t.Errorf("pmid[%d]: got %q, want %q", i, pmids[i].Value, want)
		}
	}
}

