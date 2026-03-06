package datacite

import (
	"context"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode"

	"git.archive.org/webgroup/scholar/trawler/cleaning"
	"git.archive.org/webgroup/scholar/trawler/counts"
	"git.archive.org/webgroup/scholar/trawler/fatcat2"
	"git.archive.org/webgroup/scholar/trawler/indexing"
	"git.archive.org/webgroup/scholar/trawler/issn"
	"github.com/spf13/viper"
	"go.temporal.io/sdk/activity"
)

const minAbstractLength = 10
const maxAbstractLength = 32000
const maxPublisherLength = 80

// --- Lookup tables ---

// unknownMarkersLower is the set of lowercase strings that signal missing or
// unknown values in DataCite metadata per the DataCite schema spec.
var unknownMarkersLower = map[string]struct{}{
	"(:unac)": {}, "(:unal)": {}, "(:unap)": {}, "(:unas)": {},
	"(:unav)": {}, "(:unkn)": {}, "(:none)": {}, "(:null)": {},
	"(:tba)": {}, "(:etal)": {},
	"na": {}, "nn": {}, "n.a.": {}, "[s.n.]": {}, "unknown": {},
}

func isUnknown(s string) bool {
	_, ok := unknownMarkersLower[strings.ToLower(strings.TrimSpace(s))]
	return ok
}

var spamTokens = []string{
	"full", "movies", "movie", "watch", "streaming", "online",
	"free", "hd", "download", "english", "subtitle", "bluray",
}

func isSpamTitle(title string) bool {
	lower := strings.ToLower(title)
	seen := 0
	for _, tok := range spamTokens {
		if strings.Contains(lower, tok) {
			seen++
		}
	}
	return seen >= 4
}

// typeMap maps (schema, value) to fatcat release type. Priority: citeproc,
// ris, schemaOrg, bibtex, resourceTypeGeneral.
var typeMap = map[string]map[string]string{
	"citeproc": {
		"article": "article", "article-journal": "article-journal",
		"article-magazine": "article-magazine", "article-newspaper": "article-newspaper",
		"bill": "bill", "book": "book", "broadcast": "broadcast",
		"chapter": "chapter", "dataset": "dataset",
		"entry-dictionary": "entry-dictionary", "entry-encyclopedia": "entry-encyclopedia",
		"entry": "entry", "figure": "figure", "graphic": "graphic",
		"interview": "interview", "legal_case": "legal_case", "legislation": "legislation",
		"manuscript": "manuscript", "map": "map", "motion_picture": "motion_picture",
		"musical_score": "musical_score", "pamphlet": "pamphlet",
		"paper-conference": "paper-conference", "patent": "patent",
		"personal_communication": "personal_communication",
		"post": "post", "post-weblog": "post-weblog", "report": "report",
		"review-book": "review-book", "review": "review", "song": "song",
		"speech": "speech", "thesis": "thesis", "treaty": "treaty", "webpage": "webpage",
	},
	"ris": {
		"THES": "thesis", "SOUND": "song", "CHAP": "chapter", "FIGURE": "figure",
		"RPRT": "report", "JOUR": "article-journal", "MPCT": "motion_picture",
		"GEN": "article-journal", "BOOK": "book", "DATA": "dataset", "COMP": "software",
	},
	"schemaOrg": {
		"Dataset": "dataset", "Book": "book", "ScholarlyArticle": "article-journal",
		"ImageObject": "graphic", "SoftwareSourceCode": "software",
		"Chapter": "chapter", "Thesis": "thesis", "PublicationIssue": "article",
	},
	"bibtex": {
		"phdthesis": "thesis", "inbook": "chapter",
		"article": "article-journal", "book": "book",
	},
	"resourceTypeGeneral": {
		"Image": "graphic", "Dataset": "dataset", "Software": "software",
	},
}

var containerTypeMap = map[string]string{
	"Journal": "journal", "Series": "journal", "Book Series": "book-series",
}

// iso6392to1 maps three-letter ISO 639-2 codes to two-letter ISO 639-1 codes.
var iso6392to1 = map[string]string{
	"eng": "en", "fre": "fr", "ger": "de", "spa": "es", "ita": "it",
	"por": "pt", "rus": "ru", "jpn": "ja", "chi": "zh", "kor": "ko",
	"dut": "nl", "pol": "pl", "swe": "sv", "dan": "da", "nor": "no",
	"fin": "fi", "ara": "ar", "heb": "he", "tur": "tr", "cze": "cs",
	"hun": "hu", "rum": "ro", "gre": "el", "ukr": "uk", "hrv": "hr",
	"slv": "sl", "bul": "bg", "cat": "ca", "vie": "vi", "ind": "id",
	"per": "fa", "hin": "hi", "lat": "la", "wel": "cy", "geo": "ka",
}

func normalizeLanguage(lang string) string {
	lang = strings.ToLower(strings.TrimSpace(lang))
	if len(lang) == 2 {
		return lang
	}
	// Three-letter code
	if len(lang) == 3 {
		if v, ok := iso6392to1[lang]; ok {
			return v
		}
	}
	// "en-us" style
	if len(lang) > 2 && lang[2] == '-' {
		return lang[:2]
	}
	return ""
}

// --- Struct definitions ---

type dataciteNameIdentifier struct {
	NameIdentifier       string `json:"nameIdentifier"`
	NameIdentifierScheme string `json:"nameIdentifierScheme"`
}

type dataciteAffiliation struct {
	Name string `json:"name"`
}

type dataciteCreator struct {
	Name            string                   `json:"name"`
	GivenName       string                   `json:"givenName"`
	FamilyName      string                   `json:"familyName"`
	NameType        string                   `json:"nameType"`
	NameIdentifiers []dataciteNameIdentifier `json:"nameIdentifiers"`
	Affiliation     []dataciteAffiliation    `json:"affiliation"`
	ContributorType string                   `json:"contributorType"` // for contributors only
}

type dataciteTitle struct {
	Title     string `json:"title"`
	TitleType string `json:"titleType"`
	Lang      string `json:"lang"`
}

type dataciteTypes struct {
	ResourceType        string `json:"resourceType"`
	ResourceTypeGeneral string `json:"resourceTypeGeneral"`
	Citeproc            string `json:"citeproc"`
	Ris                 string `json:"ris"`
	Bibtex              string `json:"bibtex"`
	SchemaOrg           string `json:"schemaOrg"`
}

type dataciteDate struct {
	Date     string `json:"date"`
	DateType string `json:"dateType"`
}

type dataciteDescription struct {
	Description     string `json:"description"`
	DescriptionType string `json:"descriptionType"`
}

type dataciteRelatedIdentifier struct {
	RelatedIdentifier     string `json:"relatedIdentifier"`
	RelatedIdentifierType string `json:"relatedIdentifierType"`
	RelationType          string `json:"relationType"`
}

type dataciteRights struct {
	RightsUri string `json:"rightsUri"`
}

type dataciteSubject struct {
	Subject   string `json:"subject"`
	SchemeUri string `json:"schemeUri"`
}

type dataciteContainer struct {
	Type           string `json:"type"`
	Identifier     string `json:"identifier"`
	IdentifierType string `json:"identifierType"`
	Title          string `json:"title"`
	Volume         string `json:"volume"`
	Issue          string `json:"issue"`
	FirstPage      string `json:"firstPage"`
	LastPage       string `json:"lastPage"`
}

type dataciteDoc struct {
	ID         string `json:"id"`
	Attributes struct {
		DOI                string                      `json:"doi"`
		Titles             []dataciteTitle             `json:"titles"`
		Types              dataciteTypes               `json:"types"`
		Creators           []dataciteCreator           `json:"creators"`
		Contributors       []dataciteCreator           `json:"contributors"`
		Descriptions       []dataciteDescription       `json:"descriptions"`
		Dates              []dataciteDate              `json:"dates"`
		RelatedIdentifiers []dataciteRelatedIdentifier `json:"relatedIdentifiers"`
		RightsList         []dataciteRights            `json:"rightsList"`
		Subjects           []dataciteSubject           `json:"subjects"`
		Container          dataciteContainer           `json:"container"`
		PublicationYear    int                         `json:"publicationYear"`
		Published          string                      `json:"published"`
		Publisher          string                      `json:"publisher"`
		Language           string                      `json:"language"`
		Version            string                      `json:"version"`
		MetadataVersion    int                         `json:"metadataVersion"`
		State              string                      `json:"state"`
	} `json:"attributes"`
}

// --- Date parsing ---

type dateGranularity int

const (
	granYear  dateGranularity = iota
	granMonth dateGranularity = iota
	granDay   dateGranularity = iota
)

var dateLayouts = []struct {
	layout string
	gran   dateGranularity
}{
	{"2006-01-02T15:04:05Z", granDay},
	{"2006-01-02T15:04:05", granDay},
	{"2006-01-02", granDay},
	{"2006-01", granMonth},
	{"2006", granYear},
}

type parsedDate struct {
	t    time.Time
	gran dateGranularity
}

func tryParseDate(s string) (parsedDate, bool) {
	for _, l := range dateLayouts {
		t, err := time.Parse(l.layout, strings.TrimSpace(s))
		if err == nil {
			return parsedDate{t, l.gran}, true
		}
	}
	return parsedDate{}, false
}

var dateTypePriority = []string{
	"Valid", "Available", "Accepted", "Submitted", "Copyrighted", "Created", "Updated",
}

type dateResult struct {
	releaseDate  *fatcat2.ReleaseDate
	releaseMonth int // 0 means not set
	releaseYear  int // 0 means not set
}

func (d dateResult) hasDate() bool {
	return d.releaseYear > 0 || d.releaseDate != nil
}

func toDateResult(pd parsedDate) dateResult {
	thisYear := time.Now().Year()
	year := pd.t.Year()
	if year < 1000 || year > thisYear+5 {
		return dateResult{}
	}
	out := dateResult{releaseYear: year}
	if pd.gran == granDay {
		rd := fatcat2.ReleaseDate(pd.t)
		out.releaseDate = &rd
		out.releaseMonth = int(pd.t.Month())
	} else if pd.gran == granMonth {
		out.releaseMonth = int(pd.t.Month())
	}
	return out
}

func parseDates(dates []dataciteDate) dateResult {
	for _, prio := range dateTypePriority {
		for _, d := range dates {
			if d.DateType != prio {
				continue
			}
			pd, ok := tryParseDate(d.Date)
			if !ok {
				continue
			}
			if r := toDateResult(pd); r.hasDate() {
				return r
			}
		}
	}
	// Fall back: try any date in order
	for _, d := range dates {
		pd, ok := tryParseDate(d.Date)
		if !ok {
			continue
		}
		if r := toDateResult(pd); r.hasDate() {
			return r
		}
	}
	return dateResult{}
}

func parseSingleDateStr(s string) dateResult {
	if s == "" {
		return dateResult{}
	}
	pd, ok := tryParseDate(s)
	if !ok {
		return dateResult{}
	}
	return toDateResult(pd)
}

// --- Title parsing ---

func parseTitles(titles []dataciteTitle) (title, originalTitle, subtitle string) {
	if len(titles) == 0 {
		return
	}
	if len(titles) == 1 {
		title = strings.TrimSpace(titles[0].Title)
		return
	}
	for _, t := range titles {
		if title == "" && t.TitleType == "" {
			title = strings.TrimSpace(t.Title)
		}
		if subtitle == "" && t.TitleType == "Subtitle" {
			subtitle = strings.TrimSpace(t.Title)
		}
		if originalTitle == "" && t.TitleType == "TranslatedTitle" {
			orig := strings.TrimSpace(t.Title)
			if len(orig) >= 4 && orig != title && strings.Count(orig, "?") <= 3 {
				originalTitle = orig
			}
		}
	}
	if title == "" {
		title = strings.TrimSpace(titles[0].Title)
	}
	return
}

// --- Contributor parsing ---

// indexFormToDisplayName converts "Surname, Given" to "Given Surname".
func indexFormToDisplayName(s string) string {
	if !strings.Contains(s, ",") {
		return s
	}
	for _, ch := range []string{"(", ")", "*"} {
		if strings.Contains(s, ch) {
			return s
		}
	}
	if strings.Count(s, ",") > 1 {
		return s
	}
	stopwords := []string{
		"archive", "collection", "coordinator", "department", "germany",
		"international", "national", "netherlands", "office", "organisation",
		"organization", "service", "services", "united states", "university",
		"verein", "volkshochschule",
	}
	lower := strings.ToLower(s)
	for _, stop := range stopwords {
		if strings.Contains(lower, stop) {
			return s
		}
	}
	a, b, _ := strings.Cut(s, ",")
	return strings.TrimSpace(b) + " " + strings.TrimSpace(a)
}

var nameBlocklist = map[string]struct{}{
	"Occdownload Gbif.Org": {},
}

func parseContribs(creators []dataciteCreator, role string) []fatcat2.ReleaseContrib {
	var out []fatcat2.ReleaseContrib
	idx := 0

	for _, c := range creators {
		nameType := c.NameType
		if nameType != "" && nameType != "Personal" && nameType != "Organizational" {
			continue
		}

		if nameType == "Organizational" {
			name := c.Name
			if isUnknown(name) || len(name) < 3 {
				continue
			}
			out = append(out, fatcat2.ReleaseContrib{
				Position: idx,
				Role:     role,
				Extra:    map[string]any{"organization": name},
			})
			idx++
			continue
		}

		// Personal (or unspecified)
		name := cleaning.CleanString(c.Name)
		given := cleaning.CleanString(c.GivenName)
		surname := cleaning.CleanString(c.FamilyName)

		if name == "" && given == "" && surname == "" {
			continue
		}
		if name == "" {
			name = strings.TrimSpace(given + " " + surname)
		} else {
			name = indexFormToDisplayName(name)
		}
		if _, blocked := nameBlocklist[name]; blocked {
			continue
		}
		if isUnknown(name) {
			continue
		}
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}

		var orcid string
		for _, nid := range c.NameIdentifiers {
			if strings.EqualFold(nid.NameIdentifierScheme, "orcid") {
				raw := strings.TrimPrefix(nid.NameIdentifier, "https://orcid.org/")
				raw = strings.TrimPrefix(raw, "http://orcid.org/")
				if raw != "" {
					orcid = raw
				}
				break
			}
		}

		var rawAffil string
		if len(c.Affiliation) > 0 {
			rawAffil = cleaning.CleanString(c.Affiliation[0].Name)
		}

		extra := map[string]any{}
		if c.ContributorType != "" {
			extra["type"] = c.ContributorType
		}
		if orcid != "" {
			extra["orcid"] = orcid
		}

		rc := fatcat2.ReleaseContrib{
			Position:       idx,
			RawName:        name,
			GivenName:      given,
			Surname:        surname,
			Role:           role,
			RawAffiliation: rawAffil,
		}
		if len(extra) > 0 {
			rc.Extra = extra
		}

		if !contribExists(out, rc) {
			out = append(out, rc)
			idx++
		}
	}

	return out
}

func contribExists(list []fatcat2.ReleaseContrib, c fatcat2.ReleaseContrib) bool {
	for _, e := range list {
		if e.RawName != c.RawName {
			continue
		}
		er := e.Role
		if er == "" {
			er = "author"
		}
		cr := c.Role
		if cr == "" {
			cr = "author"
		}
		if er == cr {
			return true
		}
	}
	return false
}

// --- Type resolution ---

func resolveReleaseType(doi string, types dataciteTypes) string {
	order := []struct{ schema, value string }{
		{"citeproc", types.Citeproc},
		{"ris", types.Ris},
		{"schemaOrg", types.SchemaOrg},
		{"bibtex", types.Bibtex},
		{"resourceTypeGeneral", types.ResourceTypeGeneral},
	}
	for _, tp := range order {
		if tp.value == "" {
			continue
		}
		if m, ok := typeMap[tp.schema]; ok {
			if rt, ok := m[tp.value]; ok && rt != "" {
				return rt
			}
			// Explicit nil/empty mapping (e.g. schemaOrg "Collection") means
			// the type is known but not paper-like; stop here rather than
			// falling through to a less specific schema.
			if _, ok := m[tp.value]; ok {
				return ""
			}
		}
	}
	// figshare collections
	if (strings.HasPrefix(doi, "10.6084/") || strings.HasPrefix(doi, "10.25384")) &&
		types.ResourceType == "Collection" {
		return "stub"
	}
	return ""
}

// --- License slug ---

// dataciteLicenseSlug extends the common LicenseSlugLookup with DataCite-
// specific license URI patterns (rightsstatements.org, etc.).
func dataciteLicenseSlug(raw string) string {
	if raw == "" {
		return ""
	}
	if slug := cleaning.LicenseSlugLookup(raw); slug != "" {
		return slug
	}
	if strings.Contains(raw, "rightsstatements.org") {
		parts := strings.Split(raw, "/")
		for i, p := range parts {
			if (p == "vocab" || p == "page") && i+1 < len(parts) {
				name := parts[i+1]
				if name != "" && len(name) <= 9 {
					return "RS-" + strings.ToUpper(name)
				}
			}
		}
	}
	return ""
}

// --- Biblio hacks ---

func biblioHacks(r *fatcat2.Release, doc *dataciteDoc) {
	doi := r.DOI()
	if doi == "" {
		return
	}

	// GBIF occurrence downloads
	if r.Title == "GBIF Occurrence Download" && strings.HasPrefix(doi, "10.15468/dl.") {
		r.Type = "stub"
	}

	// Cambridge Crystallographic Data Centre
	if strings.HasPrefix(doi, "10.5517/") {
		r.Type = "entry"
	}

	// Supplement files
	if strings.HasPrefix(strings.ToLower(r.Title), "additional file") &&
		(r.Type == "article" || r.Type == "article-journal") {
		r.Type = "component"
	}

	// figshare: extract version from DOI suffix and identify components
	if strings.HasPrefix(doi, "10.6084/") || strings.HasPrefix(doi, "10.25384") {
		if idx := strings.LastIndex(doi, "."); idx >= 0 {
			suffix := doi[idx+1:]
			if strings.HasPrefix(suffix, "v") && isDigits(suffix[1:]) {
				r.Version = suffix
			}
		}
		if strings.Contains(r.Title, " from ") && r.Type != "stub" && r.Type != "graphic" {
			if strings.HasPrefix(r.Title, "Figure ") {
				r.Type = "component"
			} else if strings.HasPrefix(r.Title, "Table ") {
				r.Type = "component"
			}
		}
	}

	// figshare.com container name
	if strings.HasPrefix(doi, "10.6084/m9.figshare.") {
		if _, ok := r.Extra["container_name"]; !ok {
			r.Extra["container_name"] = "figshare.com"
		}
	}

	// Columbia University institutional repository
	if strings.HasPrefix(doi, "10.7916/") && strings.Contains(doi, "-") &&
		r.Publisher == "Columbia University" {
		for _, rel := range doc.Attributes.RelatedIdentifiers {
			if rel.RelationType == "IsVariantFormOf" {
				r.ContainerID = nil
				if r.Stage == "published" || r.Stage == "" {
					r.Stage = "submitted"
				}
			}
		}
	}

	// Known institutional repository prefixes: clear container on variant forms
	irPrefixes := []string{
		"10.15495/epub_ubt_", "10.18154/rwth-20",
		"10.3204/pubdb-", "10.3204/phppubdb-", "10.26204/kluedo/",
	}
	for _, prefix := range irPrefixes {
		if strings.HasPrefix(doi, prefix) {
			for _, rel := range doc.Attributes.RelatedIdentifiers {
				if rel.RelationType == "IsVariantFormOf" {
					r.ContainerID = nil
				}
			}
		}
	}
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// --- Core transformation ---

func dataciteToFc(doc *dataciteDoc, source string) *fatcat2.Release {
	a := doc.Attributes
	doi := strings.ToLower(strings.TrimSpace(a.DOI))

	release := &fatcat2.Release{
		Contribs:    []fatcat2.ReleaseContrib{},
		ExternalIDs: []fatcat2.ExternalID{{Type: "doi", Value: doi}},
		Extra:       map[string]any{},
		Abstracts:   []fatcat2.Abstract{},
		Refs:        []fatcat2.RawRef{},
		Source:      source,
	}

	// Title, subtitle, original title
	title, originalTitle, subtitle := parseTitles(a.Titles)
	release.Title = cleaning.CleanString(title)
	if subtitle != "" {
		release.Subtitle = cleaning.CleanString(subtitle)
	}
	if originalTitle != "" {
		release.OriginalTitle = originalTitle
	}

	// Release type
	release.Type = resolveReleaseType(doi, a.Types)

	// Release stage: always "published" for DataCite unless publisher is unknown
	release.Stage = "published"

	// Publisher
	pub := strings.TrimSpace(a.Publisher)
	if isUnknown(pub) || pub == "Unpublished" {
		pub = ""
		release.Stage = ""
	} else if len(pub) > maxPublisherLength {
		pub = ""
	}
	if pub != "" {
		release.Publisher = cleaning.CleanString(pub)
	}

	// Language
	release.Language = normalizeLanguage(a.Language)

	// Version
	if a.Version != "" && !isUnknown(a.Version) {
		release.Version = a.Version
	}

	// Dates: try structured dates first, then publicationYear, then published
	dr := parseDates(a.Dates)
	if !dr.hasDate() {
		if a.PublicationYear > 0 {
			dr = parseSingleDateStr(fmt.Sprintf("%d", a.PublicationYear))
		}
	}
	if !dr.hasDate() {
		dr = parseSingleDateStr(a.Published)
	}
	release.ReleaseDate = dr.releaseDate
	release.ReleaseYear = dr.releaseYear
	if dr.releaseMonth > 0 {
		release.Extra["release_month"] = dr.releaseMonth
	}

	// Contributors (creators + contributors, deduplicated)
	release.Contribs = parseContribs(a.Creators, "author")
	for _, cc := range parseContribs(a.Contributors, "author") {
		if !contribExists(release.Contribs, cc) {
			release.Contribs = append(release.Contribs, cc)
		}
	}

	// Volume, issue, pages from container
	container := a.Container
	volume := cleaning.CleanString(strings.TrimSpace(container.Volume))
	issue := cleaning.CleanString(strings.TrimSpace(container.Issue))
	if volume != "" {
		release.Volume = volume
	}
	if issue != "" {
		release.Issue = issue
	}
	if container.FirstPage != "" {
		if container.LastPage != "" {
			release.Pages = container.FirstPage + "-" + container.LastPage
		} else {
			release.Pages = container.FirstPage
		}
	}

	// License: last matching slug wins (mirrors fatcat Python behavior)
	for _, lic := range a.RightsList {
		if slug := dataciteLicenseSlug(lic.RightsUri); slug != "" {
			release.LicenseSlug = slug
		}
	}

	// Abstracts
	for _, desc := range a.Descriptions {
		if desc.DescriptionType != "Abstract" {
			continue
		}
		text := strings.TrimSpace(desc.Description)
		if len(text) < minAbstractLength {
			continue
		}
		if len(text) > maxAbstractLength {
			text = text[:maxAbstractLength] + " [...]"
		}
		h := sha1.Sum([]byte(text))
		release.Abstracts = append(release.Abstracts, fatcat2.Abstract{
			MIMEType: "text/plain",
			Content:  text,
			SHA1:     fmt.Sprintf("%x", h),
		})
	}

	// References from relatedIdentifiers
	refIdx := 0
	for _, rel := range a.RelatedIdentifiers {
		if rel.RelationType != "References" && rel.RelationType != "Cites" {
			continue
		}
		ref := fatcat2.RawRef{Index: refIdx}
		if rel.RelatedIdentifierType == "DOI" {
			ref.Extra = map[string]any{"doi": rel.RelatedIdentifier}
		}
		release.Refs = append(release.Refs, ref)
		refIdx++
	}

	// "Reviews" relationship overrides release type
	for _, rel := range a.RelatedIdentifiers {
		if rel.RelationType == "Reviews" {
			release.Type = "review"
		}
	}

	// Extra: datacite-specific metadata
	dcExtra := map[string]any{}

	if a.MetadataVersion > 0 {
		dcExtra["metadataVersion"] = a.MetadataVersion
	}
	if t := a.Types.ResourceType; t != "" && !isUnknown(t) {
		dcExtra["resourceType"] = t
	}
	if t := a.Types.ResourceTypeGeneral; t != "" && !isUnknown(t) {
		dcExtra["resourceTypeGeneral"] = t
	}

	// Subjects (skip those with schemeUri — they don't compress well)
	var filteredSubjects []map[string]any
	for _, s := range a.Subjects {
		if s.SchemeUri != "" {
			continue
		}
		filteredSubjects = append(filteredSubjects, map[string]any{"subject": s.Subject})
	}
	if len(filteredSubjects) > 0 {
		dcExtra["subjects"] = filteredSubjects
	}

	// Relations to carry forward in extra
	relationTypes := map[string]bool{
		"IsPartOf": true, "Reviews": true, "Continues": true,
		"IsVariantFormOf": true, "IsSupplementTo": true, "HasVersion": true,
		"IsMetadataFor": true, "IsNewVersionOf": true, "IsIdenticalTo": true,
		"IsVersionOf": true, "IsDerivedFrom": true, "IsSourceOf": true,
	}
	var relations []map[string]any
	for _, rel := range a.RelatedIdentifiers {
		if relationTypes[rel.RelationType] {
			relations = append(relations, map[string]any{
				"relationType":          rel.RelationType,
				"relatedIdentifier":     rel.RelatedIdentifier,
				"relatedIdentifierType": rel.RelatedIdentifierType,
			})
		}
	}
	if len(relations) > 0 {
		dcExtra["relations"] = relations
	}

	// License raw data
	if len(a.RightsList) > 0 {
		var licExtra []map[string]any
		for _, lic := range a.RightsList {
			licExtra = append(licExtra, map[string]any{"rightsUri": lic.RightsUri})
		}
		dcExtra["license"] = licExtra
	}

	release.Extra["datacite"] = dcExtra

	biblioHacks(release, doc)

	return release
}

// --- Container and release creation ---

func createRelease(client *http.Client, cs *counts.Counts, release fatcat2.Release, doc *dataciteDoc) (*fatcat2.Release, error) {
	container := doc.Attributes.Container
	containerType := containerTypeMap[container.Type]

	// Attempt container lookup/creation via ISSN when container type is recognized
	if containerType != "" && container.IdentifierType == "ISSN" && container.Identifier != "" {
		rawISSN := container.Identifier
		if len(rawISSN) == 8 {
			rawISSN = rawISSN[:4] + "-" + rawISSN[4:]
		}

		issnl := issn.ISSN2ISSNL(rawISSN)
		if issnl != "" {
			containerID, err := fatcat2.LookupIssnl(client, issnl)
			if err != nil {
				return nil, fmt.Errorf("issnl lookup failed: %w", err)
			}

			if containerID == nil && container.Title != "" {
				c := fatcat2.Container{
					Name:      cleaning.CleanString(container.Title),
					ISSNL:     issnl,
					Publisher: release.Publisher,
					Type:      containerType,
					Source:    release.Source,
				}
				containerID, err = fatcat2.CreateContainer(client, &c)
				if err != nil {
					return nil, fmt.Errorf("container creation failed: %w", err)
				}
				cs.Containers.Added++

				containerDoc := indexing.PrepareFatcatContainerDoc(c)
				bs, err := json.Marshal(containerDoc)
				if err != nil {
					return nil, fmt.Errorf("could not marshal container doc: %w", err)
				}
				err = indexing.DoElasticIndex(client,
					viper.GetString("indexing.fatcat_container_ix"), containerDoc.LegacyIdent, bs)
				if err != nil {
					return nil, fmt.Errorf("could not index container: %w", err)
				}
			} else if containerID != nil {
				cs.Containers.Ignored++
			}

			release.ContainerID = containerID
		} else if container.Title != "" {
			// ISSNL not found but we have a title: store as container_name
			if _, ok := release.Extra["container_name"]; !ok {
				release.Extra["container_name"] = container.Title
			}
			cs.Containers.Skipped++
		}
	} else if containerType == "" && container.Title != "" {
		// Container type not recognized (e.g. DataRepository): stash name only
		if _, ok := release.Extra["container_name"]; !ok {
			release.Extra["container_name"] = container.Title
		}
		cs.Containers.Skipped++
	}

	// micropublication.org fallback
	if release.ContainerID == nil {
		if _, ok := release.Extra["container_name"]; !ok {
			if strings.HasPrefix(strings.ToLower(release.Publisher), "micropublication") {
				release.Extra["container_name"] = release.Publisher
			}
		}
	}

	id, err := fatcat2.CreateRelease(client, release)
	if err != nil {
		return nil, fmt.Errorf("release creation failed: %w", err)
	}

	r, err := fatcat2.GetRelease(client, *id)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch new release %q: %w", id, err)
	}

	releaseDoc, err := indexing.PrepareFatcatReleaseDoc(client, r)
	if err != nil {
		return nil, fmt.Errorf("failed to transform release into ES doc: %w", err)
	}

	bs, err := json.Marshal(releaseDoc)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal release ES doc: %w", err)
	}

	err = indexing.DoElasticIndex(client,
		viper.GetString("indexing.fatcat_release_ix"), releaseDoc.LegacyIdent, bs)
	if err != nil {
		return nil, fmt.Errorf("failed to index release: %w", err)
	}

	return &r, nil
}

// --- Skip logic ---

func skipReason(doc *dataciteDoc) string {
	doi := strings.TrimSpace(doc.Attributes.DOI)
	if doi == "" {
		return "no-doi"
	}
	for _, b := range []byte(doi) {
		if b > 127 {
			return "non-ascii-doi"
		}
	}

	title, _, _ := parseTitles(doc.Attributes.Titles)
	title = cleaning.CleanString(strings.TrimSpace(title))
	if title == "" {
		return "no-title"
	}
	if isSpamTitle(title) {
		return "spam-title"
	}

	return ""
}

// --- ProcessLine ---

func ProcessLine(ctx context.Context, client *http.Client, source string, lineb []byte) (counts.Counts, *fatcat2.Release, error) {
	out := counts.Counts{}
	l := activity.GetLogger(ctx)

	var release *fatcat2.Release

	var doc dataciteDoc
	if err := json.Unmarshal(lineb, &doc); err != nil {
		return out, release, fmt.Errorf("datacite unmarshal: %w", err)
	}

	doi := strings.ToLower(strings.TrimSpace(doc.Attributes.DOI))

	if reason := skipReason(&doc); reason != "" {
		l.Info("datacite: skipping record", "doi", doi, "reason", reason)
		out.Releases.Skipped++
		return out, release, nil
	}

	release = dataciteToFc(&doc, source)

	foundID, err := fatcat2.LookupDoi(client, doi)
	if err != nil {
		return out, release, fmt.Errorf("doi lookup failed for %q: %w", doi, err)
	}

	if foundID != nil {
		l.Debug("datacite: found existing release", "doi", doi, "release_id", foundID)
		r, err := fatcat2.GetRelease(client, *foundID)
		if err != nil {
			return out, release, fmt.Errorf("could not fetch existing release: %w", err)
		}
		release = &r
		out.Releases.Ignored++
	} else {
		release, err = createRelease(client, &out, *release, &doc)
		if err != nil {
			return out, release, fmt.Errorf("could not create release for doi %q: %w", doi, err)
		}
		l.Debug("datacite: created release", "doi", doi, "release_id", release.ID)
		out.Releases.Added++
	}

	return out, release, nil
}
