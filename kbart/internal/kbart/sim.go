package kbart

import (
	"bufio"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strings"
)

// ISSNMap maps an ISSN (or ISSN-L) to its ISSN-L.
type ISSNMap map[string]string

// LoadISSNMap reads an ISSN-to-ISSN-L table (whitespace-separated, with an
// "ISSN..." header line) into a lookup. Each ISSN maps to its ISSN-L, and each
// ISSN-L maps to itself, so a lookup by any form succeeds. Ported from the map
// loading in convert_sim_kbart.py.
func LoadISSNMap(path string) (ISSNMap, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	m := ISSNMap{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "ISSN") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		issn, issnl := fields[0], fields[1]
		m[issn] = issnl
		m[issnl] = issnl // double mapping makes lookups easy
	}
	return m, sc.Err()
}

// Lookup returns the ISSN-L for an ISSN (or ISSN-L), or "" if unknown.
func (m ISSNMap) Lookup(issn string) string { return m[issn] }

// ConvertSIM reads an IA SIM KBART file, projects it onto the KBART output
// columns, fills in linking_issn via the ISSN-L map, and writes the result. It
// reports how many rows were written and skipped. Ported from convert_sim_kbart.py.
func ConvertSIM(in io.Reader, m ISSNMap, out io.Writer) (written, skipped int, err error) {
	r := csv.NewReader(in)
	r.Comma = '\t'
	r.FieldsPerRecord = -1 // SIM exports can have ragged rows
	r.LazyQuotes = true    // tolerate stray quotes in title fields

	header, err := r.Read()
	if err != nil {
		return 0, 0, fmt.Errorf("read header: %w", err)
	}
	col := map[string]int{}
	for i, name := range header {
		col[name] = i
	}
	get := func(rec []string, name string) string {
		if i, ok := col[name]; ok && i < len(rec) {
			return rec[i]
		}
		return ""
	}

	w := NewWriter(out)
	if err := w.WriteHeader(); err != nil {
		return 0, 0, err
	}

	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return written, skipped, fmt.Errorf("read row: %w", err)
		}

		online := get(rec, "online_identifier")
		print := get(rec, "print_identifier")
		// Skip rows with neither ISSN.
		if online == "" && print == "" {
			skipped++
			continue
		}

		linking := get(rec, "linking_issn")
		if linking == "" {
			linking = m.Lookup(online)
		}
		if linking == "" {
			linking = m.Lookup(print)
		}
		if linking == "" {
			fmt.Fprintf(os.Stderr, "  no matching ISSN-L for: '%s' or '%s'. skipping\n", online, print)
			skipped++
			continue
		}

		title := get(rec, "publication_title")
		coverage := get(rec, "coverage_depth")
		// The Python asserted these; skip with a warning rather than crash.
		if title == "" || coverage != "fulltext" {
			fmt.Fprintf(os.Stderr, "  unexpected row (title=%q coverage_depth=%q). skipping\n", title, coverage)
			skipped++
			continue
		}

		row := Row{
			PublicationType:      get(rec, "publication_type"),
			PublicationTitle:     title,
			PrintIdentifier:      print,
			OnlineIdentifier:     online,
			DateFirstIssueOnline: get(rec, "date_first_issue_online"),
			NumFirstVolOnline:    get(rec, "num_first_vol_online"),
			NumFirstIssueOnline:  get(rec, "num_first_issue_online"),
			DateLastIssueOnline:  get(rec, "date_last_issue_online"),
			NumLastVolOnline:     get(rec, "num_last_vol_online"),
			NumLastIssueOnline:   get(rec, "num_last_issue_online"),
			TitleURL:             "", // dropped: points at archive.org
			FirstAuthor:          get(rec, "first_author"),
			TitleID:              get(rec, "title_id"),
			CoverageDepth:        coverage,
			CoverageNotes:        get(rec, "coverage_notes"),
			PublisherName:        get(rec, "publisher_name"),
			LinkingISSN:          linking,
		}
		if err := w.Write(row); err != nil {
			return written, skipped, err
		}
		written++
	}
	return written, skipped, w.Flush()
}
