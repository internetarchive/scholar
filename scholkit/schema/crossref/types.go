package crossref

import "encoding/json"

type DatePart []int64

// Author is a crossref author.
type Author struct {
	Affiliation []struct {
		ID []struct {
			ID         string `json:"id,omitempty"`
			IDType     string `json:"id-type,omitempty"`
			AssertedBy string `json:"asserted-by"`
		} `json:"id,omitempty"`
		Name  string   `json:"name"`
		Place []string `json:"place"`
	} `json:"affiliation,omitempty"`
	Family             string `json:"family,omitempty"`
	Given              string `json:"given,omitempty"`
	Sequence           string `json:"sequence,omitempty"`
	ORCID              string `json:"orcid,omitempty"`
	AuthenticatedORCID bool   `json:"authenticated-orcid"`
}

// Work is a crossref API works 1.0.0 document, as documented in
// https://www.crossref.org/documentation/retrieve-metadata/rest-api/.  This
// struct only contains the message part.
type Work struct {
	Abstract       string          `json:"abstract"`
	Author         json.RawMessage `json:"author"` // temp fix for issue
	ContainerTitle []string        `json:"container-title,omitempty"`
	ContentDomain  struct {
		CrossmarkRestriction bool            `json:"crossmark-restriction,omitempty"`
		Domain               json.RawMessage `json:"domain,omitempty"`
	} `json:"content-domain"`
	Created struct {
		DateParts []DatePart `json:"date-parts,omitempty"`
		DateTime  string     `json:"date-time,omitempty"`
		Timestamp int64      `json:"timestamp,omitempty"`
	} `json:"created"`
	DOI       string
	Deposited struct {
		DateParts []DatePart `json:"date-parts,omitempty"`
		DateTime  string     `json:"date-time,omitempty"`
		Timestamp int64      `json:"timestamp,omitempty"`
	} `json:"deposited"`
	Editor  json.RawMessage `json:"editor"`
	ISSN    []string
	Indexed struct {
		DateParts []DatePart `json:"date-parts,omitempty"`
		DateTime  string     `json:"date-time,omitempty"`
		Timestamp int64      `json:"timestamp,omitempty"`
	} `json:"indexed"`
	IsReferencedByCount int64 `json:"is-referenced-by-count,omitempty"`
	IssnType            []struct {
		Type  string `json:"type,omitempty"`
		Value string `json:"value,omitempty"`
	} `json:"issn-type"`
	Issue  string `json:"issue,omitempty"`
	Issued struct {
		DateParts []DatePart `json:"date-parts,omitempty"`
	} `json:"issued"`
	JournalIssue struct {
		Issue string `json:"issue,omitempty"`
	} `json:"journal-issue,omitempty"`
	License []struct {
		ContentVersion string `json:"content-version,omitempty"`
		DelayInDays    int64  `json:"delay-in-days,omitempty"`
		Start          struct {
			DateParts []DatePart `json:"date-parts,omitempty"`
			DateTime  string     `json:"date-time,omitempty"`
			Timestamp int64      `json:"timestamp,omitempty"`
		} `json:"start,omitempty"`
		URL string
	} `json:"license,omitempty"`
	Link []struct {
		ContentType         string `json:"content-type,omitempty"`
		ContentVersion      string `json:"content-version,omitempty"`
		IntendedApplication string `json:"intended-application,omitempty"`
		URL                 string
	} `json:"link,omitempty"`
	Member        string          `json:"member,omitempty"`
	OriginalTitle json.RawMessage `json:"original-title,omitempty"`
	Page          string          `json:"page,omitempty"`
	Prefix        string          `json:"prefix,omitempty"`
	Published     struct {
		DateParts []DatePart `json:"date-parts,omitempty"`
	} `json:"published,omitempty"`
	PublishedPrint struct {
		DateParts []DatePart `json:"date-parts,omitempty"`
	} `json:"published-print,omitempty"`
	Publisher       string `json:"publisher,omitempty"`
	ReferenceCount  int64  `json:"reference-count,omitempty"`
	ReferencesCount int64  `json:"references-count,omitempty"`
	Relation        struct {
	} `json:"relation,omitempty"`
	Resource struct {
		Primary struct {
			URL string
		} `json:"primary,omitempty"`
	} `json:"resource,omitempty"`
	Score               interface{}     `json:"score,omitempty"`
	ShortContainerTitle []string        `json:"short-container-title,omitempty"`
	ShortTitle          json.RawMessage `json:"short-title,omitempty"`
	Source              string          `json:"source,omitempty"`
	Subject             []string        `json:"subject,omitempty"`
	Subtitle            json.RawMessage `json:"subtitle,omitempty"`
	Title               []string        `json:"title,omitempty"`
	Translator          json.RawMessage `json:"translator,omitempty"`
	Type                string          `json:"type,omitempty"`
	URL                 string
	Volume              string `json:"volume,omitempty"`
}
