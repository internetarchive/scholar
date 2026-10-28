package doaj

import (
	"net/http"
	"strings"
	"testing"

	"git.archive.org/webgroup/scholar/trawler/containers"
	"git.archive.org/webgroup/scholar/trawler/containers/containerstest"
	"git.archive.org/webgroup/scholar/trawler/indexing"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

const journalCSVHeader = "\ufeffJournal title,Journal URL,URL in DOAJ,Alternative title," +
	"Journal ISSN (print version),Journal EISSN (online version),Publisher,Country of publisher,Added on Date\n"

func Test_parseJournalCSV(t *testing.T) {
	in := journalCSVHeader +
		`Nihon Shouni Jinzoubyou Gakkai zasshi,http://www.jspn.jp/,https://doaj.org/toc/4beb218f47f444b99031dac003e8e6c4,"Japanese Journal of Pediatric Nephrology",0915-2245,1881-3933,"The Japanese Society, for Pediatric Nephrology",Japan,2022-07-08T09:33:44Z` + "\n" +
		`Forensic Insights,,https://doaj.org/toc/abc,,,` + containerstest.NewISSN + `,Someone,India,2026-09-01T00:00:00Z` + "\n"

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
	require.Equal(t, containerstest.NewISSN, rows[1].EISSN)
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
			row:  journalRow{EISSN: containerstest.NewISSN},
			want: journalISSNs{ISSNE: containerstest.NewISSN},
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
	require.Equal(t, "", journalSkipReason(journalRow{Title: "A"}, journalISSNs{ISSNE: containerstest.NewISSN}))
	require.Equal(t, "no-valid-issn", journalSkipReason(journalRow{Title: "A"}, journalISSNs{}))
	require.Equal(t, "empty-title", journalSkipReason(journalRow{Title: "  "}, journalISSNs{ISSNE: containerstest.NewISSN}))
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
	c := journalToContainer(journalRow{Title: "A Journal"}, journalISSNs{ISSNE: containerstest.NewISSN}, "src")

	require.Equal(t, map[string]any{"doaj": map[string]any{}}, c.Extra)
	require.True(t, indexing.PrepareFatcatContainerDoc(c).InDOAJ)
}

func Test_Ensure_JournalFindsExistingByAnyISSN(t *testing.T) {
	id := uuid.New()
	// A legacy container that only knows the journal's electronic ISSN, filed
	// under issne.
	f := containerstest.SetupFatcat(t, map[string]uuid.UUID{"issne=1881-3933": id})

	outcome, c, err := containers.Ensure(http.DefaultClient,
		journalRow{Title: "A Journal", PISSN: "0915-2245", EISSN: "1881-3933"}, "src")
	require.NoError(t, err)
	require.Equal(t, containers.Ignored, outcome)
	require.Nil(t, c)
	require.Empty(t, f.Created)
	require.Equal(t, []string{
		"issnl=0915-2245", "issnp=0915-2245", "issne=0915-2245",
		"issnl=1881-3933", "issnp=1881-3933", "issne=1881-3933",
	}, f.Lookups)
}

func Test_Ensure_JournalStopsAtFirstHit(t *testing.T) {
	f := containerstest.SetupFatcat(t, map[string]uuid.UUID{"issnl=0915-2245": uuid.New()})

	outcome, _, err := containers.Ensure(http.DefaultClient,
		journalRow{Title: "A Journal", PISSN: "0915-2245", EISSN: "1881-3933"}, "src")
	require.NoError(t, err)
	require.Equal(t, containers.Ignored, outcome)
	require.Equal(t, []string{"issnl=0915-2245"}, f.Lookups)
}

func Test_Ensure_JournalCreatesNew(t *testing.T) {
	f := containerstest.SetupFatcat(t, map[string]uuid.UUID{})

	outcome, c, err := containers.Ensure(http.DefaultClient,
		journalRow{Title: "A Journal", Publisher: "Pub", PISSN: "0915-2245", EISSN: "1881-3933"}, "src")
	require.NoError(t, err)
	require.Equal(t, containers.Added, outcome)
	require.NotNil(t, c)
	require.Len(t, f.Created, 1)
	require.Equal(t, "0915-2245", f.Created[0].ISSNL)
	require.Equal(t, "1881-3933", f.Created[0].ISSNE)
	require.Equal(t, "src", f.Created[0].Source)
	require.Equal(t, 1, f.Legacy, "a container with an ISSN-L is checked against fc1")
	require.Equal(t, 1, f.Indexed)
}

// A journal too new for the ISSN-L table is still created, findable by the
// ISSNs it does have, and fc1 is not asked about an empty ISSN-L.
func Test_Ensure_JournalCreatesNewWithoutISSNL(t *testing.T) {
	f := containerstest.SetupFatcat(t, map[string]uuid.UUID{})

	outcome, c, err := containers.Ensure(http.DefaultClient,
		journalRow{Title: "Forensic Insights", EISSN: containerstest.NewISSN}, "src")
	require.NoError(t, err)
	require.Equal(t, containers.Added, outcome)
	require.Equal(t, "", c.ISSNL)
	require.Len(t, f.Created, 1)
	require.Equal(t, "", f.Created[0].ISSNL)
	require.Equal(t, containerstest.NewISSN, f.Created[0].ISSNE)
	require.Equal(t, 0, f.Legacy)
}

func Test_Ensure_JournalSkips(t *testing.T) {
	f := containerstest.SetupFatcat(t, map[string]uuid.UUID{})

	outcome, _, err := containers.Ensure(http.DefaultClient,
		journalRow{Title: "A Journal", PISSN: "bogus"}, "src")
	require.NoError(t, err)
	require.Equal(t, containers.Skipped, outcome)
	require.Empty(t, f.Lookups)
}
