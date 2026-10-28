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
	"errors"
	"fmt"
	"io"
	"strings"

	"git.archive.org/webgroup/scholar/trawler/containers"
	"git.archive.org/webgroup/scholar/trawler/fatcat2"
	"git.archive.org/webgroup/scholar/trawler/issn"
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
	out.LinkingISSNs = issn.LinkingISSNs(out.ISSNP, out.ISSNE)
	if len(out.LinkingISSNs) > 0 {
		out.ISSNL = out.LinkingISSNs[0]
	}
	return out
}

// journalSkipReason returns why a row cannot become a container, or empty
// string if it can.
func journalSkipReason(row journalRow, issns journalISSNs) string {
	if issns.ISSNP == "" && issns.ISSNE == "" {
		return "no-valid-issn"
	}
	if containers.CleanName(row.Title) == "" {
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
//
// extra.doaj is always set, even when the row has nothing to put in it: its
// presence is what marks the container in_doaj when indexed.
func journalToContainer(row journalRow, issns journalISSNs, source string) fatcat2.Container {
	doajExtra := map[string]any{}
	if id := doajJournalID(row.DOAJURL); id != "" {
		doajExtra["id"] = id
	}
	if row.AddedOn != "" {
		doajExtra["added_on"] = row.AddedOn
	}
	extra := map[string]any{"doaj": doajExtra}
	if row.URL != "" {
		extra["urls"] = []string{row.URL}
	}
	if row.Country != "" {
		extra["country_name"] = row.Country
	}

	return fatcat2.Container{
		Name:      containers.CleanName(row.Title),
		Type:      "journal",
		Publisher: containers.CleanName(row.Publisher),
		ISSNL:     issns.ISSNL,
		ISSNP:     issns.ISSNP,
		ISSNE:     issns.ISSNE,
		Source:    source,
		Extra:     extra,
	}
}

// ToContainer implements containers.Row. Lookup is by every ISSN the row has,
// ISSN-Ls first.
func (row journalRow) ToContainer(source string) (fatcat2.Container, []string, string) {
	issns := resolveISSNs(row)
	if reason := journalSkipReason(row, issns); reason != "" {
		return fatcat2.Container{}, nil, reason
	}
	lookup := append(append([]string{}, issns.LinkingISSNs...), issns.ISSNP, issns.ISSNE)
	return journalToContainer(row, issns, source), lookup, ""
}

func (row journalRow) LogValues() []any {
	return []any{"title", row.Title}
}
