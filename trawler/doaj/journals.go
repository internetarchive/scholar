package doaj

// DOAJ journal import: turn rows of DOAJ's journal CSV (https://doaj.org/csv)
// into fatcat2 containers, so that article importers resolving a container by
// ISSN find a journal DOAJ knows about instead of dropping the link.
//
// Only containers that fatcat does not already have are created. Existing ones
// are left untouched: they may have been curated, and DOAJ is not the authority
// for most of them.

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	"git.archive.org/webgroup/scholar/trawler/cleaning"
	"git.archive.org/webgroup/scholar/trawler/fatcat2"
	"git.archive.org/webgroup/scholar/trawler/indexing"
	"git.archive.org/webgroup/scholar/trawler/issn"
	"github.com/google/uuid"
	"github.com/spf13/viper"
)

// The CSV's columns are found by header name rather than position. DOAJ adds
// columns now and then, and has reordered them before.
const (
	colTitle     = "Journal title"
	colPISSN     = "Journal ISSN (print version)"
	colEISSN     = "Journal EISSN (online version)"
	colPublisher = "Publisher"
	colURL       = "Journal URL"
	colDOAJURL   = "URL in DOAJ"
	colCountry   = "Country of publisher"
	colAddedOn   = "Added on Date"
)

// requiredColumns are the ones we cannot make a findable container without. A
// CSV missing any of them is not one we know how to read, and is rejected
// whole rather than imported as a pile of skipped rows.
var requiredColumns = []string{colTitle, colPISSN, colEISSN}

// journalRow is the subset of a DOAJ journal CSV row we use.
type journalRow struct {
	Title     string
	PISSN     string
	EISSN     string
	Publisher string
	URL       string
	DOAJURL   string
	Country   string
	AddedOn   string
}

// parseJournalCSV reads a whole DOAJ journal CSV. It is ~25MB and ~25k rows,
// small enough to hold in memory, and having every row up front is what lets
// an import resume from a row index.
func parseJournalCSV(r io.Reader) ([]journalRow, error) {
	cr := csv.NewReader(r)
	cr.ReuseRecord = true

	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("could not read csv header: %w", err)
	}

	cols := map[string]int{}
	for i, h := range header {
		// The file is served with a UTF-8 BOM, which lands on the first header.
		h = strings.TrimPrefix(h, "\ufeff")
		cols[strings.TrimSpace(h)] = i
	}
	for _, c := range requiredColumns {
		if _, ok := cols[c]; !ok {
			return nil, fmt.Errorf("csv is missing required column %q", c)
		}
	}

	get := func(rec []string, col string) string {
		i, ok := cols[col]
		if !ok || i >= len(rec) {
			return ""
		}
		return strings.TrimSpace(rec[i])
	}

	out := []journalRow{}
	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("could not read csv row %d: %w", len(out)+1, err)
		}
		out = append(out, journalRow{
			Title:     get(rec, colTitle),
			PISSN:     get(rec, colPISSN),
			EISSN:     get(rec, colEISSN),
			Publisher: get(rec, colPublisher),
			URL:       get(rec, colURL),
			DOAJURL:   get(rec, colDOAJURL),
			Country:   get(rec, colCountry),
			AddedOn:   get(rec, colAddedOn),
		})
	}

	return out, nil
}

// journalISSNs is a row's ISSNs, normalized, plus the ISSN-L they resolve to.
type journalISSNs struct {
	ISSNL string
	ISSNP string
	ISSNE string
	// LinkingISSNs is every distinct ISSN-L the row's ISSNs map to. Usually one;
	// a few hundred DOAJ journals have print and online editions registered
	// under different ISSN-Ls, and either may be the one fatcat already has.
	LinkingISSNs []string
}

// resolveISSNs normalizes a row's ISSNs and works out its ISSN-L.
//
// The ISSN-L is never guessed. A journal too new for our ISSN-L table gets
// none, and is findable by its print and electronic ISSNs instead; inventing
// one would plant a linking ISSN that may later turn out to belong to a
// different group.
//
// Where print and online map to different ISSN-Ls, the print one wins. That is
// what fatcat's original DOAJ-derived containers did, so it is the choice most
// likely to agree with what is already there.
func resolveISSNs(row journalRow) journalISSNs {
	out := journalISSNs{
		ISSNP: issn.Normalize(row.PISSN),
		ISSNE: issn.Normalize(row.EISSN),
	}
	for _, i := range []string{out.ISSNP, out.ISSNE} {
		if i == "" {
			continue
		}
		l := issn.ISSN2ISSNL(i)
		if l == "" {
			continue
		}
		if out.ISSNL == "" {
			out.ISSNL = l
		}
		if !slices.Contains(out.LinkingISSNs, l) {
			out.LinkingISSNs = append(out.LinkingISSNs, l)
		}
	}
	return out
}

// cleanName tidies a journal or publisher name for fatcat. CleanString fixes
// punctuation but leaves whitespace alone, and DOAJ names carry stray runs of it.
func cleanName(s string) string {
	return cleaning.CleanString(strings.Join(strings.Fields(s), " "))
}

// journalSkipReason returns why a row cannot become a container, or empty
// string if it can.
func journalSkipReason(row journalRow, issns journalISSNs) string {
	if issns.ISSNP == "" && issns.ISSNE == "" {
		return "no-valid-issn"
	}
	if cleanName(row.Title) == "" {
		return "empty-title"
	}
	return ""
}

// doajJournalID pulls DOAJ's own journal identifier off the end of its TOC URL
// (https://doaj.org/toc/<id>). The CSV has no column for it on its own.
func doajJournalID(tocURL string) string {
	const marker = "/toc/"
	i := strings.LastIndex(tocURL, marker)
	if i == -1 {
		return ""
	}
	return strings.Trim(tocURL[i+len(marker):], "/")
}

// journalToContainer builds the container we would create for a row.
func journalToContainer(row journalRow, issns journalISSNs, source string) fatcat2.Container {
	extra := map[string]any{}

	doajExtra := map[string]any{}
	if id := doajJournalID(row.DOAJURL); id != "" {
		doajExtra["id"] = id
	}
	if row.AddedOn != "" {
		doajExtra["added_on"] = row.AddedOn
	}
	if len(doajExtra) > 0 {
		extra["doaj"] = doajExtra
	}
	if row.URL != "" {
		extra["urls"] = []string{row.URL}
	}
	if row.Country != "" {
		extra["country_name"] = row.Country
	}

	return fatcat2.Container{
		Name:      cleanName(row.Title),
		Type:      "journal",
		Publisher: cleanName(row.Publisher),
		ISSNL:     issns.ISSNL,
		ISSNP:     issns.ISSNP,
		ISSNE:     issns.ISSNE,
		Source:    source,
		Extra:     extra,
	}
}

// findContainer returns the ID of a fatcat2 container sharing any ISSN with
// the row, if there is one. Every ISSN the row has is tried against every ISSN
// column, because nothing guarantees which column an existing container filed
// it under: legacy containers often carry only an ISSN-L, and an ISSN-L is
// itself one of the journal's ISSNs.
//
// The ISSN-L goes first, and is looked up as an ISSN-L first, because that is
// how nearly every existing container is found: a row DOAJ has had for years
// costs one request.
func findContainer(client *http.Client, issns journalISSNs) (*uuid.UUID, error) {
	candidates := append(append([]string{}, issns.LinkingISSNs...), issns.ISSNP, issns.ISSNE)
	id, err := fatcat2.LookupContainerByISSNs(client, candidates...)
	if err != nil {
		return nil, fmt.Errorf("container lookup failed: %w", err)
	}
	return id, nil
}

// journalOutcome is what happened to a single row.
type journalOutcome string

const (
	journalAdded   journalOutcome = "added"
	journalIgnored journalOutcome = "ignored"
	journalSkipped journalOutcome = "skipped"
)

// importJournal makes sure fatcat has a container for one DOAJ journal row,
// creating and indexing one if it does not. It is idempotent, which is what
// makes re-running a day, or resuming an import part way, safe.
func importJournal(client *http.Client, row journalRow, source string) (journalOutcome, *fatcat2.Container, error) {
	issns := resolveISSNs(row)
	if reason := journalSkipReason(row, issns); reason != "" {
		return journalSkipped, nil, nil
	}

	existing, err := findContainer(client, issns)
	if err != nil {
		return "", nil, err
	}
	if existing != nil {
		return journalIgnored, nil, nil
	}

	c := journalToContainer(row, issns, source)
	if _, err := fatcat2.CreateContainer(client, &c); err != nil {
		return "", nil, fmt.Errorf("container creation failed for %q: %w", c.Name, err)
	}

	// NB as in the article importers, a failure here is not revisited: the next
	// run finds the container in fatcat and leaves it alone.
	doc := indexing.PrepareFatcatContainerDoc(c)
	bs, err := json.Marshal(doc)
	if err != nil {
		return "", nil, fmt.Errorf("could not marshal container doc: %w", err)
	}
	err = indexing.DoElasticIndex(client,
		viper.GetString("indexing.fatcat_container_ix"), doc.LegacyIdent, bs)
	if err != nil {
		return "", nil, fmt.Errorf("could not index container %q: %w", c.ID, err)
	}

	return journalAdded, &c, nil
}
