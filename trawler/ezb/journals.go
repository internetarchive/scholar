package ezb

// EZB backfill: bring fatcat2's existing containers up to date from a dump of
// the Electronic Journals Library's journal list
// (https://ezb-api.ur.de/journals; CC0, regenerated daily). It is a one-off,
// run by hand (trawler ezb-backfill), and never creates a container: EZB
// catalogues serials far more broadly than fatcat's idea of a journal, so it
// only enriches journals fatcat already has.
//
// EZB is not one record per journal. About one record in six shares an ISSN
// with another: EZB splits a journal into several records by access period, or
// files a series' volumes separately under the series ISSN. And a record may
// fold a journal's earlier titles in, listing ISSNs from several ISSN-L groups.
// So records are first grouped by the ISSN-L their ISSNs share, and:
//
//   - a group, of one record or several merged, updates the container that
//     strongly matches it (see containers.StrongMatch);
//   - a record spanning several ISSN-L groups, or with no valid ISSN, is
//     skipped: there is no one container it belongs to.
//
// An update writes extra.ezb, which EZB is the authority for, and fills in
// publisher, ISSNs and homepage only where the container has none. It never
// overwrites a value someone may have curated.

import (
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"git.archive.org/webgroup/scholar/trawler/containers"
	"git.archive.org/webgroup/scholar/trawler/issn"
)

// flexString is a JSON string that EZB occasionally sends as a number (a few
// journals are titled with a year).
type flexString string

func (s *flexString) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*s = ""
		return nil
	}
	var str string
	if err := json.Unmarshal(b, &str); err == nil {
		*s = flexString(str)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return fmt.Errorf("neither string nor number: %s", b)
	}
	*s = flexString(n.String())
	return nil
}

// flexYear is a year EZB sends as a number, a numeric string, or "" for none.
// Zero means none.
type flexYear int

func (y *flexYear) UnmarshalJSON(b []byte) error {
	var n int
	if err := json.Unmarshal(b, &n); err == nil {
		*y = flexYear(n)
		return nil
	}
	var str string
	if err := json.Unmarshal(b, &str); err != nil {
		*y = 0
		return nil
	}
	// Anything that is not a plain year is treated as no year rather than
	// failing the whole file over one odd record.
	n, err := strconv.Atoi(strings.TrimSpace(str))
	if err != nil {
		n = 0
	}
	*y = flexYear(n)
	return nil
}

// journalRecord is the subset of an EZB journal record we use.
type journalRecord struct {
	Title       flexString `json:"publication_title"`
	PrintISSNs  []string   `json:"print_identifiers"`
	OnlineISSNs []string   `json:"online_identifiers"`
	FirstYear   flexYear   `json:"date_first_issue_online"`
	// LastYear is zero for a journal EZB has as still running.
	LastYear  flexYear `json:"date_last_issue_online"`
	TitleURLs []string `json:"title_url"`
	EZBID     int      `json:"ezb_jour_id"`
	ZDBID     string   `json:"zdb_id"`
	Publisher string   `json:"publisher_name"`
	PriceType string   `json:"price_type_en"`
	// AccessRequirements and Categories carry EZB's open access labels.
	AccessRequirements string   `json:"access_requirements_en"`
	Categories         []string `json:"categories_en"`
}

// isOA reports whether EZB labels rec open access. EZB says so in both fields,
// and they agree; either is taken. "Open access, but individual registration
// required" is not: those are mostly copies on registration-walled aggregators
// (CEEOL), whatever the journal's own terms.
func (rec journalRecord) isOA() bool {
	return strings.TrimSpace(rec.AccessRequirements) == "Open Access" ||
		slices.Contains(rec.Categories, "Open Access")
}

func (rec journalRecord) isDiamond() bool {
	return slices.Contains(rec.Categories, "Diamond Open Access Journal")
}

// danglingEntity is the start of an entity left at the end of a title EZB has
// cut off at a fixed length.
var danglingEntity = regexp.MustCompile(`&#?[0-9A-Za-z]*$`)

// unescapeHTML decodes the HTML entities EZB leaves in titles and publishers
// (Spo&#322;eczna), some of them escaped twice (I&amp;#351;).
func unescapeHTML(s string) string {
	for range 2 {
		u := html.UnescapeString(s)
		if u == s {
			break
		}
		s = u
	}
	return danglingEntity.ReplaceAllString(s, "")
}

// parseJournals reads EZB's journal list, a single JSON array. It is ~140MB
// and ~125k records; only the fields above are kept, so holding them all is
// cheap, and having every record up front is what lets them be grouped and an
// import resume from a row index.
func parseJournals(r io.Reader) ([]journalRecord, error) {
	dec := json.NewDecoder(r)
	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("could not read journal list: %w", err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '[' {
		return nil, fmt.Errorf("journal list is not a JSON array (starts with %v)", tok)
	}

	out := []journalRecord{}
	for dec.More() {
		var rec journalRecord
		if err := dec.Decode(&rec); err != nil {
			return nil, fmt.Errorf("could not read record %d: %w", len(out)+1, err)
		}
		out = append(out, rec)
	}
	if _, err := dec.Token(); err != nil {
		return nil, fmt.Errorf("journal list is truncated after %d records: %w", len(out), err)
	}
	// EZB lists ~125k journals. An empty list is a broken export, and importing
	// it would just be a run that does nothing, so say so instead.
	if len(out) == 0 {
		return nil, errors.New("journal list is empty")
	}

	return out, nil
}

// recordISSNs is a record's ISSNs, normalized, and the ISSN-Ls they map to.
type recordISSNs struct {
	// Print and Online are the record's valid ISSNs of each kind, in its order.
	Print  []string
	Online []string
	// LinkingISSNs is every distinct ISSN-L the record's ISSNs map to.
	LinkingISSNs []string
}

func (ri recordISSNs) all() []string {
	out := []string{}
	for _, i := range append(append([]string{}, ri.Print...), ri.Online...) {
		if !slices.Contains(out, i) {
			out = append(out, i)
		}
	}
	return out
}

func normalizeAll(raw []string) []string {
	out := []string{}
	for _, r := range raw {
		if i := issn.Normalize(r); i != "" && !slices.Contains(out, i) {
			out = append(out, i)
		}
	}
	return out
}

func resolveRecordISSNs(rec journalRecord) recordISSNs {
	out := recordISSNs{
		Print:  normalizeAll(rec.PrintISSNs),
		Online: normalizeAll(rec.OnlineISSNs),
	}
	out.LinkingISSNs = issn.LinkingISSNs(out.all()...)
	return out
}

// journalRow is one journal: a single EZB record, or several that share an
// ISSN-L, merged.
type journalRow struct {
	// Records is the journal's records, in list order. Never empty.
	Records []journalRecord
	// ISSNL is the ISSN-L the records share, or empty for a journal too new for
	// our ISSN-L table.
	ISSNL string
	// ISSNP and ISSNE are the journal's print and electronic ISSN, if EZB gives
	// one in its ISSN-L group.
	ISSNP string
	ISSNE string
	// ISSNs is every valid ISSN across the records.
	ISSNs []string
	// Skip, if set, is why this row can neither create nor update a container.
	Skip string
}

// groupJournals turns EZB's records into one row per journal. Records sharing
// any ISSN are joined; a joined group whose ISSNs reach more than one ISSN-L,
// and a record that does on its own, become skipped rows, one per record, so
// the tally still counts every record. Rows come out in the order of their
// first record, so an import's row indexes are stable for a given file.
func groupJournals(recs []journalRecord) []journalRow {
	resolved := make([]recordISSNs, len(recs))
	parent := make([]int, len(recs))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}

	// Join records that share an ISSN. A record spanning several ISSN-L groups
	// joins nothing: it is skipped, and letting it join would drag the groups it
	// spans into skipping with it.
	byISSN := map[string]int{}
	for i, rec := range recs {
		resolved[i] = resolveRecordISSNs(rec)
		if len(resolved[i].LinkingISSNs) > 1 {
			continue
		}
		for _, x := range resolved[i].all() {
			if j, ok := byISSN[x]; ok {
				parent[find(i)] = find(j)
			} else {
				byISSN[x] = i
			}
		}
	}

	// Each output slot is either a lone skipped record or a group, placed where
	// the group's first record was.
	type slot struct {
		skipped *journalRow
		root    int
	}
	slots := []slot{}
	members := map[int][]int{}
	for i, rec := range recs {
		reason := ""
		switch {
		case len(resolved[i].all()) == 0:
			reason = "no-valid-issn"
		case len(resolved[i].LinkingISSNs) > 1:
			reason = "multiple-issnl"
		}
		if reason != "" {
			slots = append(slots, slot{skipped: &journalRow{Records: []journalRecord{rec}, Skip: reason}})
			continue
		}
		root := find(i)
		if _, ok := members[root]; !ok {
			slots = append(slots, slot{root: root})
		}
		members[root] = append(members[root], i)
	}

	out := []journalRow{}
	for _, s := range slots {
		if s.skipped != nil {
			out = append(out, *s.skipped)
			continue
		}
		row := buildRow(recs, resolved, members[s.root])
		if row.Skip != "" {
			// Each record is its own skipped row, as a lone one would be.
			for _, rec := range row.Records {
				out = append(out, journalRow{Records: []journalRecord{rec}, Skip: row.Skip})
			}
			continue
		}
		out = append(out, row)
	}
	return out
}

// buildRow merges a group of records sharing ISSNs into one row.
func buildRow(recs []journalRecord, resolved []recordISSNs, idx []int) journalRow {
	row := journalRow{}
	linking := []string{}
	for _, i := range idx {
		row.Records = append(row.Records, recs[i])
		for _, x := range resolved[i].all() {
			if !slices.Contains(row.ISSNs, x) {
				row.ISSNs = append(row.ISSNs, x)
			}
		}
		for _, l := range resolved[i].LinkingISSNs {
			if !slices.Contains(linking, l) {
				linking = append(linking, l)
			}
		}
	}
	// Records joined through an ISSN too new for the table can still each carry
	// a different ISSN-L.
	if len(linking) > 1 {
		row.Skip = "ambiguous-issnl"
		return row
	}
	if len(linking) == 1 {
		row.ISSNL = linking[0]
	}

	// Print and electronic ISSN come from the primary record where it has them,
	// and are only taken from the journal's own ISSN-L group: a record listing
	// a predecessor's print ISSN must not file it as this journal's.
	inGroup := func(x string) bool {
		return row.ISSNL == "" || issn.ISSN2ISSNL(x) == row.ISSNL
	}
	p := primary(row.Records)
	ordered := []int{}
	for k, i := range idx {
		if k == p {
			ordered = append([]int{i}, ordered...)
		} else {
			ordered = append(ordered, i)
		}
	}
	for _, i := range ordered {
		for _, x := range resolved[i].Print {
			if row.ISSNP == "" && inGroup(x) {
				row.ISSNP = x
			}
		}
		for _, x := range resolved[i].Online {
			if row.ISSNE == "" && inGroup(x) && x != row.ISSNP {
				row.ISSNE = x
			}
		}
	}
	return row
}

// primary returns the index of the record that speaks for the group's title,
// publisher and homepage: the one EZB has as still running, latest started;
// failing that, the one that ran latest; ties to the lowest EZB ID.
func primary(recs []journalRecord) int {
	best := 0
	better := func(a, b journalRecord) bool {
		aOpen, bOpen := a.LastYear == 0, b.LastYear == 0
		switch {
		case aOpen != bOpen:
			return aOpen
		case aOpen && a.FirstYear != b.FirstYear:
			return a.FirstYear > b.FirstYear
		case !aOpen && a.LastYear != b.LastYear:
			return a.LastYear > b.LastYear
		}
		return a.EZBID < b.EZBID
	}
	for i := range recs {
		if better(recs[i], recs[best]) {
			best = i
		}
	}
	return best
}

// priceTypes maps EZB's English price types to the slugs stored in extra.ezb.
// Anything else is left out rather than stored as free text whose wording
// could change under us.
var priceTypes = map[string]string{
	"free of charge":    "free",
	"subject to fee":    "fee",
	"pay per view only": "pay-per-view",
}

// ezbExtra is what goes in a container's extra.ezb for row. It is rebuilt from
// the records on every run, so it must come out identical from identical
// records: lists are sorted, and nothing in it depends on the run.
func ezbExtra(row journalRow) map[string]any {
	ids := []int{}
	zdb := []string{}
	prices := []string{}
	first, last, open := 0, 0, false
	// A journal split across records is only labelled where every record
	// agrees, as with a mixed price type.
	oa, diamond := true, true
	for _, rec := range row.Records {
		oa = oa && rec.isOA()
		diamond = diamond && rec.isDiamond()
		ids = append(ids, rec.EZBID)
		if z := strings.TrimSpace(rec.ZDBID); z != "" && !slices.Contains(zdb, z) {
			zdb = append(zdb, z)
		}
		if p, ok := priceTypes[strings.TrimSpace(rec.PriceType)]; ok && !slices.Contains(prices, p) {
			prices = append(prices, p)
		}
		if y := int(rec.FirstYear); y > 0 && (first == 0 || y < first) {
			first = y
		}
		if rec.LastYear == 0 {
			open = true
		} else if int(rec.LastYear) > last {
			last = int(rec.LastYear)
		}
	}
	slices.Sort(ids)
	slices.Sort(zdb)

	out := map[string]any{"ids": ids}
	if len(zdb) > 0 {
		out["zdb_ids"] = zdb
	}
	switch len(prices) {
	case 0:
	case 1:
		out["price_type"] = prices[0]
	default:
		// Free for some years and paid for others, typically.
		out["price_type"] = "mixed"
	}
	if first > 0 {
		out["first_year"] = first
	}
	if !open && last > 0 {
		out["last_year"] = last
	}
	// Only ever true: an absent key and false mean the same, and leaving it out
	// keeps extra.ezb small for the ~100k journals that are neither.
	if oa {
		out["oa"] = true
	}
	if diamond {
		out["diamond"] = true
	}
	return out
}

// homepage is the primary record's first URL, if it has one.
func (row journalRow) homepage() string {
	for _, u := range row.Records[primary(row.Records)].TitleURLs {
		if u = strings.TrimSpace(u); u != "" {
			return u
		}
	}
	return ""
}

func (row journalRow) title() string {
	return containers.CleanName(unescapeHTML(string(row.Records[primary(row.Records)].Title)))
}

// placeholderPublishers are what EZB says for a publisher it does not know,
// lowercased and without the square brackets cataloguers sometimes add. Filled
// into a container, they would read as a publisher's name.
var placeholderPublishers = []string{
	"verlag nicht ermittelbar", // "publisher could not be determined"
}

func (row journalRow) publisher() string {
	p := containers.CleanName(unescapeHTML(row.Records[primary(row.Records)].Publisher))
	if slices.Contains(placeholderPublishers, strings.ToLower(strings.Trim(p, "[] "))) {
		return ""
	}
	return p
}

// Lookup implements containers.Updater: every ISSN the journal has, ISSN-L
// first.
func (row journalRow) Lookup() ([]string, string) {
	if row.Skip != "" {
		return nil, row.Skip
	}
	lookup := []string{}
	if row.ISSNL != "" {
		lookup = append(lookup, row.ISSNL)
	}
	return append(lookup, row.ISSNs...), ""
}

// Update implements containers.Updater. extra.ezb is replaced outright; every
// other field is only filled where the container has nothing.
func (row journalRow) Update(doc map[string]any) {
	extra, _ := doc["extra"].(map[string]any)
	if extra == nil {
		extra = map[string]any{}
		doc["extra"] = extra
	}
	extra["ezb"] = ezbExtra(row)

	// A container's homepages may have been curated, or gathered from several
	// upstreams; EZB's is only offered to one that has none.
	if u := row.homepage(); u != "" {
		if urls, _ := extra["urls"].([]any); len(urls) == 0 {
			extra["urls"] = []any{u}
		}
	}

	fillBlank(doc, "publisher", row.publisher())
	fillBlank(doc, "issnl", row.ISSNL)
	fillISSN(doc, "issnp", "issne", row.ISSNP)
	fillISSN(doc, "issne", "issnp", row.ISSNE)
}

func fillBlank(doc map[string]any, key, value string) {
	if value == "" {
		return
	}
	if cur, _ := doc[key].(string); cur == "" {
		doc[key] = value
	}
}

// fillISSN fills the empty print or electronic ISSN column key, unless the
// container already files value under the other one: EZB's print/online labels
// are not always right, and one ISSN in both columns is worse than one in the
// wrong column. (An ISSN-L equal to it is normal, and no obstacle.)
func fillISSN(doc map[string]any, key, other, value string) {
	if value == "" {
		return
	}
	if cur, _ := doc[other].(string); issn.Normalize(cur) == value {
		return
	}
	fillBlank(doc, key, value)
}

func (row journalRow) LogValues() []any {
	ids := []int{}
	for _, rec := range row.Records {
		ids = append(ids, rec.EZBID)
	}
	return []any{"ezb_ids", ids, "title", row.title()}
}
