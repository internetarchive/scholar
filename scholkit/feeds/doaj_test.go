package feeds

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/miku/metha"
)

// mockDOAJRecord is a complete OAI-PMH record in the oai_doaj metadata format.
const mockDOAJRecord = `<record>
  <header>
    <identifier>oai:doaj.org/article:abc1234567890abcdef1234567890ab</identifier>
    <datestamp>2023-01-15</datestamp>
    <setSpec>some:set</setSpec>
  </header>
  <metadata>
    <oai_doaj:doajArticle xmlns:oai_doaj="http://doaj.org/features/oai_doaj/1.0/">
      <oai_doaj:language>EN</oai_doaj:language>
      <oai_doaj:publisher>Test Publisher</oai_doaj:publisher>
      <oai_doaj:journalTitle>Test Journal</oai_doaj:journalTitle>
      <oai_doaj:issn>1234-5678</oai_doaj:issn>
      <oai_doaj:eissn>8765-4321</oai_doaj:eissn>
      <oai_doaj:publicationDate>2023-01-15</oai_doaj:publicationDate>
      <oai_doaj:volume>10</oai_doaj:volume>
      <oai_doaj:issue>2</oai_doaj:issue>
      <oai_doaj:startPage>100</oai_doaj:startPage>
      <oai_doaj:endPage>110</oai_doaj:endPage>
      <oai_doaj:doi>10.1234/test.2023</oai_doaj:doi>
      <oai_doaj:title>Test Article Title</oai_doaj:title>
      <oai_doaj:abstract>A test abstract.</oai_doaj:abstract>
      <oai_doaj:fullTextUrl format="PDF">https://example.com/article.pdf</oai_doaj:fullTextUrl>
      <oai_doaj:keywords>
        <oai_doaj:keyword>keyword1</oai_doaj:keyword>
        <oai_doaj:keyword>keyword2</oai_doaj:keyword>
      </oai_doaj:keywords>
      <oai_doaj:authors>
        <oai_doaj:author>
          <oai_doaj:name>Jane Smith</oai_doaj:name>
          <oai_doaj:affiliationId>aff1</oai_doaj:affiliationId>
          <oai_doaj:orcid_id>0000-0000-0000-0001</oai_doaj:orcid_id>
        </oai_doaj:author>
      </oai_doaj:authors>
      <oai_doaj:affiliationsList>
        <oai_doaj:affiliationName affiliationId="aff1">University of Testing</oai_doaj:affiliationName>
      </oai_doaj:affiliationsList>
      <oai_doaj:licenseRef>CC-BY</oai_doaj:licenseRef>
    </oai_doaj:doajArticle>
  </metadata>
</record>`

// doajMetaRecord builds a metha.Record whose Metadata.Body is metaBody, which
// should be the raw XML of a <oai_doaj:doajArticle> element.
func doajMetaRecord(identifier string, metaBody []byte) *metha.Record {
	return &metha.Record{
		Header: metha.Header{
			Identifier: identifier,
			DateStamp:  "2023-01-15",
			SetSpec:    []string{"some:set"},
		},
		Metadata: metha.Metadata{Body: metaBody},
	}
}

// -- doajArticleID --

func TestDoajArticleID(t *testing.T) {
	cases := []struct {
		identifier string
		want       string
	}{
		{"oai:doaj.org/article:abc123", "abc123"},
		{"oai:doaj.org/article:abc1234567890abcdef1234567890ab", "abc1234567890abcdef1234567890ab"},
		{"not-a-doaj-identifier", "not-a-doaj-identifier"},
		{"", ""},
	}
	for _, tc := range cases {
		got := doajArticleID(tc.identifier)
		if got != tc.want {
			t.Errorf("doajArticleID(%q): got %q, want %q", tc.identifier, got, tc.want)
		}
	}
}

// -- flattenDOAJRecord --

func TestFlattenDOAJRecordEmptyBody(t *testing.T) {
	r := doajMetaRecord("oai:doaj.org/article:abc123", nil)
	dr := flattenDOAJRecord(r)
	if dr.Identifier != "oai:doaj.org/article:abc123" {
		t.Errorf("Identifier: got %q", dr.Identifier)
	}
	if dr.ID != "abc123" {
		t.Errorf("ID: got %q, want abc123", dr.ID)
	}
	if dr.Title != "" || dr.Publisher != "" || len(dr.Authors) != 0 {
		t.Errorf("expected empty metadata fields for nil body; got title=%q publisher=%q authors=%v",
			dr.Title, dr.Publisher, dr.Authors)
	}
}

func TestFlattenDOAJRecordFull(t *testing.T) {
	const metaXML = `<oai_doaj:doajArticle xmlns:oai_doaj="http://doaj.org/features/oai_doaj/1.0/">
  <oai_doaj:language>EN</oai_doaj:language>
  <oai_doaj:publisher>  Test Publisher  </oai_doaj:publisher>
  <oai_doaj:journalTitle>Test Journal</oai_doaj:journalTitle>
  <oai_doaj:issn>1234-5678</oai_doaj:issn>
  <oai_doaj:eissn>8765-4321</oai_doaj:eissn>
  <oai_doaj:publicationDate>2023-01-15</oai_doaj:publicationDate>
  <oai_doaj:volume>10</oai_doaj:volume>
  <oai_doaj:issue>2</oai_doaj:issue>
  <oai_doaj:startPage>100</oai_doaj:startPage>
  <oai_doaj:endPage>110</oai_doaj:endPage>
  <oai_doaj:doi>10.1234/test.2023</oai_doaj:doi>
  <oai_doaj:title>Test Article Title</oai_doaj:title>
  <oai_doaj:abstract>A test abstract.</oai_doaj:abstract>
  <oai_doaj:fullTextUrl format="PDF">https://example.com/article.pdf</oai_doaj:fullTextUrl>
  <oai_doaj:keywords>
    <oai_doaj:keyword>keyword1</oai_doaj:keyword>
    <oai_doaj:keyword>  </oai_doaj:keyword>
    <oai_doaj:keyword>keyword2</oai_doaj:keyword>
  </oai_doaj:keywords>
  <oai_doaj:authors>
    <oai_doaj:author>
      <oai_doaj:name>Jane Smith</oai_doaj:name>
      <oai_doaj:affiliationId>aff1</oai_doaj:affiliationId>
      <oai_doaj:orcid_id>0000-0000-0000-0001</oai_doaj:orcid_id>
    </oai_doaj:author>
  </oai_doaj:authors>
  <oai_doaj:affiliationsList>
    <oai_doaj:affiliationName affiliationId="aff1">University of Testing</oai_doaj:affiliationName>
  </oai_doaj:affiliationsList>
  <oai_doaj:licenseRef>CC-BY</oai_doaj:licenseRef>
</oai_doaj:doajArticle>`

	dr := flattenDOAJRecord(doajMetaRecord("oai:doaj.org/article:abc123", []byte(metaXML)))

	if dr.Language != "EN" {
		t.Errorf("Language: got %q, want EN", dr.Language)
	}
	// TrimSpace is applied — leading/trailing spaces stripped.
	if dr.Publisher != "Test Publisher" {
		t.Errorf("Publisher: got %q, want Test Publisher", dr.Publisher)
	}
	if dr.DOI != "10.1234/test.2023" {
		t.Errorf("DOI: got %q, want 10.1234/test.2023", dr.DOI)
	}
	if dr.Title != "Test Article Title" {
		t.Errorf("Title: got %q, want Test Article Title", dr.Title)
	}
	if dr.FullTextURL != "https://example.com/article.pdf" {
		t.Errorf("FullTextURL: got %q", dr.FullTextURL)
	}
	if dr.FullTextFormat != "PDF" {
		t.Errorf("FullTextFormat: got %q, want PDF", dr.FullTextFormat)
	}
	if dr.LicenseRef != "CC-BY" {
		t.Errorf("LicenseRef: got %q, want CC-BY", dr.LicenseRef)
	}
	// The blank keyword ("  ") should be filtered out.
	if len(dr.Keywords) != 2 || dr.Keywords[0] != "keyword1" || dr.Keywords[1] != "keyword2" {
		t.Errorf("Keywords: got %v, want [keyword1 keyword2]", dr.Keywords)
	}
	if len(dr.Authors) != 1 {
		t.Fatalf("Authors: got %d, want 1", len(dr.Authors))
	}
	if dr.Authors[0].Name != "Jane Smith" {
		t.Errorf("Authors[0].Name: got %q, want Jane Smith", dr.Authors[0].Name)
	}
	// affiliationId "aff1" should resolve to the name from affiliationsList.
	if dr.Authors[0].Affiliation != "University of Testing" {
		t.Errorf("Authors[0].Affiliation: got %q, want University of Testing", dr.Authors[0].Affiliation)
	}
	if dr.Authors[0].OrcidID != "0000-0000-0000-0001" {
		t.Errorf("Authors[0].OrcidID: got %q", dr.Authors[0].OrcidID)
	}
}

func TestFlattenDOAJRecordAffiliationFallback(t *testing.T) {
	// When an author's affiliationId does not appear in affiliationsList the
	// raw ID string should be used as the affiliation value.
	const metaXML = `<oai_doaj:doajArticle xmlns:oai_doaj="http://doaj.org/features/oai_doaj/1.0/">
  <oai_doaj:authors>
    <oai_doaj:author>
      <oai_doaj:name>Bob Jones</oai_doaj:name>
      <oai_doaj:affiliationId>unknown-id</oai_doaj:affiliationId>
    </oai_doaj:author>
  </oai_doaj:authors>
</oai_doaj:doajArticle>`

	dr := flattenDOAJRecord(doajMetaRecord("oai:doaj.org/article:xyz", []byte(metaXML)))

	if len(dr.Authors) != 1 {
		t.Fatalf("Authors: got %d, want 1", len(dr.Authors))
	}
	if dr.Authors[0].Affiliation != "unknown-id" {
		t.Errorf("Affiliation fallback: got %q, want unknown-id", dr.Authors[0].Affiliation)
	}
}

// -- DOAJHarvester.baseURL --

func TestDOAJHarvesterBaseURL(t *testing.T) {
	h := &DOAJHarvester{}
	if got := h.baseURL(); got != doajDefaultBaseURL {
		t.Errorf("default baseURL: got %q, want %q", got, doajDefaultBaseURL)
	}
	h.BaseURL = "https://custom.example.com/oai"
	if got := h.baseURL(); got != h.BaseURL {
		t.Errorf("custom baseURL: got %q, want %q", got, h.BaseURL)
	}
}

// -- DOAJHarvester.DaySliceKey --

func TestDOAJHarvesterDaySliceKey(t *testing.T) {
	h := &DOAJHarvester{}
	ts := time.Date(2023, 1, 15, 12, 30, 0, 0, time.UTC)

	key, start, end := h.DaySliceKey(ts, "")

	wantKey := "doaj-2023-01-15-2023-01-15.json.zst"
	if key != wantKey {
		t.Errorf("key: got %q, want %q", key, wantKey)
	}
	wantStart := time.Date(2023, 1, 15, 0, 0, 0, 0, time.UTC)
	if !start.Equal(wantStart) {
		t.Errorf("start: got %v, want %v", start, wantStart)
	}
	wantEnd := time.Date(2023, 1, 15, 23, 59, 59, 0, time.UTC)
	if !end.Equal(wantEnd) {
		t.Errorf("end: got %v, want %v", end, wantEnd)
	}
}

func TestDOAJHarvesterDaySliceKeyPrefix(t *testing.T) {
	h := &DOAJHarvester{}
	ts := time.Date(2023, 1, 15, 0, 0, 0, 0, time.UTC)
	key, _, _ := h.DaySliceKey(ts, "harvest-")
	if !strings.HasPrefix(key, "harvest-") {
		t.Errorf("key with prefix: got %q, missing prefix", key)
	}
}

// -- DOAJHarvester.WriteSlice --

func TestDOAJHarvesterWriteSliceEmpty(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/xml")
		io.WriteString(w, oaiError("noRecordsMatch", "No records match"))
	}))
	defer server.Close()

	h := &DOAJHarvester{BaseURL: server.URL, Client: &metha.Client{Doer: http.DefaultClient}}
	var buf bytes.Buffer
	from := time.Date(2023, 1, 15, 0, 0, 0, 0, time.UTC)
	until := time.Date(2023, 1, 15, 23, 59, 59, 0, time.UTC)
	if err := h.WriteSlice(&buf, from, until); err != nil {
		t.Fatalf("WriteSlice: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("expected empty output for noRecordsMatch, got %d bytes", buf.Len())
	}
}

func TestDOAJHarvesterWriteSliceSingleRecord(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/xml")
		io.WriteString(w, oaiListRecords([]string{mockDOAJRecord}, ""))
	}))
	defer server.Close()

	h := &DOAJHarvester{BaseURL: server.URL, Client: &metha.Client{Doer: http.DefaultClient}}
	var buf bytes.Buffer
	from := time.Date(2023, 1, 15, 0, 0, 0, 0, time.UTC)
	until := time.Date(2023, 1, 15, 23, 59, 59, 0, time.UTC)
	if err := h.WriteSlice(&buf, from, until); err != nil {
		t.Fatalf("WriteSlice: %v", err)
	}
	if buf.Len() == 0 {
		t.Fatal("expected non-empty output")
	}
	var rec DOAJRecord
	if err := json.NewDecoder(&buf).Decode(&rec); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if rec.Identifier != "oai:doaj.org/article:abc1234567890abcdef1234567890ab" {
		t.Errorf("Identifier: got %q", rec.Identifier)
	}
	if rec.ID != "abc1234567890abcdef1234567890ab" {
		t.Errorf("ID: got %q", rec.ID)
	}
	if rec.Title != "Test Article Title" {
		t.Errorf("Title: got %q, want Test Article Title", rec.Title)
	}
	if rec.DOI != "10.1234/test.2023" {
		t.Errorf("DOI: got %q, want 10.1234/test.2023", rec.DOI)
	}
	if len(rec.Authors) != 1 || rec.Authors[0].Name != "Jane Smith" {
		t.Errorf("Authors: got %v", rec.Authors)
	}
	if rec.Authors[0].Affiliation != "University of Testing" {
		t.Errorf("Affiliation: got %q, want University of Testing", rec.Authors[0].Affiliation)
	}
}

func TestDOAJHarvesterWriteSliceResumption(t *testing.T) {
	const token = "DOAJ_RESUME_TOKEN_123"
	var callCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&callCount, 1)
		w.Header().Set("Content-Type", "text/xml")
		if n == 1 {
			io.WriteString(w, oaiListRecords([]string{mockDOAJRecord}, token))
		} else {
			io.WriteString(w, oaiListRecords([]string{mockDOAJRecord}, ""))
		}
	}))
	defer server.Close()

	h := &DOAJHarvester{BaseURL: server.URL, Client: &metha.Client{Doer: http.DefaultClient}}
	var buf bytes.Buffer
	from := time.Date(2023, 1, 15, 0, 0, 0, 0, time.UTC)
	until := time.Date(2023, 1, 15, 23, 59, 59, 0, time.UTC)
	if err := h.WriteSlice(&buf, from, until); err != nil {
		t.Fatalf("WriteSlice: %v", err)
	}
	if got := atomic.LoadInt32(&callCount); got != 2 {
		t.Errorf("expected 2 OAI requests (pagination), got %d", got)
	}
	dec := json.NewDecoder(&buf)
	var count int
	for {
		var rec map[string]interface{}
		if err := dec.Decode(&rec); err != nil {
			break
		}
		count++
	}
	if count != 2 {
		t.Errorf("expected 2 records in output, got %d", count)
	}
}

func TestDOAJHarvesterWriteSliceOAIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/xml")
		io.WriteString(w, oaiError("badArgument", "The request includes illegal arguments"))
	}))
	defer server.Close()

	h := &DOAJHarvester{BaseURL: server.URL, Client: &metha.Client{Doer: http.DefaultClient}}
	var buf bytes.Buffer
	from := time.Date(2023, 1, 15, 0, 0, 0, 0, time.UTC)
	until := time.Date(2023, 1, 15, 23, 59, 59, 0, time.UTC)
	err := h.WriteSlice(&buf, from, until)
	if err == nil {
		t.Fatal("expected error for OAI badArgument, got nil")
	}
	if !strings.Contains(err.Error(), "badArgument") {
		t.Errorf("error should mention OAI error code, got: %v", err)
	}
}

// -- DOAJHarvester.WriteDaySlice --

func TestDOAJHarvesterWriteDaySliceIdempotent(t *testing.T) {
	dir := t.TempDir()
	h := &DOAJHarvester{}
	ts := time.Date(2023, 1, 15, 0, 0, 0, 0, time.UTC)
	key, _, _ := h.DaySliceKey(ts, "")
	dst := filepath.Join(dir, key)

	want := []byte("already done")
	if err := os.WriteFile(dst, want, 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if err := h.WriteDaySlice(ts, dir, ""); err != nil {
		t.Fatalf("WriteDaySlice: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("file was overwritten; got %q, want %q", got, want)
	}
}

func TestDOAJHarvesterWriteDaySlice(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/xml")
		io.WriteString(w, oaiListRecords([]string{mockDOAJRecord}, ""))
	}))
	defer server.Close()

	h := &DOAJHarvester{BaseURL: server.URL, Client: &metha.Client{Doer: http.DefaultClient}}
	dir := t.TempDir()
	ts := time.Date(2023, 1, 15, 0, 0, 0, 0, time.UTC)

	if err := h.WriteDaySlice(ts, dir, ""); err != nil {
		t.Fatalf("WriteDaySlice: %v", err)
	}
	key, _, _ := h.DaySliceKey(ts, "")
	dst := filepath.Join(dir, key)
	if _, err := os.Stat(dst); os.IsNotExist(err) {
		t.Fatalf("expected output file %s to exist", dst)
	}

	// Verify the file is valid zstd-compressed NDJSON.
	f, err := os.Open(dst)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	zr, err := zstd.NewReader(f)
	if err != nil {
		t.Fatalf("zstd.NewReader: %v", err)
	}
	defer zr.Close()
	var rec DOAJRecord
	if err := json.NewDecoder(zr).Decode(&rec); err != nil {
		t.Fatalf("output is not valid JSON inside zstd: %v", err)
	}
	if rec.Identifier == "" {
		t.Errorf("expected non-empty identifier in output record")
	}
	if rec.ID == "" {
		t.Errorf("expected non-empty id in output record")
	}
}
