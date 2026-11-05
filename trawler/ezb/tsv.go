package ezb

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// EZB also exports its journal list as TSV
// (https://ezb.ur.de/services/titlelist?collection_id=journals), from a
// different service than the JSON. It is worth reading when the JSON endpoint
// will not answer. It carries the same records with one difference: it has no
// categories, so EZB's Diamond label is missing. Its open access label is
// there, in Access requirements.
//
// The file opens with a count line ("Total: 124489 journal(s)") and a blank
// line before the header. Columns are found by header name.
const (
	tsvTitle      = "Title"
	tsvPublisher  = "Publisher"
	tsvEISSN      = "E-ISSN"
	tsvPISSN      = "P-ISSN"
	tsvZDBID      = "ZDB-Number"
	tsvEZBID      = "EZB-Id"
	tsvURLs       = "bibliographical URL(s)"
	tsvPriceType  = "Price type"
	tsvAccess     = "Access requirements"
	tsvFirstYear  = "First year"
	tsvLastYear   = "Last year"
	tsvListSep    = ";"   // between ISSNs in one cell
	tsvURLListSep = "***" // between URLs in one cell
)

// tsvRequired are the columns without which a row cannot be matched to a
// container, or told apart from another.
var tsvRequired = []string{tsvTitle, tsvEISSN, tsvPISSN, tsvEZBID}

// parseJournalsAny reads EZB's journal list in either format, telling them
// apart by the first character: the JSON is an array.
func parseJournalsAny(r io.Reader) ([]journalRecord, error) {
	br := bufio.NewReader(r)
	if bom, _ := br.Peek(3); bytes.Equal(bom, []byte("\xef\xbb\xbf")) {
		br.Discard(3)
	}
	for {
		b, err := br.Peek(1)
		if err != nil {
			return nil, fmt.Errorf("could not read journal list: %w", err)
		}
		switch b[0] {
		case ' ', '\t', '\r', '\n':
			br.ReadByte()
			continue
		case '[':
			return parseJournals(br)
		}
		return parseJournalsTSV(br)
	}
}

// parseJournalsTSV reads EZB's TSV journal list.
func parseJournalsTSV(r io.Reader) ([]journalRecord, error) {
	sc := bufio.NewScanner(r)
	// Some rows carry long keyword and URL lists.
	sc.Buffer(make([]byte, 0, 1<<20), 16<<20)

	// Skip the count line and blank line, to the header.
	var cols map[string]int
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, tsvTitle+"\t") {
			continue
		}
		cols = map[string]int{}
		for i, h := range strings.Split(line, "\t") {
			cols[strings.TrimSpace(h)] = i
		}
		break
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("could not read journal list: %w", err)
	}
	if cols == nil {
		return nil, errors.New("journal list is neither a JSON array nor EZB's TSV (no header row)")
	}
	for _, c := range tsvRequired {
		if _, ok := cols[c]; !ok {
			return nil, fmt.Errorf("TSV journal list is missing required column %q", c)
		}
	}

	out := []journalRecord{}
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		f := strings.Split(line, "\t")
		get := func(col string) string {
			i, ok := cols[col]
			if !ok || i >= len(f) {
				return ""
			}
			return strings.TrimSpace(f[i])
		}

		id, err := strconv.Atoi(get(tsvEZBID))
		if err != nil {
			return nil, fmt.Errorf("record %d has no EZB ID (%q): is the file cut short?", len(out)+1, get(tsvEZBID))
		}
		out = append(out, journalRecord{
			Title:              flexString(get(tsvTitle)),
			PrintISSNs:         splitList(get(tsvPISSN), tsvListSep),
			OnlineISSNs:        splitList(get(tsvEISSN), tsvListSep),
			FirstYear:          tsvYear(get(tsvFirstYear)),
			LastYear:           tsvYear(get(tsvLastYear)),
			TitleURLs:          splitList(get(tsvURLs), tsvURLListSep),
			EZBID:              id,
			ZDBID:              get(tsvZDBID),
			Publisher:          get(tsvPublisher),
			PriceType:          get(tsvPriceType),
			AccessRequirements: get(tsvAccess),
		})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("could not read journal list after %d records: %w", len(out), err)
	}
	if len(out) == 0 {
		return nil, errors.New("journal list is empty")
	}

	return out, nil
}

func splitList(s, sep string) []string {
	out := []string{}
	for _, v := range strings.Split(s, sep) {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// tsvYear is a year cell; empty or unreadable is no year.
func tsvYear(s string) flexYear {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return flexYear(n)
}
