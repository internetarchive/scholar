package indexing

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/spf13/viper"
)

type FulltextDocV1 struct {
	Key             string       `json:"key"`
	Type            string       `json:"doc_type"`
	LegacyWorkIdent string       `json:"work_ident"`
	Fulltext        FulltextV1   `json:"fulltext"`
	IndexTime       time.Time    `json:"doc_index_ts"`
	CollapseKey     string       `json:"collapse_key"`
	Tags            []string     `json:"tags"`
	Biblio          BiblioV1     `json:"biblio"`
	Abstracts       []AbstractV1 `json:"abstracts"`
	Releases        []ReleaseV1  `json:"releases"`
	Access          []AccessV1   `json:"access"`

	// known unneeded for now (lots of this in ES currently but we have no plans atm to add more)
	// ia_sim
}

// TODO i should probably use the legacy style fatcat ids for indexing just to keep things consistent

type BiblioV1 struct {
	BiblioCommonV1
	LegacyReleaseIdent    string   `json:"release_ident"`
	Subtitle              string   `json:"subtitle,omitempty"`
	OriginalTitle         string   `json:"original_title,omitempty"`
	Language              string   `json:"lang_code,omitempty"`
	CountryCode           string   `json:"country_code,omitempty"`
	Volume                string   `json:"volume,omitempty"`
	VolumeInt             int      `json:"volume_int,omitempty"`
	Issue                 string   `json:"issue,omitempty"`
	IssueInt              int      `json:"issue_int,omitempty"`
	Pages                 string   `json:"pages,omitempty"`
	FirstPage             string   `json:"first_page,omitempty"`
	FirstPageInt          int      `json:"first_page_int,omitempty"`
	Number                string   `json:"number,omitempty"`
	Publisher             string   `json:"publisher,omitempty"`
	PublisherType         string   `json:"publisher_type,omitempty"`
	ISSNs                 []string `json:"issns,omitempty"`
	ContainerOriginalName string   `json:"container_original_name,omitempty"`
	ContainerWikidataQID  string   `json:"container_wikidata_qid,omitempty"`
	ContainerSherpaColor  string   `json:"container_sherpa_color,omitempty"`
	ContribCount          int      `json:"contrib_count,omitempty"`
	ContribNames          []string `json:"contrib_names,omitempty"`
	Affiliations          []string `json:"affiliations,omitempty"`
}

type FulltextV1 struct {
	Language           string `json:"lang_code,omitempty"`
	Body               string `json:"body,omitempty"`
	Acknowledgement    string `json:"acknowledgement,omitempty"`
	Annex              string `json:"annex,omitempty"`
	LegacyReleaseIdent string `json:"release_ident"`
	LegacyFileIdent    string `json:"file_ident"`
	FileSha1           string `json:"file_sha1"`
	FileMimetype       string `json:"file_mimetype"`
	Size               int    `json:"size_bytes"`
	// TODO this field annoys me since in theory it's always just synthesizable
	// from the FileSha1; perhaps there are exceptions to that, though...
	Thumbnail  string `json:"thumbnail_url,omitempty"`
	AccessURL  string `json:"access_url,omitempty"`
	AccessType string `json:"access_type,omitempty"`
}

type AbstractV1 struct {
	Body     string `json:"body"`
	Language string `json:"lang_code"`
}

// TODO i should probably use the legacy style fatcat ids for indexing just to keep things consistent

type ReleaseV1 struct {
	BiblioCommonV1
	LegacyIdent string `json:"ident"`
	Revision    string `json:"revision,omitempty"`
}

type AccessV1 struct {
	Type               string `json:"access_type"`
	Url                string `json:"access_url"`
	Mimetype           string `json:"mimetype"`
	LegacyFileIdent    string `json:"file_ident"`
	LegacyReleaseIdent string `json:"release_ident"`
}

// BiblioCommon stores metadata used both in top level "biblio" key as well on on items in "releases" key
type BiblioCommonV1 struct {
	Title                string     `json:"title"`
	ReleaseDate          *time.Time `json:"release_date"`
	ReleaseYear          int        `json:"release_year,omitempty"`
	ReleaseStage         string     `json:"release_stage,omitempty"`
	ReleaseType          string     `json:"release_type"`
	WithdrawnStatus      string     `json:"withdrawn_status,omitempty"`
	LicenseSlug          string     `json:"license_slug,omitempty"`
	ContainerName        string     `json:"container_name,omitempty"`
	LegacyContainerIdent string     `json:"container_ident,omitempty"`
	ContainerISSNL       string     `json:"container_issnl,omitempty"`
	ContainerType        string     `json:"container_type,omitempty"`
	DOI                  string     `json:"doi,omitempty"`
	DOIPrefix            string     `json:"doi_prefix,omitempty"`
	DOIRegistrar         string     `json:"doi_registrar,omitempty"`
	PMID                 string     `json:"pmid,omitempty"`
	PMCID                string     `json:"pmcid,omitempty"`
	ISBN13               string     `json:"isbn13,omitempty"`
	WikidataQID          string     `json:"wikidata_qid,omitempty"`
	ArxivID              string     `json:"arxiv_id,omitempty"`
	JstorID              string     `json:"jstor_id,omitempty"`
	DOAJID               string     `json:"doaj_id,omitempty"`
	DBLPID               string     `json:"dblp_id,omitempty"`
	OAIID                string     `json:"oai_id,omitempty"`
}

/*
------
|both|
------
title
release_date
release_year
release_type
release_stage
withdrawn_status
doi
doi_prefix
doi_registrar
pmid
pmcid
isbn13
wikidata_qid
arxiv_id
jstor_id
doaj_id
dblp_id
oai_id
license_slug
container_name
container_ident
container_issnl

--------
|biblio|
--------
release_ident
subtitle
original_title
lang_code
country_code
volume
volume_int
issue
issue_int
pages
first_page
first_page_int
number
publisher
publisher_type
container_original_name
container_wikidata_qid
container_sherpa_color
issns
container_type
contrib_count
contrib_names
affiliations

---------
|release|
---------
ident
revision
container_type

*/

// TODO would use this for bulk ops
type ElasticPayload struct {
	Index  string `json:"_index"`
	OpType string `json:"_op_type"`
	ID     string `json:"_id"`
	// Source is a JSON representation of the document you want to ingest
	Source string `json:"_source"`
}

func Ingest(client *http.Client, doc FulltextDocV1) error {
	u := fmt.Sprintf("%s/%s/_doc/%d",
		viper.GetString("indexing.elastic_url"),
		viper.GetString("indexing.elastic_index"),
		doc.Key)

	docJson, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("could not serialize es doc: %w", err)
	}

	req, err := http.NewRequest("POST", u, bytes.NewReader(docJson))
	if err != nil {
		return fmt.Errorf("could not prepare elasticsearch POST: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to POST elasticsearch: %w", err)
	}

	if resp.StatusCode != 200 {
		var body string
		bs, err := io.ReadAll(resp.Body)
		if err == nil {
			body = string(bs)
		}

		return fmt.Errorf("elasticsearch failed to index: '%s'", body)
	}

	return nil
}
