package feeds

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/internetarchive/scholar/pubmed2json"
)

const mockEsearchOne = `{
	"esearchresult": {
		"count": "2",
		"retmax": "10000",
		"retstart": "0",
		"idlist": ["12345", "67890"]
	}
}`

// mockEfetch returns a minimal PubmedArticleSet with one article per PMID in the id param.
func mockEfetchXML(ids []string) string {
	var articles strings.Builder
	for _, id := range ids {
		fmt.Fprintf(&articles, `
		<PubmedArticle>
			<MedlineCitation Status="MEDLINE" Owner="NLM">
				<PMID Version="1">%s</PMID>
				<Article PubModel="Print">
					<Journal>
						<JournalIssue CitedMedium="Print">
							<PubDate><Year>2026</Year></PubDate>
						</JournalIssue>
						<Title>Test Journal</Title>
					</Journal>
					<ArticleTitle>Article %s</ArticleTitle>
				</Article>
				<MedlineJournalInfo/>
			</MedlineCitation>
		</PubmedArticle>`, id, id)
	}
	return `<?xml version="1.0" encoding="UTF-8"?><PubmedArticleSet>` +
		articles.String() +
		`</PubmedArticleSet>`
}

// newHarvesterServer returns a test server that handles esearch and efetch.
// The esearchResponses slice is consumed in order, one per esearch request.
func newHarvesterServer(esearchResponses []string) *httptest.Server {
	var callCount int
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "esearch"):
			idx := callCount
			if idx >= len(esearchResponses) {
				idx = len(esearchResponses) - 1
			}
			callCount++
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, esearchResponses[idx])
		case strings.Contains(r.URL.Path, "efetch"):
			ids := strings.Split(r.URL.Query().Get("id"), ",")
			w.Header().Set("Content-Type", "application/xml")
			io.WriteString(w, mockEfetchXML(ids))
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestPubMedHarvesterDaySliceKey(t *testing.T) {
	h := &PubMedHarvester{}
	// Use a mid-day time to verify start/end are normalized to day boundaries.
	day := time.Date(2026, 2, 17, 15, 30, 0, 0, time.UTC)
	key, start, end := h.DaySliceKey(day, "pubmed-feed-0-")

	if !strings.HasPrefix(key, "pubmed-feed-0-mdat-2026-02-17") {
		t.Errorf("unexpected key prefix: %s", key)
	}
	if !strings.HasSuffix(key, ".json.zst") {
		t.Errorf("key should end with .json.zst, got: %s", key)
	}

	wantStart := time.Date(2026, 2, 17, 0, 0, 0, 0, time.UTC)
	if !start.Equal(wantStart) {
		t.Errorf("start: got %v, want %v", start, wantStart)
	}
	wantEnd := time.Date(2026, 2, 17, 23, 59, 59, 0, time.UTC)
	if !end.Equal(wantEnd) {
		t.Errorf("end: got %v, want %v", end, wantEnd)
	}
}

func TestPubMedHarvesterWriteSlice(t *testing.T) {
	server := newHarvesterServer([]string{mockEsearchOne})
	defer server.Close()

	h := &PubMedHarvester{
		Client:  http.DefaultClient,
		ApiBase: server.URL,
	}

	var buf strings.Builder
	from := time.Date(2026, 2, 17, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 2, 17, 23, 59, 59, 0, time.UTC)

	if err := h.WriteSlice(&buf, from, until); err != nil {
		t.Fatalf("WriteSlice: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(lines))
	}

	wantPMIDs := []string{"12345", "67890"}
	for i, line := range lines {
		var rec pubmed2json.Record
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Errorf("line %d: invalid JSON: %v", i, err)
			continue
		}
		if rec.Type != "article" {
			t.Errorf("line %d: type = %q, want \"article\"", i, rec.Type)
		}
		if rec.Article == nil {
			t.Errorf("line %d: article is nil", i)
			continue
		}
		if got := rec.Article.MedlineCitation.PMID.Value; got != wantPMIDs[i] {
			t.Errorf("line %d: PMID = %q, want %q", i, got, wantPMIDs[i])
		}
	}
}

func TestPubMedHarvesterWriteDaySlice(t *testing.T) {
	server := newHarvesterServer([]string{mockEsearchOne})
	defer server.Close()

	dir := t.TempDir()
	h := &PubMedHarvester{
		Client:  http.DefaultClient,
		ApiBase: server.URL,
	}

	day := time.Date(2026, 2, 17, 0, 0, 0, 0, time.UTC)
	prefix := "pubmed-feed-0-"

	if err := h.WriteDaySlice(day, dir, prefix); err != nil {
		t.Fatalf("WriteDaySlice: %v", err)
	}

	key, _, _ := h.DaySliceKey(day, prefix)
	dst := filepath.Join(dir, key)
	if _, err := os.Stat(dst); os.IsNotExist(err) {
		t.Fatalf("expected file %s to exist", dst)
	}

	// Calling again should be a no-op (idempotent).
	if err := h.WriteDaySlice(day, dir, prefix); err != nil {
		t.Fatalf("WriteDaySlice (idempotent): %v", err)
	}
}

func TestPubMedHarvesterPagination(t *testing.T) {
	// Simulate a total of 3 records split across two esearch pages (fetchSize=2).
	page1 := `{"esearchresult":{"count":"3","retmax":"2","retstart":"0","idlist":["111","222"]}}`
	page2 := `{"esearchresult":{"count":"3","retmax":"2","retstart":"2","idlist":["333"]}}`

	server := newHarvesterServer([]string{page1, page2})
	defer server.Close()

	h := &PubMedHarvester{
		Client:    http.DefaultClient,
		ApiBase:   server.URL,
		FetchSize: 2,
		BatchSize: 200,
	}

	var buf strings.Builder
	from := time.Date(2026, 2, 17, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 2, 17, 23, 59, 59, 0, time.UTC)

	if err := h.WriteSlice(&buf, from, until); err != nil {
		t.Fatalf("WriteSlice: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3", len(lines))
	}

	wantPMIDs := []string{"111", "222", "333"}
	for i, line := range lines {
		var rec pubmed2json.Record
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Errorf("line %d: invalid JSON: %v", i, err)
			continue
		}
		if rec.Article == nil {
			t.Errorf("line %d: article is nil", i)
			continue
		}
		if got := rec.Article.MedlineCitation.PMID.Value; got != wantPMIDs[i] {
			t.Errorf("line %d: PMID = %q, want %q", i, got, wantPMIDs[i])
		}
	}
}
