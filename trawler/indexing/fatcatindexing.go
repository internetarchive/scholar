package indexing

import "time"

/*

TODO

need to understand the other indexing workers:

- fatcat-elasticsearch-changelog-worker
- fatcat-elasticsearch-container-worker
- fatcat-elasticsearch-file-worker
- fatcat-elasticsearch-release-worker

I think we can punt on the changelog worker for now; changelog isn't much of a
thing anymore. it made sense to index it when a changelog entry could consist
of a bunch of different operations on different entities.

but the others we should consider and each one comes up in this new pipeline.

a complicating factor is not having qa versions of these indices, so:

- [ ] create QA indices for container, file, release
- [X] release transform code
- [ ] container transform code
- [ ] file transform code
- [ ] decide when to do the indexing...right after adding? yes, should probably do that
- [ ] how different is fatcat release index payload from scholar? probably a lot :(
- [X] get mapping/sample for release
- [X] get mapping/sample for container
- [X] get mapping/sample for file
- [X] when is fatcat_ref populated? it isn't mentioned in workers doc
  - only via refcat stuff so ignoring for now

*/

/*
Doc ID is ident
*/
type FatcatReleaseDocV1 struct {
	LegacyIdent     string    `json:"ident,omitempty"`
	IndexTime       time.Time `json:"doc_index_ts"`
	State           string    `json:"state,omitempty"`
	LegacyWorkIdent string    `json:"work_id,omitempty"`
	Title           string    `json:"title,omitempty"`
	Subtitle        string    `json:"subtitle,omitempty"`
	OriginalTitle   string    `json:"original_title,omitempty"`
	Type            string    `json:"release_type,omitempty"`
	Stage           string    `json:"release_stage,omitempty"`
	WithdrawnStatus string    `json:"withdrawn_status,omitempty"`
	Language        string    `json:"language,omitempty"`
	Volume          string    `json:"volume,omitempty"`
	Issue           string    `json:"issue,omitempty"`
	Pages           string    `json:"pages,omitempty"`
	Number          string    `json:"number,omitempty"`
	License         string    `json:"license,omitempty"`
	Version         string    `json:"version,omitempty"`
	Publisher       string    `json:"publisher,omitempty"`
	ContainerName   string    `json:"container_name"`

	// ext ids
	DOI     string `json:"doi,omitempty"`
	PMID    string `json:"pmid,omitempty"`
	PMCID   string `json:"pmcid,omitempty"`
	ISBN13  string `json:"isbn13,omitempty"`
	ArxivID string `json:"arxiv_id,omitempty"`
	JstorID string `json:"jstor_id,omitempty"`
	DoajID  string `json:"doaj_id,omitempty"`
	DblpID  string `json:"dblp_id,omitempty"`
	OAIID   string `json:"oai_id,omitempty"`

	DOIPrefix    string `json:"doi_prefix,omitempty"`
	DOIRegistrar string `json:"doi_registrar,omitempty"`

	Tags []string `json:"tags"`

	// NB in old system this is marked with a TODO as never set but there are
	// clearly documents in ES with this set
	IsOA         bool `json:"is_oa"`
	IsLongtailOA bool `json:"is_longtail_oa"`
	IsPreserved  bool `json:"is_preserved"`
	InWeb        bool `json:"in_web"`
	InIA         bool `json:"in_ia"`
	InIaSim      bool `json:"in_ia_sim"`
	InKBART      bool `json:"in_kbart"`
	InJSTOR      bool `json:"in_jstor"`
	InDOAJ       bool `json:"in_doaj"`

	ReleaseYear int    `json:"release_year"`
	ReleaseDate string `json:"release_date,omitempty"`
	AnyAbstract bool   `json:"any_abstract"`

	RefReleaseLegacyIdents []string `json:"ref_release_ids"`
	RefCount               int      `json:"ref_count"`
	RefLinkedCount         int      `json:"ref_linked_count"`

	ContribCount int      `json:"contrib_count"`
	ContribNames []string `json:"contrib_names"`

	CreatorLegacyIdents []string `json:"creator_ids"`
	Affiliations        []string `json:"affiliations"`

	FileCount    int    `json:"file_count"`
	BestPdfUrl   string `json:"best_pdf_url,omitempty"`
	IaPdfUrl     string `json:"ia_pdf_url,omitempty"`
	FirstPage    string `json:"first_page,omitempty"`
	Preservation string `json:"preservation"`

	// NB elided -- either unused ever, unused moving forward, or simply unwanted
	// is_work_alias
	// fileset_count
	// webcapture_count
	// affiliation_rors
	// revision
	// core_id
	// ark_id
	// mag_id
	// hdl
	// wikidata_qid
	// in_dweb
	// in_shadows
}

type FatcatContainerDocV1 struct {
	LegacyIdent       string    `json:"ident"`
	IndexTime         time.Time `json:"doc_index_ts"`
	State             string    `json:"state"`
	Name              string    `json:"name"`
	Publisher         string    `json:"publisher"`
	Type              string    `json:"container_type"`
	PublicationStatus string    `json:"publication_status"`
	Issnl             string    `json:"issnl,omitempty"`
	Issne             string    `json:"issne,omitempty"`
	Issnp             string    `json:"issnp,omitempty"`
	Languages         []string  `json:"languages"`
	Issns             []string  `json:"issns"`
	SimPubID          string    `json:"sim_pubid,omitempty"`
	IaSimCollection   string    `json:"ia_sim_collection,omitempty"`
	IsOA              bool      `json:"is_oa"`
	IsLongtailOA      bool      `json:"is_longtail_oa"`

	// TODO

	// TODO post-xref-poc
	// keepers
	// in_doaj
	// in_road
	// any_kbart
	// any_jstor
	// any_ia_sim
	// preservation_none
	// releases_total
	// preservation_bright
	// preservation_dark
	// preservation_shadows_only

	// NB elided
	// revision
	// wikidata_qid
	// sherpa_romeo_color
	// is_superceded
}

type FatcatFileDocV1 struct {
	LegacyIdent         string    `json:"ident"`
	IndexTime           time.Time `json:"doc_index_ts"`
	State               string    `json:"state"`
	ReleaseLegacyIdents []string  `json:"release_ids"`
	Mimetype            string    `json:"mimetype"`
	Size                int       `json:"size_bytes"`
	Sha1                string    `json:"sha1"`
	Sha256              string    `json:"sha256"`
	Md5                 string    `json:"md5"`
	Hosts               []string  `json:"hosts"`
	Domains             []string  `json:"domains"`
	Rels                []string  `json:"rels"`
	InIA                bool      `json:"in_ia"`
	InIaPetabox         bool      `json:"in_ia_petabox"`
	BestURL             bool      `json:"best_url"`
	ReleaseCount        int       `json:"release_count"`
}
