package fatcat

import "encoding/json"

type Contrib struct {
	CreatorId string `json:"creator_id,omitempty"`
	Extra     struct {
		Seq             string   `json:"seq,omitempty"`
		MoreAffiliation []string `json:"more_affiliation,omitempty"`
	} `json:"extra,omitempty"`
	GivenName      string `json:"given_name,omitempty"`
	Index          int64  `json:"index,omitempty"`
	RawName        string `json:"raw_name,omitempty"`
	Role           string `json:"role,omitempty"`
	Surname        string `json:"surname,omitempty"`
	RawAffiliation string `json:"raw_affiliation,omitempty"`
}

type Ref struct {
	ContainerName string          `json:"container_name,omitempty"`
	Extra         json.RawMessage `json:"extra,omitempty"`
	Index         int64           `json:"index,omitempty"`
	Key           string          `json:"key,omitempty"`
	Locator       string          `json:"locator,omitempty"`
	Title         string          `json:"title,omitempty"`
	Year          int64           `json:"year,omitempty"`
}

type ExtID struct {
	Ark                string `json:"ark,omitempty"`
	Arxiv              string `json:"arxiv,omitempty"`
	Core               string `json:"core,omitempty"`
	DBLP               string `json:"dblp,omitempty"`
	DOAJ               string `json:"doaj,omitempty"`
	DOI                string `json:"doi,omitempty"`
	FatcatReleaseIdent string `json:"release_ident,omitempty"`
	FatcatWorkIdent    string `json:"work_ident,omitempty"`
	HDL                string `json:"hdl,omitempty"`
	ISBN13             string `json:"isbn13,omitempty"`
	JStor              string `json:"jstor,omitempty"`
	MAG                string `json:"mag,omitempty"`
	MID                string `json:"mid,omitempty"`
	OAI                string `json:"oai,omitempty"`
	OpenAlex           string `json:"openalex,omitempty"`
	PII                string `json:"pii,omitempty"`
	PMCID              string `json:"pmcid,omitempty"`
	PMID               string `json:"pmid,omitempty"`
	WikidataQID        string `json:"wikidata_qid,omitempty"`
}

type Abstract struct {
	Content  string `json:"content,omitempty"`
	Mimetype string `json:"mimetype,omitempty"`
	SHA1     string `json:"sha1,omitempty"`
}

// Release, with expanded entities, from v0 API.
type Release struct {
	Source      string     `json:"source"` // source name; uniq; newly added
	Abstracts   []Abstract `json:"abstracts,omitempty"`
	ContainerId string     `json:"container_id,omitempty"`
	Contribs    []Contrib  `json:"contribs,omitempty"`
	ExtIDs      ExtID      `json:"ext_ids,omitempty"`
	Extra       struct {
		Arxiv struct {
			Subjects []string `json:"subjects"`
		} `json:"arxiv"`
		DBLP struct {
			EE []struct {
				Type string `json:"type,omitempty"`
				Text string `json:"text,omitempty"`
			} `json:"ee,omitempty"`
		} `json:"dblp,omitempty"`
		OpenAlex struct {
			PdfUrls        []string `json:"pdf_urls,omitempty"`
			LocationsCount int64    `json:"locations_count,omitempty"`
			HasFulltext    bool     `json:"has_fulltext,omitempty"`
			OpenAccess     struct {
				AnyRepositoryHasFulltext bool        `json:"any_repository_has_fulltext,omitempty"`
				IsOa                     bool        `json:"is_oa,omitempty"`
				OaStatus                 string      `json:"oa_status,omitempty"`
				OaUrl                    interface{} `json:"oa_url,omitempty"`
			} `json:"open_access,omitempty"`
		} `json:"openalex,omitempty"`
		Crossref struct {
			AlternativeId []string `json:"alternative-id,omitempty"`
			Funder        []struct {
				Award         []string `json:"award,omitempty"`
				DOI           string
				DOIAssertedBy string `json:"doi-asserted-by,omitempty"`
				Name          string `json:"name,omitempty"`
			} `json:"funder,omitempty"`
			License []struct {
				ContentVersion string `json:"content-version,omitempty"`
				DelayInDays    int64  `json:"delay-in-days,omitempty"`
				Start          string `json:"start,omitempty"`
				URL            string
			} `json:"license,omitempty"`
			Subject []string `json:"subject,omitempty"`
			Type    string   `json:"type,omitempty"`
		} `json:"crossref,omitempty"`
		OAI struct {
			SetSpec []string `json:"set_spec,omitempty"` // setSpec
			URL     []string `json:"url,omitempty"`
		} `json:"oai,omitempty"`
	} `json:"extra,omitempty"`
	Ident           string `json:"ident,omitempty"` // release ident
	ID              string `json:"id,omitempty"`    // new-style identifier
	Issue           string `json:"issue,omitempty"`
	Language        string `json:"language,omitempty"`
	LicenseSlug     string `json:"license_slug,omitempty"`
	Number          string `json:"number,omitempty"`
	OriginalTitle   string `json:"original_title,omitempty"`
	Pages           string `json:"pages,omitempty"`
	Publisher       string `json:"publisher,omitempty"`
	Refs            []Ref  `json:"refs,omitempty"`
	ReleaseDate     string `json:"release_date,omitempty"`
	ReleaseStage    string `json:"release_stage,omitempty"`
	ReleaseType     string `json:"release_type,omitempty"`
	ReleaseYear     int64  `json:"release_year,omitempty"`
	Revision        string `json:"revision,omitempty"`
	State           string `json:"state,omitempty"`
	Subtitle        string `json:"subtitle,omitempty"`
	Title           string `json:"title,omitempty"`
	Version         string `json:"version,omitempty"`
	Volume          string `json:"volume,omitempty"`
	WithdrawnStatus string `json:"withdrawn_status,omitempty"`
	WithdrawnDate   string `json:"withdrawn_date,omitempty"`
	WithdrawnYear   string `json:"withdrawn_year,omitempty"`
	WorkID          string `json:"work_id,omitempty"` // work ident
}
