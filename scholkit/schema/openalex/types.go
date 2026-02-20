package openalex

import "fmt"

// Work entity in OpenAlex. TODO: clarify interface fields.
type Work struct {
	AbstractInvertedIndex interface{} `json:"abstract_inverted_index"`
	ApcList               interface{} `json:"apc_list"`
	ApcPaid               interface{} `json:"apc_paid"`
	AuthorsCount          int64       `json:"authors_count"`
	Authorships           []struct {
		Author struct {
			DisplayName string      `json:"display_name"`
			Id          string      `json:"id"`
			Orcid       interface{} `json:"orcid"`
		} `json:"author"`
		AuthorPosition        string        `json:"author_position"`
		Countries             []interface{} `json:"countries"`
		Institutions          []interface{} `json:"institutions"`
		IsCorresponding       bool          `json:"is_corresponding"`
		RawAffiliationString  string        `json:"raw_affiliation_string"`
		RawAffiliationStrings []interface{} `json:"raw_affiliation_strings"`
		RawAuthorName         string        `json:"raw_author_name"`
	} `json:"authorships"`
	BestOaLocation interface{} `json:"best_oa_location"`
	Biblio         struct {
		FirstPage string `json:"first_page"`
		Issue     string `json:"issue"`
		LastPage  string `json:"last_page"`
		Volume    string `json:"volume"`
	} `json:"biblio"`
	CitedByApiUrl         string `json:"cited_by_api_url"`
	CitedByCount          int64  `json:"cited_by_count"`
	CitedByPercentileYear struct {
		Max interface{} `json:"max"`
		Min interface{} `json:"min"`
	} `json:"cited_by_percentile_year"`
	Concepts []struct {
		DisplayName string  `json:"display_name"`
		Id          string  `json:"id"`
		Level       int64   `json:"level"`
		Score       float64 `json:"score"`
		Wikidata    string  `json:"wikidata"`
	} `json:"concepts"`
	ConceptsCount               int64         `json:"concepts_count"`
	CorrespondingAuthorIds      []string      `json:"corresponding_author_ids"`
	CorrespondingInstitutionIds []interface{} `json:"corresponding_institution_ids"`
	CountriesDistinctCount      int64         `json:"countries_distinct_count"`
	CountsByYear                []interface{} `json:"counts_by_year"`
	CreatedDate                 string        `json:"created_date"`
	DisplayName                 string        `json:"display_name"`
	DOI                         string        `json:"doi"`
	DOIRegistrationAgency       interface{}   `json:"doi_registration_agency"`
	Grants                      []interface{} `json:"grants"`
	HasFulltext                 bool          `json:"has_fulltext"`
	ID                          string        `json:"id"`
	//
	// 	{
	// 		"openalex": "https://openalex.org/W1976788090",
	// 		"doi": "https://doi.org/10.1002/jlac.19707420104",
	// 		"pmid": "https://pubmed.ncbi.nlm.nih.gov/5500151",
	// 		"mag": 1976788090
	//      "arxiv_id": "arXiv:1910.03251"
	// 	}
	//
	IDs struct {
		Mag      int64  `json:"mag"`
		Openalex string `json:"openalex"`
		DOI      string `json:"doi"`
		PMID     string `json:"pmid"`
		PMCID    string `json:"pmcid"`
		Arxiv    string `json:"arxiv_id"`
	} `json:"ids"`
	IndexedIn                 []interface{} `json:"indexed_in"`
	InstitutionsDistinctCount int64         `json:"institutions_distinct_count"`
	IsParatext                bool          `json:"is_paratext"`
	IsRetracted               bool          `json:"is_retracted"`
	Keywords                  []interface{} `json:"keywords"`
	Language                  string        `json:"language"`
	Locations                 []struct {
		DOI            interface{} `json:"doi"`
		IsAccepted     bool        `json:"is_accepted"`
		IsOa           bool        `json:"is_oa"`
		IsPublished    bool        `json:"is_published"`
		LandingPageUrl string      `json:"landing_page_url"`
		License        string      `json:"license"`
		PdfUrl         string      `json:"pdf_url"`
		Source         struct {
			DisplayName                  string        `json:"display_name"`
			HostInstitutionLineage       []interface{} `json:"host_institution_lineage"`
			HostInstitutionLineageNames  []interface{} `json:"host_institution_lineage_names"`
			HostOrganization             string        `json:"host_organization"`
			HostOrganizationLineage      []interface{} `json:"host_organization_lineage"`
			HostOrganizationLineageNames []interface{} `json:"host_organization_lineage_names"`
			HostOrganizationName         string        `json:"host_organization_name"`
			Id                           string        `json:"id"`
			IsInDoaj                     bool          `json:"is_in_doaj"`
			IsOa                         bool          `json:"is_oa"`
			Issn                         []string      `json:"issn"`
			IssnL                        string        `json:"issn_l"`
			Publisher                    string        `json:"publisher"`
			PublisherId                  string        `json:"publisher_id"`
			PublisherLineage             []interface{} `json:"publisher_lineage"`
			PublisherLineageNames        []interface{} `json:"publisher_lineage_names"`
			Type                         string        `json:"type"`
		} `json:"source"`
		Version interface{} `json:"version"`
	} `json:"locations"`
	LocationsCount int64         `json:"locations_count"`
	Mesh           []interface{} `json:"mesh"`
	OpenAccess     struct {
		AnyRepositoryHasFulltext bool        `json:"any_repository_has_fulltext"`
		IsOa                     bool        `json:"is_oa"`
		OaStatus                 string      `json:"oa_status"`
		OaUrl                    interface{} `json:"oa_url"`
	} `json:"open_access"`
	PrimaryLocation struct {
		DOI            interface{} `json:"doi"`
		IsAccepted     bool        `json:"is_accepted"`
		IsOa           bool        `json:"is_oa"`
		IsPublished    bool        `json:"is_published"`
		LandingPageUrl string      `json:"landing_page_url"`
		License        string      `json:"license"`
		PdfUrl         string      `json:"pdf_url"`
		Source         struct {
			DisplayName                  string        `json:"display_name"`
			HostInstitutionLineage       []interface{} `json:"host_institution_lineage"`
			HostInstitutionLineageNames  []interface{} `json:"host_institution_lineage_names"`
			HostOrganization             string        `json:"host_organization"`
			HostOrganizationLineage      []interface{} `json:"host_organization_lineage"`
			HostOrganizationLineageNames []interface{} `json:"host_organization_lineage_names"`
			HostOrganizationName         string        `json:"host_organization_name"`
			Id                           string        `json:"id"`
			IsInDoaj                     bool          `json:"is_in_doaj"`
			IsOa                         bool          `json:"is_oa"`
			Issn                         []string      `json:"issn"`
			IssnL                        string        `json:"issn_l"`
			Publisher                    string        `json:"publisher"`
			PublisherId                  string        `json:"publisher_id"`
			PublisherLineage             []interface{} `json:"publisher_lineage"`
			PublisherLineageNames        []interface{} `json:"publisher_lineage_names"`
			Type                         string        `json:"type"`
		} `json:"source"`
		Version interface{} `json:"version"`
	} `json:"primary_location"`
	PublicationDate      string        `json:"publication_date"`
	PublicationYear      int64         `json:"publication_year"`
	ReferencedWorks      []interface{} `json:"referenced_works"`
	ReferencedWorksCount int64         `json:"referenced_works_count"`
	RelatedWorks         []interface{} `json:"related_works"`
	SummaryStats         struct {
		CitedByCount   int64 `json:"cited_by_count"`
		YrCitedByCount int64 `json:"2yr_cited_by_count"`
	} `json:"summary_stats"`
	SustainableDevelopmentGoals []struct {
		DisplayName string  `json:"display_name"`
		Id          string  `json:"id"`
		Score       float64 `json:"score"`
	} `json:"sustainable_development_goals"`
	Title        string `json:"title"`
	Type         string `json:"type"`
	TypeCrossref string `json:"type_crossref"`
	Updated      string `json:"updated"`
	UpdatedDate  string `json:"updated_date"`
}

// Pages returns a page range as string.
func (w *Work) Pages() string {
	f := w.Biblio.FirstPage
	l := w.Biblio.LastPage
	switch {
	case len(f) == 0 && len(l) == 0:
		return ""
	case len(f) > 0 && len(l) == 0:
		return f
	case len(f) == 0 && len(l) > 0:
		return l
	default:
		return fmt.Sprintf("%s-%s", f, l)
	}
}

func (w *Work) PdfUrls() (result []string) {
	for _, loc := range w.Locations {
		if len(loc.PdfUrl) == 0 {
			continue
		}
		result = append(result, loc.PdfUrl)
	}
	return
}
