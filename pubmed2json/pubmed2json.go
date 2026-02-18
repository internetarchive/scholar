// Package pubmed2json decodes PubMed XML update files into Go types and
// provides helpers for streaming the records as newline-delimited JSON.
//
// # Streaming decoder
//
// Use [NewDecoder] to iterate over records one at a time without buffering
// the entire file in memory:
//
//	dec := pubmed2json.NewDecoder(r)
//	for {
//	    rec, err := dec.Next()
//	    if err == io.EOF {
//	        break
//	    }
//	    if err != nil {
//	        log.Fatal(err)
//	    }
//	    // rec.Type is "article" or "deleteCitation"
//	    if rec.Article != nil {
//	        fmt.Println(rec.Article.MedlineCitation.PMID.Value)
//	    }
//	}
//
// # Bulk conversion
//
// Use [Convert] to write every record as a JSON line to an [io.Writer]:
//
//	stats, err := pubmed2json.Convert(r, w)
package pubmed2json

import (
	"bufio"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"regexp"
	"strings"
)

var xmlTagRe = regexp.MustCompile(`<[^>]+>`)

func stripTags(s string) string {
	return strings.TrimSpace(xmlTagRe.ReplaceAllString(s, ""))
}

// MarkupString is a string whose XML source may contain inline markup
// (b, i, sub, sup, u, mml:math). Tags are stripped on decode; only text
// content is retained.
type MarkupString string

func (m *MarkupString) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	var raw struct {
		Inner string `xml:",innerxml"`
	}
	if err := d.DecodeElement(&raw, &start); err != nil {
		return err
	}
	*m = MarkupString(stripTags(raw.Inner))
	return nil
}

// PMID is the PubMed identifier with an optional version attribute.
type PMID struct {
	Version string `xml:"Version,attr" json:"version,omitempty"`
	Value   string `xml:",chardata" json:"value"`
}

// Date represents a year/month/day structured date.
type Date struct {
	Year  string `xml:"Year" json:"year,omitempty"`
	Month string `xml:"Month" json:"month,omitempty"`
	Day   string `xml:"Day" json:"day,omitempty"`
}

// ISSN is an International Standard Serial Number with type classification.
type ISSN struct {
	IssnType string `xml:"IssnType,attr" json:"type,omitempty"`
	Value    string `xml:",chardata" json:"value"`
}

// PubDate is the publication date, which may be structured or a free-text MedlineDate.
type PubDate struct {
	Year        string `xml:"Year" json:"year,omitempty"`
	Month       string `xml:"Month" json:"month,omitempty"`
	Day         string `xml:"Day" json:"day,omitempty"`
	Season      string `xml:"Season" json:"season,omitempty"`
	MedlineDate string `xml:"MedlineDate" json:"medlineDate,omitempty"`
}

// JournalIssue holds volume/issue/date information for a journal issue.
type JournalIssue struct {
	CitedMedium string  `xml:"CitedMedium,attr" json:"citedMedium,omitempty"`
	Volume      string  `xml:"Volume" json:"volume,omitempty"`
	Issue       string  `xml:"Issue" json:"issue,omitempty"`
	PubDate     PubDate `xml:"PubDate" json:"pubDate"`
}

// Journal describes the journal in which an article was published.
type Journal struct {
	ISSN            *ISSN        `xml:"ISSN" json:"issn,omitempty"`
	JournalIssue    JournalIssue `xml:"JournalIssue" json:"journalIssue"`
	Title           string       `xml:"Title" json:"title,omitempty"`
	ISOAbbreviation string       `xml:"ISOAbbreviation" json:"isoAbbreviation,omitempty"`
}

// Pagination holds the page range for an article within its journal issue.
type Pagination struct {
	MedlinePgn string `xml:"MedlinePgn" json:"medlinePgn,omitempty"`
}

// ELocationID is an electronic location identifier (e.g., DOI, pii).
type ELocationID struct {
	EIdType string `xml:"EIdType,attr" json:"type,omitempty"`
	ValidYN string `xml:"ValidYN,attr" json:"validYN,omitempty"`
	Value   string `xml:",chardata" json:"value"`
}

// AbstractText is a section of an abstract, optionally labeled and categorized.
// Text content is stripped of inline markup (b, i, sub, sup, u, mml:math).
type AbstractText struct {
	Label       string `json:"label,omitempty"`
	NlmCategory string `json:"nlmCategory,omitempty"`
	Text        string `json:"text"`
}

func (a *AbstractText) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	for _, attr := range start.Attr {
		switch attr.Name.Local {
		case "Label":
			a.Label = attr.Value
		case "NlmCategory":
			a.NlmCategory = attr.Value
		}
	}
	var raw struct {
		Inner string `xml:",innerxml"`
	}
	if err := d.DecodeElement(&raw, &start); err != nil {
		return err
	}
	a.Text = stripTags(raw.Inner)
	return nil
}

// Abstract is the article abstract, consisting of one or more text sections.
type Abstract struct {
	Texts                []AbstractText `xml:"AbstractText" json:"texts,omitempty"`
	CopyrightInformation string         `xml:"CopyrightInformation" json:"copyrightInformation,omitempty"`
}

// OtherAbstract is an abstract in a language or from a source other than the primary.
type OtherAbstract struct {
	Type                 string         `xml:"Type,attr" json:"type,omitempty"`
	Language             string         `xml:"Language,attr" json:"language,omitempty"`
	Texts                []AbstractText `xml:"AbstractText" json:"texts,omitempty"`
	CopyrightInformation string         `xml:"CopyrightInformation" json:"copyrightInformation,omitempty"`
}

// Identifier is a contributor identifier (e.g., ORCID).
type Identifier struct {
	Source string `xml:"Source,attr" json:"source,omitempty"`
	Value  string `xml:",chardata" json:"value"`
}

// AffiliationInfo holds an author's institutional affiliation.
type AffiliationInfo struct {
	Affiliation string       `xml:"Affiliation" json:"affiliation,omitempty"`
	Identifiers []Identifier `xml:"Identifier" json:"identifiers,omitempty"`
}

// Author is a contributor to the article.
type Author struct {
	ValidYN         string            `xml:"ValidYN,attr" json:"validYN,omitempty"`
	LastName        string            `xml:"LastName" json:"lastName,omitempty"`
	ForeName        string            `xml:"ForeName" json:"foreName,omitempty"`
	Initials        string            `xml:"Initials" json:"initials,omitempty"`
	Suffix          string            `xml:"Suffix" json:"suffix,omitempty"`
	CollectiveName  string            `xml:"CollectiveName" json:"collectiveName,omitempty"`
	AffiliationInfo []AffiliationInfo `xml:"AffiliationInfo" json:"affiliationInfo,omitempty"`
	Identifiers     []Identifier      `xml:"Identifier" json:"identifiers,omitempty"`
}

// AuthorList is the list of article authors.
type AuthorList struct {
	CompleteYN string   `xml:"CompleteYN,attr" json:"completeYN,omitempty"`
	Authors    []Author `xml:"Author" json:"authors,omitempty"`
}

// Grant is a funding grant associated with the article.
type Grant struct {
	GrantID string `xml:"GrantID" json:"grantID,omitempty"`
	Acronym string `xml:"Acronym" json:"acronym,omitempty"`
	Agency  string `xml:"Agency" json:"agency,omitempty"`
	Country string `xml:"Country" json:"country,omitempty"`
}

// GrantList is the list of grants funding the article.
type GrantList struct {
	CompleteYN string  `xml:"CompleteYN,attr" json:"completeYN,omitempty"`
	Grants     []Grant `xml:"Grant" json:"grants,omitempty"`
}

// PublicationType classifies the article type (e.g., "Journal Article", "Review").
type PublicationType struct {
	UI    string `xml:"UI,attr" json:"ui,omitempty"`
	Value string `xml:",chardata" json:"value"`
}

// DataBank is a referenced biological database with accession numbers.
type DataBank struct {
	DataBankName     string   `xml:"DataBankName" json:"dataBankName,omitempty"`
	AccessionNumbers []string `xml:"AccessionNumberList>AccessionNumber" json:"accessionNumbers,omitempty"`
}

// ArticleDate is the electronic publication date of the article.
type ArticleDate struct {
	DateType string `xml:"DateType,attr" json:"dateType,omitempty"`
	Year     string `xml:"Year" json:"year,omitempty"`
	Month    string `xml:"Month" json:"month,omitempty"`
	Day      string `xml:"Day" json:"day,omitempty"`
}

// Article is the core bibliographic record for a PubMed article.
type Article struct {
	PubModel         string           `xml:"PubModel,attr" json:"pubModel,omitempty"`
	Journal          Journal          `xml:"Journal" json:"journal"`
	ArticleTitle     MarkupString     `xml:"ArticleTitle" json:"articleTitle"`
	VernacularTitle  MarkupString     `xml:"VernacularTitle" json:"vernacularTitle,omitempty"`
	Pagination       *Pagination      `xml:"Pagination" json:"pagination,omitempty"`
	ELocationIDs     []ELocationID    `xml:"ELocationID" json:"eLocationIDs,omitempty"`
	Abstract         *Abstract        `xml:"Abstract" json:"abstract,omitempty"`
	AuthorList       *AuthorList      `xml:"AuthorList" json:"authorList,omitempty"`
	Language         []string         `xml:"Language" json:"language,omitempty"`
	GrantList        *GrantList       `xml:"GrantList" json:"grantList,omitempty"`
	PublicationTypes []PublicationType `xml:"PublicationTypeList>PublicationType" json:"publicationTypes,omitempty"`
	DataBankList     []DataBank       `xml:"DataBankList>DataBank" json:"dataBankList,omitempty"`
	ArticleDates     []ArticleDate    `xml:"ArticleDate" json:"articleDates,omitempty"`
}

// MedlineJournalInfo identifies the journal in the Medline database.
type MedlineJournalInfo struct {
	Country     string `xml:"Country" json:"country,omitempty"`
	MedlineTA   string `xml:"MedlineTA" json:"medlineTA,omitempty"`
	NlmUniqueID string `xml:"NlmUniqueID" json:"nlmUniqueID,omitempty"`
	ISSNLinking string `xml:"ISSNLinking" json:"issnLinking,omitempty"`
}

// NameOfSubstance is a chemical substance name with MeSH UI.
type NameOfSubstance struct {
	UI    string `xml:"UI,attr" json:"ui,omitempty"`
	Value string `xml:",chardata" json:"value"`
}

// Chemical is a substance associated with the article.
type Chemical struct {
	RegistryNumber  string          `xml:"RegistryNumber" json:"registryNumber,omitempty"`
	NameOfSubstance NameOfSubstance `xml:"NameOfSubstance" json:"nameOfSubstance"`
}

// DescriptorName is a MeSH descriptor term.
type DescriptorName struct {
	UI           string `xml:"UI,attr" json:"ui,omitempty"`
	MajorTopicYN string `xml:"MajorTopicYN,attr" json:"majorTopicYN,omitempty"`
	Type         string `xml:"Type,attr" json:"type,omitempty"`
	Value        string `xml:",chardata" json:"value"`
}

// QualifierName is a MeSH subheading qualifier.
type QualifierName struct {
	UI           string `xml:"UI,attr" json:"ui,omitempty"`
	MajorTopicYN string `xml:"MajorTopicYN,attr" json:"majorTopicYN,omitempty"`
	Value        string `xml:",chardata" json:"value"`
}

// MeshHeading is a MeSH subject heading with optional qualifiers.
type MeshHeading struct {
	DescriptorName DescriptorName  `xml:"DescriptorName" json:"descriptorName"`
	QualifierNames []QualifierName `xml:"QualifierName" json:"qualifierNames,omitempty"`
}

// Keyword is a free-text keyword provided by the author or publisher.
type Keyword struct {
	MajorTopicYN string `xml:"MajorTopicYN,attr" json:"majorTopicYN,omitempty"`
	Value        string `xml:",chardata" json:"value"`
}

// KeywordList is a set of keywords from a given source (Owner).
// Multiple KeywordLists may exist per citation (one per owner/source).
type KeywordList struct {
	Owner    string    `xml:"Owner,attr" json:"owner,omitempty"`
	Keywords []Keyword `xml:"Keyword" json:"keywords,omitempty"`
}

// SupplMeshName is a Supplemental MeSH concept name.
type SupplMeshName struct {
	Type  string `xml:"Type,attr" json:"type,omitempty"`
	UI    string `xml:"UI,attr" json:"ui,omitempty"`
	Value string `xml:",chardata" json:"value"`
}

// PersonalNameSubject is a person's name used as a subject heading.
type PersonalNameSubject struct {
	LastName string `xml:"LastName" json:"lastName,omitempty"`
	ForeName string `xml:"ForeName" json:"foreName,omitempty"`
	Initials string `xml:"Initials" json:"initials,omitempty"`
	Suffix   string `xml:"Suffix" json:"suffix,omitempty"`
}

// Investigator is a named investigator in a study (from InvestigatorList).
type Investigator struct {
	ValidYN         string            `xml:"ValidYN,attr" json:"validYN,omitempty"`
	LastName        string            `xml:"LastName" json:"lastName,omitempty"`
	ForeName        string            `xml:"ForeName" json:"foreName,omitempty"`
	Initials        string            `xml:"Initials" json:"initials,omitempty"`
	Suffix          string            `xml:"Suffix" json:"suffix,omitempty"`
	CollectiveName  string            `xml:"CollectiveName" json:"collectiveName,omitempty"`
	AffiliationInfo []AffiliationInfo `xml:"AffiliationInfo" json:"affiliationInfo,omitempty"`
	Identifiers     []Identifier      `xml:"Identifier" json:"identifiers,omitempty"`
}

// InvestigatorList is the list of named study investigators.
type InvestigatorList struct {
	CompleteYN    string         `xml:"CompleteYN,attr" json:"completeYN,omitempty"`
	Investigators []Investigator `xml:"Investigator" json:"investigators,omitempty"`
}

// CommentsCorrections links related citations (errata, comments, retractions, etc.).
type CommentsCorrections struct {
	RefType   string `xml:"RefType,attr" json:"refType,omitempty"`
	RefSource string `xml:"RefSource" json:"refSource,omitempty"`
	PMID      *PMID  `xml:"PMID" json:"pmid,omitempty"`
	Note      string `xml:"Note" json:"note,omitempty"`
}

// OtherID is an identifier from a source other than NLM (e.g., PMC, NASA).
type OtherID struct {
	Source string `xml:"Source,attr" json:"source,omitempty"`
	Value  string `xml:",chardata" json:"value"`
}

// MedlineCitation is the full Medline bibliographic record for an article.
type MedlineCitation struct {
	Status                  string                `xml:"Status,attr" json:"status,omitempty"`
	IndexingMethod          string                `xml:"IndexingMethod,attr" json:"indexingMethod,omitempty"`
	Owner                   string                `xml:"Owner,attr" json:"owner,omitempty"`
	PMID                    PMID                  `xml:"PMID" json:"pmid"`
	DateCompleted           *Date                 `xml:"DateCompleted" json:"dateCompleted,omitempty"`
	DateRevised             *Date                 `xml:"DateRevised" json:"dateRevised,omitempty"`
	Article                 Article               `xml:"Article" json:"article"`
	MedlineJournalInfo      MedlineJournalInfo    `xml:"MedlineJournalInfo" json:"medlineJournalInfo"`
	CitationSubset          []string              `xml:"CitationSubset" json:"citationSubset,omitempty"`
	CommentsCorrectionsList []CommentsCorrections `xml:"CommentsCorrectionsList>CommentsCorrections" json:"commentsCorrections,omitempty"`
	ChemicalList            []Chemical            `xml:"ChemicalList>Chemical" json:"chemicals,omitempty"`
	SupplMeshList           []SupplMeshName       `xml:"SupplMeshList>SupplMeshName" json:"supplMesh,omitempty"`
	MeshHeadingList         []MeshHeading         `xml:"MeshHeadingList>MeshHeading" json:"meshHeadings,omitempty"`
	KeywordLists            []KeywordList         `xml:"KeywordList" json:"keywordLists,omitempty"`
	PersonalNameSubjects    []PersonalNameSubject `xml:"PersonalNameSubjectList>PersonalNameSubject" json:"personalNameSubjects,omitempty"`
	InvestigatorList        *InvestigatorList     `xml:"InvestigatorList" json:"investigatorList,omitempty"`
	OtherIDs                []OtherID             `xml:"OtherID" json:"otherIDs,omitempty"`
	OtherAbstracts          []OtherAbstract       `xml:"OtherAbstract" json:"otherAbstracts,omitempty"`
	CoiStatement            string                `xml:"CoiStatement" json:"coiStatement,omitempty"`
	NumberOfReferences      string                `xml:"NumberOfReferences" json:"numberOfReferences,omitempty"`
}

// PubMedPubDate is a dated entry in the article's publication history.
type PubMedPubDate struct {
	PubStatus string `xml:"PubStatus,attr" json:"pubStatus,omitempty"`
	Year      string `xml:"Year" json:"year,omitempty"`
	Month     string `xml:"Month" json:"month,omitempty"`
	Day       string `xml:"Day" json:"day,omitempty"`
	Hour      string `xml:"Hour" json:"hour,omitempty"`
	Minute    string `xml:"Minute" json:"minute,omitempty"`
}

// ArticleId is an identifier for an article (e.g., pubmed, pmc, doi, pii).
type ArticleId struct {
	IdType string `xml:"IdType,attr" json:"type,omitempty"`
	Value  string `xml:",chardata" json:"value"`
}

// Reference is a bibliographic reference cited in the article.
type Reference struct {
	Citation   string      `xml:"Citation" json:"citation,omitempty"`
	ArticleIds []ArticleId `xml:"ArticleIdList>ArticleId" json:"articleIds,omitempty"`
}

// PubmedData holds publication status, history, IDs, and references.
type PubmedData struct {
	History           []PubMedPubDate `xml:"History>PubMedPubDate" json:"history,omitempty"`
	PublicationStatus string          `xml:"PublicationStatus" json:"publicationStatus,omitempty"`
	ArticleIds        []ArticleId     `xml:"ArticleIdList>ArticleId" json:"articleIds,omitempty"`
	ReferenceList     []Reference     `xml:"ReferenceList>Reference" json:"references,omitempty"`
}

// PubmedArticle is the top-level element for a single article record.
type PubmedArticle struct {
	MedlineCitation MedlineCitation `xml:"MedlineCitation" json:"medlineCitation"`
	PubmedData      *PubmedData     `xml:"PubmedData" json:"pubmedData,omitempty"`
}

// DeleteCitation lists PMIDs that have been deleted from PubMed.
type DeleteCitation struct {
	PMIDs []PMID `xml:"PMID" json:"pmids"`
}

// Record is the top-level output type for each entry in the stream.
// Type is "article" or "deleteCitation"; exactly one of Article or
// DeleteCitation will be non-nil.
type Record struct {
	Type           string          `json:"type"`
	Article        *PubmedArticle  `json:"article,omitempty"`
	DeleteCitation *DeleteCitation `json:"deleteCitation,omitempty"`
}

// Decoder reads a PubMed XML stream and returns one [Record] at a time.
// Create one with [NewDecoder].
type Decoder struct {
	d *xml.Decoder
}

// NewDecoder returns a Decoder that reads from r.
// r should supply uncompressed XML; wrap with [compress/gzip.NewReader] for .gz files.
func NewDecoder(r io.Reader) *Decoder {
	return &Decoder{d: xml.NewDecoder(r)}
}

// Next returns the next record from the stream.
// It returns [io.EOF] when the stream is exhausted, and any other error
// if the XML is malformed or an I/O error occurs.
func (d *Decoder) Next() (*Record, error) {
	for {
		tok, err := d.d.Token()
		if err == io.EOF {
			return nil, io.EOF
		}
		if err != nil {
			return nil, fmt.Errorf("reading XML token: %w", err)
		}

		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}

		switch se.Name.Local {
		case "PubmedArticle":
			var article PubmedArticle
			if err := d.d.DecodeElement(&article, &se); err != nil {
				return nil, fmt.Errorf("decoding PubmedArticle: %w", err)
			}
			return &Record{Type: "article", Article: &article}, nil

		case "DeleteCitation":
			var del DeleteCitation
			if err := d.d.DecodeElement(&del, &se); err != nil {
				return nil, fmt.Errorf("decoding DeleteCitation: %w", err)
			}
			return &Record{Type: "deleteCitation", DeleteCitation: &del}, nil
		}
	}
}

// ConvertStats holds the counts of records written by [Convert].
type ConvertStats struct {
	Articles        int
	DeleteCitations int
}

// Convert reads PubMed XML from r and writes each record as a JSON line to w.
// It returns counts of article and deleteCitation records written.
// r should supply uncompressed XML; wrap with [compress/gzip.NewReader] for .gz files.
func Convert(r io.Reader, w io.Writer) (ConvertStats, error) {
	bw := bufio.NewWriterSize(w, 1<<20)
	enc := json.NewEncoder(bw)
	dec := NewDecoder(r)

	var stats ConvertStats
	for {
		rec, err := dec.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return stats, err
		}
		if err := enc.Encode(rec); err != nil {
			return stats, fmt.Errorf("encoding JSON: %w", err)
		}
		switch rec.Type {
		case "article":
			stats.Articles++
		case "deleteCitation":
			stats.DeleteCitations++
		}
	}

	if err := bw.Flush(); err != nil {
		return stats, fmt.Errorf("flushing output: %w", err)
	}
	return stats, nil
}
