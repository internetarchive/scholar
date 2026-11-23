package etd

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"slices"
	"strings"
	"time"

	"git.archive.org/webgroup/scholar/trawler/cleaning"
	"git.archive.org/webgroup/scholar/trawler/fatcat2"
)

// Record is one line of the etd-fulltext ndjson. Only fields we map are
// declared; the rest of the line is ignored.
type Record struct {
	Key            string     `json:"key"`
	Source         string     `json:"source"`
	Host           string     `json:"host"`
	Title          string     `json:"title"`
	Creators       []string   `json:"creators"`
	Advisors       []string   `json:"advisors"`
	Orcids         []string   `json:"orcids"`
	Date           string     `json:"date"`
	Year           int        `json:"year"`
	Institution    string     `json:"institution"`
	InstitutionQID string     `json:"institution_qid"`
	Department     string     `json:"department"`
	Country        string     `json:"country"`
	Languages      []string   `json:"languages"`
	Subjects       []string   `json:"subjects"`
	DCType         string     `json:"dc_type"`
	DocKind        string     `json:"doc_kind"`
	Level          string     `json:"level"`
	Abstract       string     `json:"abstract"`
	Landing        string     `json:"landing"`
	Aliases        []string   `json:"aliases"`
	Withdrawn      string     `json:"withdrawn"`
	Fulltext       []Fulltext `json:"fulltext"`
	ETD            struct {
		Build  string `json:"build"`
		Ledger string `json:"ledger"`
	} `json:"etd"`
}

// Fulltext is one PDF location for a record.
type Fulltext struct {
	// Part is whole-verified, whole or chapter.
	Part string `json:"part"`
	// URL is the original live-web URL.
	URL string `json:"url"`
	// Exact entries name a specific capture; inexact ones carry a fuzzy
	// web/2/ replay URL that wayback resolves to the nearest capture.
	Exact      bool   `json:"exact"`
	WaybackURL string `json:"wayback_url"`
	FetchURL   string `json:"fetch_url"`
}

var partRank = map[string]int{"whole-verified": 0, "whole": 1, "chapter": 2}

// rankedFulltext returns the record's PDF entries best-first: whole-verified,
// then whole, then chapter, exact captures before fuzzy ones, otherwise in
// file order. Entries without a fetch URL are dropped.
func (r Record) rankedFulltext() []Fulltext {
	out := make([]Fulltext, 0, len(r.Fulltext))
	for _, f := range r.Fulltext {
		if f.FetchURL != "" {
			out = append(out, f)
		}
	}
	slices.SortStableFunc(out, func(a, b Fulltext) int {
		ra, ok := partRank[a.Part]
		if !ok {
			ra = len(partRank)
		}
		rb, ok := partRank[b.Part]
		if !ok {
			rb = len(partRank)
		}
		if ra != rb {
			return ra - rb
		}
		switch {
		case a.Exact && !b.Exact:
			return -1
		case !a.Exact && b.Exact:
			return 1
		}
		return 0
	})
	return out
}

// ExtIDs returns the identifiers fc2 can look up and store: DOI (from a
// doi: key or a DOI landing URL), handle (from a hdl.handle.net landing URL)
// and Wikidata QID (from a wikidata: key). At most one of each type.
func (r Record) ExtIDs() []fatcat2.ExternalID {
	var out []fatcat2.ExternalID
	add := func(t, v string) {
		if v == "" {
			return
		}
		for _, e := range out {
			if e.Type == t {
				return
			}
		}
		out = append(out, fatcat2.ExternalID{Type: t, Value: v})
	}

	if rest, ok := strings.CutPrefix(r.Key, "doi:"); ok {
		add("doi", cleaning.NormalizeDOI(rest))
	}
	if rest, ok := strings.CutPrefix(r.Key, "wikidata:"); ok && strings.HasPrefix(rest, "Q") {
		add("wikidata_qid", rest)
	}
	if r.Landing != "" {
		add("doi", landingDOI(r.Landing))
		add("hdl", landingHandle(r.Landing))
	}
	return out
}

func landingDOI(landing string) string {
	u, err := url.Parse(landing)
	if err != nil {
		return ""
	}
	switch strings.ToLower(u.Host) {
	case "doi.org", "dx.doi.org", "www.doi.org":
		return cleaning.NormalizeDOI(strings.TrimPrefix(u.Path, "/"))
	}
	return ""
}

// landingHandle extracts a handle from a hdl.handle.net URL, lowercased the
// way fatcat has always stored handles.
func landingHandle(landing string) string {
	u, err := url.Parse(landing)
	if err != nil || strings.ToLower(u.Host) != "hdl.handle.net" {
		return ""
	}
	h := strings.Trim(u.Path, "/")
	// A handle is prefix/suffix; anything else is a landing page we misread.
	if !strings.Contains(h, "/") {
		return ""
	}
	return strings.ToLower(h)
}

// ReleaseDate returns the record's date when it is a full calendar date.
// Most are only a year (release_year carries that) or a year and month,
// which fc2's release_date cannot hold.
func (r Record) ReleaseDate() *fatcat2.ReleaseDate {
	if len(r.Date) < 10 {
		return nil
	}
	t, err := time.Parse("2006-01-02", r.Date[:10])
	if err != nil {
		return nil
	}
	d := fatcat2.ReleaseDate(t)
	return &d
}

// contrib turns a creator string into a contrib. Names come overwhelmingly as
// "Surname, Given"; exactly one comma is taken to mean that form.
func contrib(raw string, position int) fatcat2.ReleaseContrib {
	c := fatcat2.ReleaseContrib{RawName: strings.TrimSpace(raw), Role: "author", Position: new(position)}
	if sur, given, ok := strings.Cut(raw, ","); ok && !strings.Contains(given, ",") {
		sur, given = strings.TrimSpace(sur), strings.TrimSpace(given)
		if sur != "" && given != "" {
			c.Surname, c.GivenName = sur, given
		}
	}
	return c
}

// ToRelease maps a record to a new fc2 release stamped with source. The
// result has no ID; fc2 or the caller assigns one.
func (r Record) ToRelease(source string) fatcat2.Release {
	rel := fatcat2.Release{
		Title:       strings.TrimSpace(r.Title),
		Type:        "thesis",
		Stage:       "published",
		ReleaseDate: r.ReleaseDate(),
		ReleaseYear: r.Year,
		Publisher:   strings.TrimSpace(r.Institution),
		Language:    r.Language(),
		Source:      source,
		ExternalIDs: r.ExtIDs(),
		Extra:       map[string]any{"etd": r.extra()},
	}
	if rel.ReleaseYear == 0 && rel.ReleaseDate != nil {
		rel.ReleaseYear = time.Time(*rel.ReleaseDate).Year()
	}
	pos := 0
	for _, c := range r.Creators {
		if strings.TrimSpace(c) == "" {
			continue
		}
		rel.Contribs = append(rel.Contribs, contrib(c, pos))
		pos++
	}
	if a := strings.TrimSpace(r.Abstract); len(a) >= cleaning.MinAbstractLength {
		rel.Abstracts = []fatcat2.Abstract{abstract(a, rel.Language)}
	}
	return rel
}

// Language is the record's first language that maps to an ISO 639-1 code.
func (r Record) Language() string {
	for _, l := range r.Languages {
		if code := cleaning.NormalizeLanguageLoose(l); code != "" {
			return code
		}
	}
	return ""
}

// extra is what goes in the release's extra.etd: provenance, plus whatever the
// record knows that fc2 has no column for. Empty values are left out.
func (r Record) extra() map[string]any {
	m := map[string]any{}
	set := func(k string, v any) {
		switch x := v.(type) {
		case string:
			if x == "" {
				return
			}
		case []string:
			if len(x) == 0 {
				return
			}
		}
		m[k] = v
	}
	set("key", r.Key)
	set("source", r.Source)
	set("host", r.Host)
	set("level", r.Level)
	set("dc_type", r.DCType)
	set("doc_kind", r.DocKind)
	set("institution", r.Institution)
	set("institution_qid", r.InstitutionQID)
	set("department", r.Department)
	set("country", r.Country)
	set("subjects", r.Subjects)
	set("advisors", r.Advisors)
	set("orcids", r.Orcids)
	set("aliases", r.Aliases)
	set("landing", r.Landing)
	set("build", r.ETD.Build)

	// Normalize through JSON so the map has the same shape (lists as []any)
	// as one decoded from a GET. fillBlanks compares the two to decide whether
	// a PUT is needed, and []string never equals []any.
	bs, _ := json.Marshal(m)
	var out map[string]any
	json.Unmarshal(bs, &out)
	return out
}

func abstract(content, lang string) fatcat2.Abstract {
	content = cleaning.CleanString(content)
	h := sha1.Sum([]byte(content))
	return fatcat2.Abstract{
		Content:  content,
		SHA1:     hex.EncodeToString(h[:]),
		Language: lang,
		MIMEType: "text/plain",
	}
}
