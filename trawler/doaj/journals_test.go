package doaj

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"git.archive.org/webgroup/scholar/trawler/fatcat2"
	"git.archive.org/webgroup/scholar/trawler/indexing"
	"github.com/google/uuid"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/log"
)

const journalCSVHeader = "\ufeffJournal title,Journal URL,URL in DOAJ,Alternative title," +
	"Journal ISSN (print version),Journal EISSN (online version),Publisher,Country of publisher,Added on Date\n"

func Test_parseJournalCSV(t *testing.T) {
	in := journalCSVHeader +
		`Nihon Shouni Jinzoubyou Gakkai zasshi,http://www.jspn.jp/,https://doaj.org/toc/4beb218f47f444b99031dac003e8e6c4,"Japanese Journal of Pediatric Nephrology",0915-2245,1881-3933,"The Japanese Society, for Pediatric Nephrology",Japan,2022-07-08T09:33:44Z` + "\n" +
		`Forensic Insights,,https://doaj.org/toc/abc,,,3105-4242,Someone,India,2026-09-01T00:00:00Z` + "\n"

	rows, err := parseJournalCSV(strings.NewReader(in))
	require.NoError(t, err)
	require.Len(t, rows, 2)

	require.Equal(t, journalRow{
		Title:     "Nihon Shouni Jinzoubyou Gakkai zasshi",
		PISSN:     "0915-2245",
		EISSN:     "1881-3933",
		Publisher: "The Japanese Society, for Pediatric Nephrology",
		URL:       "http://www.jspn.jp/",
		DOAJURL:   "https://doaj.org/toc/4beb218f47f444b99031dac003e8e6c4",
		Country:   "Japan",
		AddedOn:   "2022-07-08T09:33:44Z",
	}, rows[0])
	require.Equal(t, "", rows[1].PISSN)
	require.Equal(t, "3105-4242", rows[1].EISSN)
}

// Columns are found by name, so DOAJ reordering or adding them is harmless.
func Test_parseJournalCSV_ReorderedColumns(t *testing.T) {
	in := "Journal EISSN (online version),Some New Column,Journal title,Journal ISSN (print version)\n" +
		"1881-3933,whatever,A Journal,0915-2245\n"

	rows, err := parseJournalCSV(strings.NewReader(in))
	require.NoError(t, err)
	require.Equal(t, []journalRow{{Title: "A Journal", PISSN: "0915-2245", EISSN: "1881-3933"}}, rows)
}

// A file without the columns we need -- an error page, a format change -- is
// rejected rather than imported as rows of nothing.
func Test_parseJournalCSV_MissingColumn(t *testing.T) {
	_, err := parseJournalCSV(strings.NewReader("Journal title,Publisher\nA Journal,Someone\n"))
	require.ErrorContains(t, err, "Journal ISSN (print version)")

	_, err = parseJournalCSV(strings.NewReader("<html><body>oops</body></html>\n"))
	require.Error(t, err)
}

func Test_resolveISSNs(t *testing.T) {
	cases := []struct {
		name string
		row  journalRow
		want journalISSNs
	}{
		{
			name: "both ISSNs share an ISSN-L",
			row:  journalRow{PISSN: "0915-2245", EISSN: "1881-3933"},
			want: journalISSNs{ISSNL: "0915-2245", ISSNP: "0915-2245", ISSNE: "1881-3933",
				LinkingISSNs: []string{"0915-2245"}},
		},
		{
			name: "print and online in different ISSN-L groups: print wins, both kept for lookup",
			row:  journalRow{PISSN: "0854-3844", EISSN: "2355-7826"},
			want: journalISSNs{ISSNL: "0854-3844", ISSNP: "0854-3844", ISSNE: "2355-7826",
				LinkingISSNs: []string{"0854-3844", "2355-7826"}},
		},
		{
			name: "too new for the ISSN-L table: no ISSN-L is invented",
			row:  journalRow{EISSN: "3105-4242"},
			want: journalISSNs{ISSNE: "3105-4242"},
		},
		{
			name: "unhyphenated and lowercase input is normalized",
			row:  journalRow{PISSN: "09152245"},
			want: journalISSNs{ISSNL: "0915-2245", ISSNP: "0915-2245", LinkingISSNs: []string{"0915-2245"}},
		},
		{
			name: "invalid ISSNs are dropped",
			row:  journalRow{PISSN: "0915-2246", EISSN: "nope"},
			want: journalISSNs{},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.want, resolveISSNs(c.row))
		})
	}
}

func Test_journalSkipReason(t *testing.T) {
	require.Equal(t, "", journalSkipReason(journalRow{Title: "A"}, journalISSNs{ISSNE: "3105-4242"}))
	require.Equal(t, "no-valid-issn", journalSkipReason(journalRow{Title: "A"}, journalISSNs{}))
	require.Equal(t, "empty-title", journalSkipReason(journalRow{Title: "  "}, journalISSNs{ISSNE: "3105-4242"}))
}

func Test_journalToContainer(t *testing.T) {
	row := journalRow{
		Title:     "  Nihon   Shouni Jinzoubyou Gakkai zasshi ",
		Publisher: "The Japanese Society for Pediatric Nephrology",
		URL:       "http://www.jspn.jp/",
		DOAJURL:   "https://doaj.org/toc/4beb218f47f444b99031dac003e8e6c4",
		Country:   "Japan",
		AddedOn:   "2022-07-08T09:33:44Z",
	}
	issns := journalISSNs{ISSNL: "0915-2245", ISSNP: "0915-2245", ISSNE: "1881-3933"}

	c := journalToContainer(row, issns, "test-source")

	require.Equal(t, "Nihon Shouni Jinzoubyou Gakkai zasshi", c.Name)
	require.Equal(t, "journal", c.Type)
	require.Equal(t, "0915-2245", c.ISSNL)
	require.Equal(t, "0915-2245", c.ISSNP)
	require.Equal(t, "1881-3933", c.ISSNE)
	require.Equal(t, "test-source", c.Source)
	require.Equal(t, map[string]any{
		"doaj": map[string]any{
			"id":       "4beb218f47f444b99031dac003e8e6c4",
			"added_on": "2022-07-08T09:33:44Z",
		},
		"urls":         []string{"http://www.jspn.jp/"},
		"country_name": "Japan",
	}, c.Extra)
	require.True(t, indexing.PrepareFatcatContainerDoc(c).InDOAJ)
}

// A row with no DOAJ id or added date still gets extra.doaj, which is what
// marks the container in_doaj.
func Test_journalToContainer_AlwaysInDOAJ(t *testing.T) {
	c := journalToContainer(journalRow{Title: "A Journal"}, journalISSNs{ISSNE: "3105-4242"}, "src")

	require.Equal(t, map[string]any{"doaj": map[string]any{}}, c.Extra)
	require.True(t, indexing.PrepareFatcatContainerDoc(c).InDOAJ)
}

// fakeFatcat stands in for fatcat2 (container lookup and create), fatcat1
// (legacy lookup) and ES (indexing).
type fakeFatcat struct {
	mu sync.Mutex
	// existing maps "field=value" to a container ID.
	existing map[string]uuid.UUID
	lookups  []string
	created  []fatcat2.Container
	indexed  int
	legacy   int
}

func (f *fakeFatcat) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	switch {
	case r.Method == "GET" && r.URL.Path == "/fc2/container/lookup":
		key := r.URL.Query().Get("id_type") + "=" + r.URL.Query().Get("id_value")
		f.lookups = append(f.lookups, key)
		id, ok := f.existing[key]
		if !ok {
			w.WriteHeader(404)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"id": id})
	case r.Method == "POST" && r.URL.Path == "/fc2/container":
		var c fatcat2.Container
		bs, _ := io.ReadAll(r.Body)
		json.Unmarshal(bs, &c)
		f.created = append(f.created, c)
		w.WriteHeader(201)
	case strings.HasPrefix(r.URL.Path, "/fc1/"):
		f.legacy++
		w.WriteHeader(404)
	case strings.HasPrefix(r.URL.Path, "/es/"):
		f.indexed++
		w.WriteHeader(201)
	default:
		w.WriteHeader(500)
	}
}

func setupFakeFatcat(t *testing.T, existing map[string]uuid.UUID) *fakeFatcat {
	f := &fakeFatcat{existing: existing}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	viper.Set("fatcat2.endpoint", srv.URL+"/fc2")
	viper.Set("fatcat1.endpoint", srv.URL+"/fc1")
	viper.Set("indexing.elasticsearch_url", srv.URL+"/es")
	viper.Set("indexing.fatcat_container_ix", "fatcat_container")
	return f
}

func Test_importJournal_FindsExistingByAnyISSN(t *testing.T) {
	id := uuid.New()
	// A legacy container that only knows the journal's electronic ISSN, filed
	// under issne.
	f := setupFakeFatcat(t, map[string]uuid.UUID{"issne=1881-3933": id})

	outcome, c, err := importJournal(http.DefaultClient,
		journalRow{Title: "A Journal", PISSN: "0915-2245", EISSN: "1881-3933"}, "src")
	require.NoError(t, err)
	require.Equal(t, journalIgnored, outcome)
	require.Nil(t, c)
	require.Empty(t, f.created)
	require.Equal(t, []string{
		"issnl=0915-2245", "issnp=0915-2245", "issne=0915-2245",
		"issnl=1881-3933", "issnp=1881-3933", "issne=1881-3933",
	}, f.lookups)
}

func Test_importJournal_StopsAtFirstHit(t *testing.T) {
	f := setupFakeFatcat(t, map[string]uuid.UUID{"issnl=0915-2245": uuid.New()})

	outcome, _, err := importJournal(http.DefaultClient,
		journalRow{Title: "A Journal", PISSN: "0915-2245", EISSN: "1881-3933"}, "src")
	require.NoError(t, err)
	require.Equal(t, journalIgnored, outcome)
	require.Equal(t, []string{"issnl=0915-2245"}, f.lookups)
}

func Test_importJournal_CreatesNew(t *testing.T) {
	f := setupFakeFatcat(t, map[string]uuid.UUID{})

	outcome, c, err := importJournal(http.DefaultClient,
		journalRow{Title: "A Journal", Publisher: "Pub", PISSN: "0915-2245", EISSN: "1881-3933"}, "src")
	require.NoError(t, err)
	require.Equal(t, journalAdded, outcome)
	require.NotNil(t, c)
	require.Len(t, f.created, 1)
	require.Equal(t, "0915-2245", f.created[0].ISSNL)
	require.Equal(t, "1881-3933", f.created[0].ISSNE)
	require.Equal(t, "src", f.created[0].Source)
	require.Equal(t, 1, f.legacy, "a container with an ISSN-L is checked against fc1")
	require.Equal(t, 1, f.indexed)
}

// A journal too new for the ISSN-L table is still created, findable by the
// ISSNs it does have, and fc1 is not asked about an empty ISSN-L.
func Test_importJournal_CreatesNewWithoutISSNL(t *testing.T) {
	f := setupFakeFatcat(t, map[string]uuid.UUID{})

	outcome, c, err := importJournal(http.DefaultClient,
		journalRow{Title: "Forensic Insights", EISSN: "3105-4242"}, "src")
	require.NoError(t, err)
	require.Equal(t, journalAdded, outcome)
	require.Equal(t, "", c.ISSNL)
	require.Len(t, f.created, 1)
	require.Equal(t, "", f.created[0].ISSNL)
	require.Equal(t, "3105-4242", f.created[0].ISSNE)
	require.Equal(t, 0, f.legacy)
}

func Test_importJournal_Skips(t *testing.T) {
	f := setupFakeFatcat(t, map[string]uuid.UUID{})

	outcome, _, err := importJournal(http.DefaultClient,
		journalRow{Title: "A Journal", PISSN: "bogus"}, "src")
	require.NoError(t, err)
	require.Equal(t, journalSkipped, outcome)
	require.Empty(t, f.lookups)
}

// --- importJournalRows ----------------------------------------------------

func nopLogger() log.Logger {
	return log.NewStructuredLogger(slog.New(slog.DiscardHandler))
}

// scriptedImport returns each row's outcome from its Title, so a test reads
// as a list of what happened: "added", "ignored", "skipped", "err" (or
// "added-nol" for a container created without an ISSN-L).
func scriptedImport(seen *[]string) importFunc {
	return func(_ *http.Client, row journalRow, _ string) (journalOutcome, *fatcat2.Container, error) {
		*seen = append(*seen, row.Title)
		switch row.Title {
		case "added":
			return journalAdded, &fatcat2.Container{ISSNL: "0915-2245"}, nil
		case "added-nol":
			return journalAdded, &fatcat2.Container{}, nil
		case "ignored":
			return journalIgnored, nil, nil
		case "skipped":
			return journalSkipped, nil, nil
		}
		return "", nil, errors.New("boom")
	}
}

func rowsOf(titles ...string) []journalRow {
	out := make([]journalRow, len(titles))
	for i, t := range titles {
		out[i] = journalRow{Title: t}
	}
	return out
}

func Test_importJournalRows_Tallies(t *testing.T) {
	var seen []string
	var last importProgress
	counts, err := importJournalRows(context.Background(), nopLogger(), nil,
		rowsOf("added", "added-nol", "ignored", "ignored", "skipped", "err", "ignored"),
		"src", 3, importProgress{},
		func(p importProgress) { last = p },
		scriptedImport(&seen))
	require.NoError(t, err)
	require.Equal(t, JournalCounts{Rows: 7, Added: 2, AddedWithoutISSNL: 1, Ignored: 3, Skipped: 1, Errored: 1}, counts)
	require.Equal(t, importProgress{Next: 7, Counts: counts}, last)
}

func Test_importJournalRows_ResumesFromCheckpoint(t *testing.T) {
	var seen []string
	prior := JournalCounts{Rows: 2, Ignored: 2}
	counts, err := importJournalRows(context.Background(), nopLogger(), nil,
		rowsOf("ignored", "ignored", "added", "ignored"),
		"src", 3, importProgress{Next: 2, Counts: prior},
		func(importProgress) {},
		scriptedImport(&seen))
	require.NoError(t, err)
	require.Equal(t, []string{"added", "ignored"}, seen)
	require.Equal(t, JournalCounts{Rows: 4, Added: 1, Ignored: 3}, counts)
}

// A streak of failures aborts, checkpointing back to where the streak began
// so the retry redoes those rows instead of writing them off.
func Test_importJournalRows_StreakAbortsAndRewinds(t *testing.T) {
	var seen []string
	var last importProgress
	_, err := importJournalRows(context.Background(), nopLogger(), nil,
		rowsOf("ignored", "err", "ignored", "err", "err", "err", "ignored"),
		"src", 3, importProgress{},
		func(p importProgress) { last = p },
		scriptedImport(&seen))
	require.ErrorContains(t, err, "3 consecutive rows failed")
	require.Equal(t, importProgress{Next: 3, Counts: JournalCounts{Rows: 3, Ignored: 2, Errored: 1}}, last)
	require.Len(t, seen, 6, "rows after the abort are not attempted")
}
