package datacite

type Document struct {
	Attributes struct {
		CitationCount int64 `json:"citationCount"`
		Container     struct {
		} `json:"container"`
		ContentUrl   []string `json:"contentUrl"` // https://support.datacite.org/docs/api-get-doi
		Contributors []struct {
			Affiliation []struct {
				AffiliationIdentifier       string `json:"affiliationIdentifier"`
				AffiliationIdentifierScheme string `json:"affiliationIdentifierScheme"`
				Name                        string `json:"name"`
				SchemeUri                   string `json:"schemeUri"`
			} `json:"affiliation"`
			ContributorType string `json:"contributorType"`
			FamilyName      string `json:"familyName"`
			GivenName       string `json:"givenName"`
			Name            string `json:"name"`
			NameIdentifiers []struct {
				NameIdentifier       string `json:"nameIdentifier"`
				NameIdentifierScheme string `json:"nameIdentifierScheme"`
				SchemeUri            string `json:"schemeUri"`
			} `json:"nameIdentifiers"`

			NameType string `json:"nameType"`
		} `json:"contributors"`
		Created  string `json:"created"`
		Creators []struct {
			Affiliation []struct {
				AffiliationIdentifier       string `json:"affiliationIdentifier"`
				AffiliationIdentifierScheme string `json:"affiliationIdentifierScheme"`
				Name                        string `json:"name"`
				SchemeUri                   string `json:"schemeUri"`
			} `json:"affiliation"`

			FamilyName      string `json:"familyName"`
			GivenName       string `json:"givenName"`
			Name            string `json:"name"`
			NameIdentifiers []struct {
				NameIdentifier       string `json:"nameIdentifier"`
				NameIdentifierScheme string `json:"nameIdentifierScheme"`
				SchemeUri            string `json:"schemeUri"`
			} `json:"nameIdentifiers"`
			NameType string `json:"nameType"`
		} `json:"creators"`
		// Dates []struct {
		// 	Date     string `json:"date"`
		// 	DateType string `json:"dateType"`
		// } `json:"dates"`
		// Descriptions []struct {
		// 	Description     string `json:"description"`
		// 	DescriptionType string `json:"descriptionType"`
		// 	Lang            string `json:"lang"`
		// } `json:"descriptions"`
		DOI               string   `json:"doi"`
		DownloadCount     int64    `json:"downloadCount"`
		Formats           []string `json:"formats"`
		FundingReferences []struct {
			AwardNumber          string `json:"awardNumber"`
			AwardTitle           string `json:"awardTitle"`
			FunderIdentifier     string `json:"funderIdentifier"`
			FunderIdentifierType string `json:"funderIdentifierType"`
			FunderName           string `json:"funderName"`
		} `json:"fundingReferences"`
		GeoLocations []struct {
			GeoLocationPlace string `json:"geoLocationPlace"`
		} `json:"geoLocations"`
		Identifiers []struct {
			Identifier     string `json:"identifier"`
			IdentifierType string `json:"identifierType"`
		} `json:"identifiers"`
		IsActive           bool        `json:"isActive"`
		Language           string      `json:"language"`
		MetadataVersion    int64       `json:"metadataVersion"`
		PartCount          int64       `json:"partCount"`
		PartOfCount        int64       `json:"partOfCount"`
		PublicationYear    int64       `json:"publicationYear"`
		Published          string      `json:"published"` // https://support.datacite.org/docs/api-get-doi
		Publisher          string      `json:"publisher"`
		Reason             interface{} `json:"reason"` // Legacy attribute for EZID compatibility.
		ReferenceCount     int64       `json:"referenceCount"`
		Registered         string      `json:"registered"`
		RelatedIdentifiers []struct {
			RelatedIdentifier     string `json:"relatedIdentifier"`
			RelatedIdentifierType string `json:"relatedIdentifierType"`
			RelationType          string `json:"relationType"`
		} `json:"relatedIdentifiers"`
		RelatedItems []struct {
			RelatedItemIdentifier struct {
				RelatedItemIdentifier     string `json:"relatedItemIdentifier"`
				RelatedItemIdentifierType string `json:"relatedItemIdentifierType"`
			} `json:"relatedItemIdentifier"`
			RelatedItemType string `json:"relatedItemType"`
			RelationType    string `json:"relationType"`
			Titles          []struct {
				Title string `json:"title"`
			} `json:"titles"`
		} `json:"relatedItems"`
		RightsList []struct {
			Rights                 string `json:"rights"`
			RightsIdentifier       string `json:"rightsIdentifier"`
			RightsIdentifierScheme string `json:"rightsIdentifierScheme"`
			RightsUri              string `json:"rightsUri"`
			SchemeUri              string `json:"schemeUri"`
		} `json:"rightsList"`
		SchemaVersion string   `json:"schemaVersion"`
		Sizes         []string `json:"sizes"`
		Source        string   `json:"source"`
		State         string   `json:"state"`
		Subjects      []struct {
			Subject string `json:"subject"`
		} `json:"subjects"`
		Titles []struct {
			Lang      string `json:"lang"`
			Title     string `json:"title"`
			TitleType string `json:"titleType"`
		} `json:"titles"`
		Types struct {
			Bibtex              string `json:"bibtex"`
			Citeproc            string `json:"citeproc"`
			ResourceTypeGeneral string `json:"resourceTypeGeneral"`
			Ris                 string `json:"ris"`
			SchemaOrg           string `json:"schemaOrg"`
		} `json:"types"`
		Updated        string `json:"updated"`
		URL            string `json:"url"`
		Version        string `json:"version"`
		VersionCount   int64  `json:"versionCount"`
		VersionOfCount int64  `json:"versionOfCount"`
		ViewCount      int64  `json:"viewCount"`
	} `json:"attributes"`
	ID            string `json:"id"`
	Relationships struct {
		Client struct {
			Data struct {
				Id   string `json:"id"`
				Type string `json:"type"`
			} `json:"data"`
		} `json:"client"`
	} `json:"relationships"`
	Type string `json:"type"`
}
