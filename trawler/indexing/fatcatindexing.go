package indexing

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
- [ ] port the transform code over for each entity
- [ ] decide when to do the indexing...right after adding? yes, should probably do that
- [ ] how different is fatcat release index payload from scholar? probably a lot :(
- [X] get mapping/sample for release
- [ ] get mapping/sample for container
- [ ] get mapping/sample for file
- [X] when is fatcat_ref populated? it isn't mentioned in workers doc
  - only via refcat stuff so ignoring for now

*/

/*
Doc ID is ident
*/
type FatcatReleaseDocV1 struct {
	LegacyIdent     string `json:"ident,omitempty"`
	State           string `json:"state,omitempty"`
	LegacyWorkIdent string `json:"work_id,omitempty"`
	Title           string `json:"title,omitempty"`
	Subtitle        string `json:"subtitle,omitempty"`
	OriginalTitle   string `json:"original_title,omitempty"`
	ReleaseType     string `json:"release_type,omitempty"`
	ReleaseStage    string `json:"release_stage,omitempty"`
	WithdrawnStatus string `json:"withdrawn_status,omitempty"`
	Language        string `json:"language,omitempty"`
	Volume          string `json:"volume,omitempty"`
	Issue           string `json:"issue,omitempty"`
	Pages           string `json:"pages,omitempty"`
	Number          string `json:"number,omitempty"`
	License         string `json:"license,omitempty"`
	Version         string `json:"version,omitempty"`
	Publisher       string `json:"publisher,omitempty"`
	ContainerName   string `json:"container_name"`

	// ext ids
	DOI         string `json:"doi,omitempty"`
	PMID        string `json:"pmid,omitempty"`
	PMCID       string `json:"pmcid,omitempty"`
	ISBN13      string `json:"isbn13,omitempty"`
	WikidataQID string `json:"wikidata_qid,omitempty"`
	ArxivID     string `json:"arxiv_id,omitempty"`
	JstorID     string `json:"jstor_id,omitempty"`
	DOAJID      string `json:"doaj_id,omitempty"`
	DBLPID      string `json:"dblp_id,omitempty"`
	OAIID       string `json:"oai_id,omitempty"`

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
	InIASim      bool `json:"in_ia_sim"`
	InKbart      bool `json:"in_kbart"`
	InJstor      bool `json:"in_jstor"`
	InDoaj       bool `json:"in_doaj"`

	ReleaseYear int  `json:"release_year"`
	AnyAbstract bool `json:"any_abstract"`

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
	// in_dweb
	// in_shadows
}

/*
"state": "active",
"revision": "4946836e-261b-41de-a8d2-50260f700ec6",
"name": "Journal of information science",
"publisher": "ELSEVIER LTD",
"container_type": null,
"publication_status": null,
"issnl": "1352-7460",
"issne": null,
"issnp": null,
"wikidata_qid": null,
"languages": [
  "en"
],
"issns": [
  "1352-7460"
],
"sherpa_romeo_color": null,
"sim_pubid": null,
"ia_sim_collection": null,
"is_superceded": false,
"keepers": [],
"in_doaj": false,
"in_road": false,
"any_kbart": false,
"is_oa": false,
"is_longtail_oa": false,
"any_jstor": false,
"any_ia_sim": true,
"preservation_none": 0,
"releases_total": 0,
"preservation_bright": 0,
"preservation_dark": 0,
"preservation_shadows_only": 0
*/

type FatcatContainerDocV1 struct {
	LegacyIdent string `json:"ident"`
	// TODO
}

/*
"state": "active",
"revision": "1c4bca76-f027-4f6c-a148-255a06b6f6d7",
"release_ids": [

	"ohwxjbtojbg5rexxlvezutq4jq"

],
"release_count": 1,
"mimetype": "application/pdf",
"size_bytes": 6956115,
"sha1": "76d72d3d79cb7876d1fca0ff160a92fdd16b7da4",
"sha256": "4dd54e03678e906301b368498cade11e5452a9ec28d81c0951a3c60b37969440",
"md5": "f9fb72982ab79f9ee5c09456baff0c7a",
"hosts": [

	"web.archive.org",
	"core.ac.uk"

],
"domains": [

	"archive.org",
	"core.ac.uk"

],
"rels": [

	"aggregator",
	"webarchive"

],
"in_ia": true,
"in_ia_petabox": false,
"best_url": "https://web.archive.org/web/20190321155050/https://core.ac.uk/download/pdf/82346661.pdf"
*/
type FatcatFileDocV1 struct {
	LegacyIdent string `json:"ident"`
}
