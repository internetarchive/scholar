// Package kbart holds the KBART row model, the TSV writer, the fatcat container
// eligibility logic (ported from fatcat_kbart.py), and the IA SIM serials
// conversion (ported from convert_sim_kbart.py).
package kbart

import (
	"encoding/csv"
	"io"
)

// FieldNames is the ordered set of KBART columns this tool emits. It matches the
// KBART_FIELD_NAMES used by both original Python scripts, so the fatcat and SIM
// outputs are concatenation-compatible.
var FieldNames = []string{
	"publication_type",
	"publication_title",
	"print_identifier",
	"online_identifier",
	"date_first_issue_online",
	"num_first_vol_online",
	"num_first_issue_online",
	"date_last_issue_online",
	"num_last_vol_online",
	"num_last_issue_online",
	"title_url",
	"first_author",
	"title_id",
	"coverage_depth",
	"coverage_notes",
	"publisher_name",
	"linking_issn",
}

// Row is one KBART record. Field order in fields() must match FieldNames.
type Row struct {
	PublicationType      string
	PublicationTitle     string
	PrintIdentifier      string
	OnlineIdentifier     string
	DateFirstIssueOnline string
	NumFirstVolOnline    string
	NumFirstIssueOnline  string
	DateLastIssueOnline  string
	NumLastVolOnline     string
	NumLastIssueOnline   string
	TitleURL             string
	FirstAuthor          string
	TitleID              string
	CoverageDepth        string
	CoverageNotes        string
	PublisherName        string
	LinkingISSN          string
}

func (r Row) fields() []string {
	return []string{
		r.PublicationType,
		r.PublicationTitle,
		r.PrintIdentifier,
		r.OnlineIdentifier,
		r.DateFirstIssueOnline,
		r.NumFirstVolOnline,
		r.NumFirstIssueOnline,
		r.DateLastIssueOnline,
		r.NumLastVolOnline,
		r.NumLastIssueOnline,
		r.TitleURL,
		r.FirstAuthor,
		r.TitleID,
		r.CoverageDepth,
		r.CoverageNotes,
		r.PublisherName,
		r.LinkingISSN,
	}
}

// Writer emits KBART rows as tab-separated values, matching the excel-tab
// dialect the Python csv module produced.
type Writer struct {
	cw *csv.Writer
}

// NewWriter returns a KBART TSV Writer over w. Rows are terminated with CRLF to
// match the files historically submitted to the Keepers Registry (the Python
// csv module's excel-tab dialect used \r\n).
func NewWriter(w io.Writer) *Writer {
	cw := csv.NewWriter(w)
	cw.Comma = '\t'
	cw.UseCRLF = true
	return &Writer{cw: cw}
}

// WriteHeader writes the column header row.
func (w *Writer) WriteHeader() error {
	return w.cw.Write(FieldNames)
}

// Write emits one row.
func (w *Writer) Write(r Row) error {
	return w.cw.Write(r.fields())
}

// Flush flushes buffered output and returns any write error.
func (w *Writer) Flush() error {
	w.cw.Flush()
	return w.cw.Error()
}
