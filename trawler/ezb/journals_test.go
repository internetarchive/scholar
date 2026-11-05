package ezb

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"git.archive.org/webgroup/scholar/trawler/containers"
	"git.archive.org/webgroup/scholar/trawler/containers/containerstest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Fixtures are real EZB records, trimmed. In the ISSN-L table:
//   - 0915-2245 (print) and 1881-3933 (online) share ISSN-L 0915-2245;
//   - Analytical Communications, 1359-7337 and 1364-5536, share 1359-7337,
//     and EZB lists it twice, split by access period;
//   - Chemical Communications lists its predecessors' print ISSNs, 0009-241X
//     and 0022-4936, each its own ISSN-L group, beside its own 1359-7345.

const nephrology = `{"publication_title": "Nihon Shouni Jinzoubyou Gakkai zasshi",
	"print_identifiers": ["0915-2245"], "online_identifiers": ["1881-3933"],
	"date_first_issue_online": 1988, "date_last_issue_online": "",
	"title_url": ["http://www.jspn.jp/"], "ezb_jour_id": 100, "zdb_id": "2448735-0",
	"publisher_name": "Japanese Society  for Pediatric Nephrology",
	"price_type_en": "free of charge", "languages": ["ja"]}`

const analComm1 = `{"publication_title": "Analytical Communications",
	"print_identifiers": ["1359-7337"], "online_identifiers": ["1364-5536"],
	"date_first_issue_online": 1997, "date_last_issue_online": 1999,
	"title_url": ["https://pubs.rsc.org/ac"], "ezb_jour_id": 4, "zdb_id": "1495496-5",
	"publisher_name": "Royal Society of Chemistry (RSC)", "price_type_en": "free of charge"}`

const analComm2 = `{"publication_title": "Analytical Communications (old)",
	"print_identifiers": ["1359-7337"], "online_identifiers": [],
	"date_first_issue_online": 1996, "date_last_issue_online": "1999",
	"title_url": [], "ezb_jour_id": 39960, "zdb_id": "1495496-5",
	"publisher_name": "RSC", "price_type_en": "subject to fee"}`

const chemComm = `{"publication_title": "Chemical Communications : ChemComm",
	"print_identifiers": ["0009-241X", "0022-4936", "1359-7345"], "online_identifiers": ["1364-548X"],
	"date_first_issue_online": 1996, "date_last_issue_online": "",
	"title_url": ["https://pubs.rsc.org/cc"], "ezb_jour_id": 6, "zdb_id": "1472881-3",
	"publisher_name": "Royal Society of Chemistry (RSC)", "price_type_en": "subject to fee"}`

const noISSN = `{"publication_title": "A Magazine", "print_identifiers": [], "online_identifiers": [],
	"date_first_issue_online": "", "date_last_issue_online": "", "title_url": [], "ezb_jour_id": 7}`

func list(recs ...string) string {
	return "[" + strings.Join(recs, ",") + "]"
}

func mustParse(t *testing.T, recs ...string) []journalRecord {
	out, err := parseJournals(strings.NewReader(list(recs...)))
	require.NoError(t, err)
	return out
}

func Test_parseJournals(t *testing.T) {
	recs := mustParse(t, nephrology, analComm2)
	require.Len(t, recs, 2)
	require.Equal(t, journalRecord{
		Title:       "Nihon Shouni Jinzoubyou Gakkai zasshi",
		PrintISSNs:  []string{"0915-2245"},
		OnlineISSNs: []string{"1881-3933"},
		FirstYear:   1988,
		TitleURLs:   []string{"http://www.jspn.jp/"},
		EZBID:       100,
		ZDBID:       "2448735-0",
		Publisher:   "Japanese Society  for Pediatric Nephrology",
		PriceType:   "free of charge",
	}, recs[0])
	// Years arrive as numbers or numeric strings.
	require.Equal(t, flexYear(1999), recs[1].LastYear)
}

// A handful of EZB titles are bare numbers, sent as JSON numbers.
func Test_parseJournals_NumericTitle(t *testing.T) {
	recs := mustParse(t, `{"publication_title": 1984, "ezb_jour_id": 1}`)
	require.Equal(t, flexString("1984"), recs[0].Title)
}

func Test_parseJournals_Rejects(t *testing.T) {
	for name, in := range map[string]string{
		"error page":   "<html><body>oops</body></html>",
		"not an array": `{"error": "busy"}`,
		"empty":        `[]`,
		"truncated":    "[" + nephrology + ",",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := parseJournals(strings.NewReader(in))
			require.Error(t, err)
		})
	}
}

func Test_groupJournals(t *testing.T) {
	rows := groupJournals(mustParse(t, nephrology, analComm1, chemComm, noISSN, analComm2))

	require.Len(t, rows, 4)

	require.Equal(t, "", rows[0].Skip)
	require.Len(t, rows[0].Records, 1)
	require.Equal(t, "0915-2245", rows[0].ISSNL)
	require.Equal(t, "0915-2245", rows[0].ISSNP)
	require.Equal(t, "1881-3933", rows[0].ISSNE)

	// EZB's two Analytical Communications records are one journal, in the
	// place of the first.
	require.Equal(t, "", rows[1].Skip)
	require.Len(t, rows[1].Records, 2)
	require.Equal(t, "1359-7337", rows[1].ISSNL)
	require.Equal(t, []string{"1359-7337", "1364-5536"}, rows[1].ISSNs)

	require.Equal(t, "multiple-issnl", rows[2].Skip)
	require.Equal(t, "no-valid-issn", rows[3].Skip)
}

// A record spanning several ISSN-L groups does not drag the journals whose
// ISSNs it lists into being skipped with it.
func Test_groupJournals_MultipleISSNLRecordJoinsNothing(t *testing.T) {
	own := `{"publication_title": "Chemical Communications", "print_identifiers": ["1359-7345"],
		"online_identifiers": ["1364-548X"], "ezb_jour_id": 8}`
	rows := groupJournals(mustParse(t, chemComm, own))

	require.Len(t, rows, 2)
	require.Equal(t, "multiple-issnl", rows[0].Skip)
	require.Equal(t, "", rows[1].Skip)
	require.Len(t, rows[1].Records, 1)
	require.Equal(t, "1359-7345", rows[1].ISSNL)
}

// The ISSNs too new for the table give no ISSN-L, but still join records, and
// can join two ISSN-L groups; then neither can be trusted.
func Test_groupJournals_AmbiguousThroughNewISSN(t *testing.T) {
	a := `{"publication_title": "A", "print_identifiers": ["0915-2245"], "online_identifiers": ["` +
		containerstest.NewISSN + `"], "ezb_jour_id": 1}`
	b := `{"publication_title": "B", "print_identifiers": ["1359-7337"], "online_identifiers": ["` +
		containerstest.NewISSN + `"], "ezb_jour_id": 2}`
	rows := groupJournals(mustParse(t, a, b))

	require.Len(t, rows, 2)
	require.Equal(t, "ambiguous-issnl", rows[0].Skip)
	require.Equal(t, "ambiguous-issnl", rows[1].Skip)
}

func Test_ezbExtra_Merged(t *testing.T) {
	row := groupJournals(mustParse(t, analComm2, analComm1))[0]
	require.Equal(t, map[string]any{
		"ids":        []int{4, 39960},
		"zdb_ids":    []string{"1495496-5"},
		"price_type": "mixed",
		"first_year": 1996,
		"last_year":  1999,
	}, ezbExtra(row))
}

// A journal EZB has as still running has no last year.
func Test_ezbExtra_Running(t *testing.T) {
	row := groupJournals(mustParse(t, nephrology))[0]
	require.Equal(t, map[string]any{
		"ids":        []int{100},
		"zdb_ids":    []string{"2448735-0"},
		"price_type": "free",
		"first_year": 1988,
	}, ezbExtra(row))
}

const openAccess = `{"publication_title": "Footprint : Delft School of Design Journal",
	"print_identifiers": [], "online_identifiers": ["1875-1490"], "ezb_jour_id": 140337,
	"price_type_en": "free of charge", "access_requirements_en": "Open Access",
	"categories_en": ["Open Access", "Diamond Open Access Journal", "Indexed in DOAJ"]}`

// Diamond and in DOAJ, but EZB's only access is through CEEOL, which wants a
// login.
const registrationRequired = `{"publication_title": "Polish Sociological Review",
	"print_identifiers": ["1231-1413"], "online_identifiers": ["2657-4276"], "ezb_jour_id": 58995,
	"price_type_en": "free of charge",
	"access_requirements_en": "Open access, but individual registration required",
	"categories_en": ["Diamond Open Access Journal", "Indexed in DDH", "Indexed in DOAJ"]}`

func Test_ezbExtra_OpenAccess(t *testing.T) {
	row := groupJournals(mustParse(t, openAccess))[0]
	require.Equal(t, map[string]any{
		"ids":        []int{140337},
		"price_type": "free",
		"oa":         true,
		"diamond":    true,
	}, ezbExtra(row))
}

func Test_ezbExtra_RegistrationRequiredIsNotOA(t *testing.T) {
	extra := ezbExtra(groupJournals(mustParse(t, registrationRequired))[0])
	_, oa := extra["oa"]
	require.False(t, oa)
	require.Equal(t, true, extra["diamond"], "EZB's diamond label is still recorded")
}

// Either of EZB's two OA labels is enough.
func Test_journalRecord_isOA(t *testing.T) {
	require.True(t, journalRecord{AccessRequirements: "Open Access"}.isOA())
	require.True(t, journalRecord{Categories: []string{"Indexed in DOAJ", "Open Access"}}.isOA())
	require.False(t, journalRecord{AccessRequirements: "Free access, but probably without further rights of use"}.isOA())
	require.False(t, journalRecord{Categories: []string{"Journal without APC"}}.isOA())
}

// A journal split across records is only OA if every record says so.
func Test_ezbExtra_MergedOAMustAgree(t *testing.T) {
	oaHalf := strings.Replace(analComm1, `"price_type_en"`,
		`"access_requirements_en": "Open Access", "categories_en": ["Open Access"], "price_type_en"`, 1)

	extra := ezbExtra(groupJournals(mustParse(t, oaHalf, analComm2))[0])
	_, oa := extra["oa"]
	require.False(t, oa)

	both := strings.Replace(analComm2, `"price_type_en"`, `"access_requirements_en": "Open Access", "price_type_en"`, 1)
	extra = ezbExtra(groupJournals(mustParse(t, oaHalf, both))[0])
	require.Equal(t, true, extra["oa"])
}

func Test_TitleAndPublisherHTMLEntities(t *testing.T) {
	row := groupJournals(mustParse(t, `{"publication_title": "Psychologia Spo&#322;eczna",
		"print_identifiers": ["1896-1800"], "ezb_jour_id": 183771,
		"publisher_name": "Bilimler I&amp;#351;&amp;#305;&#287;&#305; ; CEEOL"}`))[0]

	require.Equal(t, "Psychologia Społeczna", row.title())
	require.Equal(t, "Bilimler Işığı ; CEEOL", row.publisher(), "escaped twice")
}

// EZB's "publisher could not be determined" is not filled in as a publisher.
func Test_PlaceholderPublisher(t *testing.T) {
	for _, p := range []string{"Verlag nicht ermittelbar", "[Verlag nicht ermittelbar]"} {
		row := groupJournals(mustParse(t, `{"publication_title": "A", "print_identifiers": ["0915-2245"],
			"ezb_jour_id": 1, "publisher_name": "`+p+`"}`))[0]
		require.Equal(t, "", row.publisher(), p)

		doc := map[string]any{"issnl": "0915-2245"}
		row.Update(doc)
		_, has := doc["publisher"]
		require.False(t, has, p)
	}
}

// EZB cuts some long titles off mid-entity.
func Test_unescapeHTML_DanglingEntity(t *testing.T) {
	require.Equal(t, "Journal = المجلة", unescapeHTML("Journal = &#1575;&#1604;&#1605;&#1580;&#1604;&#1577;&#"))
	require.Equal(t, "Science & Society", unescapeHTML("Science &amp; Society"))
	require.Equal(t, "R&D Notes", unescapeHTML("R&D Notes"))
}

func Test_Lookup(t *testing.T) {
	lookup, skip := groupJournals(mustParse(t, nephrology))[0].Lookup()
	require.Equal(t, "", skip)
	require.Equal(t, []string{"0915-2245", "0915-2245", "1881-3933"}, lookup)

	_, skip = groupJournals(mustParse(t, chemComm))[0].Lookup()
	require.Equal(t, "multiple-issnl", skip)
}

// A merged group takes its publisher and homepage from the record EZB has
// latest.
func Test_MergedGroupPrimary(t *testing.T) {
	row := groupJournals(mustParse(t, analComm2, analComm1))[0]

	require.Equal(t, "Analytical Communications", row.title(), "both ended in 1999; the tie goes to the lower EZB ID")
	require.Equal(t, "Royal Society of Chemistry (RSC)", row.publisher())
	require.Equal(t, "https://pubs.rsc.org/ac", row.homepage())
	require.Equal(t, "1364-5536", row.ISSNE)
}

// asServed round-trips a doc through JSON, as fc2 would serve it.
func asServed(t *testing.T, doc map[string]any) map[string]any {
	bs, err := json.Marshal(doc)
	require.NoError(t, err)
	out := map[string]any{}
	require.NoError(t, json.Unmarshal(bs, &out))
	return out
}

func Test_Update_FillsOnlyBlanks(t *testing.T) {
	row := groupJournals(mustParse(t, nephrology))[0]
	doc := asServed(t, map[string]any{
		"name":      "Japanese Journal of Pediatric Nephrology",
		"publisher": "",
		"issnl":     "0915-2245",
		"extra": map[string]any{
			"urls": []string{"https://curated.example/"},
			"ezb":  map[string]any{"ezb_id": 100, "color": "green"},
		},
	})

	row.Update(doc)

	require.Equal(t, "Japanese Journal of Pediatric Nephrology", doc["name"], "names are never overwritten")
	require.Equal(t, "Japanese Society for Pediatric Nephrology", doc["publisher"])
	require.Equal(t, "0915-2245", doc["issnp"], "an ISSN-L equal to the print ISSN does not stop it being filled")
	require.Equal(t, "1881-3933", doc["issne"])
	extra := doc["extra"].(map[string]any)
	require.Equal(t, []any{"https://curated.example/"}, extra["urls"], "existing homepages are kept")
	require.Equal(t, ezbExtra(row), extra["ezb"], "chocula's old extra.ezb is replaced")
}

func Test_Update_DoesNotFileAnISSNTwice(t *testing.T) {
	row := groupJournals(mustParse(t, nephrology))[0]
	// The container already has EZB's print ISSN, filed as electronic.
	doc := asServed(t, map[string]any{"issne": "0915-2245", "extra": nil})

	row.Update(doc)

	_, hasP := doc["issnp"]
	require.False(t, hasP)
	require.Equal(t, "0915-2245", doc["issne"])
	require.Equal(t, []any{"http://www.jspn.jp/"}, doc["extra"].(map[string]any)["urls"])
}

// UpdateExisting only writes a container that Update changes; a re-run with the
// same list must change nothing.
func Test_UpdateExisting_SecondRunIsQuiet(t *testing.T) {
	id := uuid.New()
	f := containerstest.SetupFatcat(t, map[string]uuid.UUID{"issnl=0915-2245": id})
	f.Docs[id] = map[string]any{
		"id": id.String(), "name": "Nihon Shouni Jinzoubyou Gakkai zasshi",
		"issnl": "0915-2245", "source": "chocula", "created": "2019-01-29T00:00:00Z",
		"wikidata_qid": "Q1", "extra": map[string]any{"doaj": map[string]any{}},
	}
	row := groupJournals(mustParse(t, nephrology))[0]

	outcome, _, err := containers.UpdateExisting(http.DefaultClient, row, false)
	require.NoError(t, err)
	require.Equal(t, containers.Updated, outcome)
	require.Equal(t, "chocula", f.Put[0]["source"], "an update keeps the container's source")
	require.Equal(t, "Q1", f.Put[0]["wikidata_qid"])

	outcome, _, err = containers.UpdateExisting(http.DefaultClient, row, false)
	require.NoError(t, err)
	require.Equal(t, containers.Unchanged, outcome)
	require.Len(t, f.Put, 1)
}
