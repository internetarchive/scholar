package crossref

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"git.archive.org/webgroup/scholar/trawler/issn"
	"git.archive.org/webgroup/scholar/trawler/spn/spnclient"
	"github.com/google/uuid"
	"github.com/spf13/viper"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/workflow"
)

// scrape crossref for a day's worth of metadata: huge ndjson file in s3, each line a paper-like entity
// for each entity, create an entry in fatcat2
// for each entity, if there's a suitable URL, try and obtain a PDF
// for each obtained PDF
// - extract metadata and make sure it matches the fatcat2 record
// - create a file entry in fatcat2
// - extract fulltext and ingest into elasticsearch

// containerTypeMap maps from fatcat release types to their assumed parent container type
var containerTypeMap = map[string]string{
	"article-journal":  "journal",
	"paper-conference": "conference",
	"book":             "book-series",
}

var releaseTypeMap = map[string]string{
	// CSL types
	"book":                "book",
	"book-chapter":        "chapter",
	"book-part":           "chapter",
	"book-section":        "chapter",
	"dataset":             "dataset",
	"dissertation":        "thesis",
	"edited-book":         "book",
	"journal-article":     "article-journal",
	"monograph":           "book",
	"posted-content":      "post",
	"proceedings-article": "paper-conference",
	"report":              "report",

	// considering switching to ignored types pending Jefferson convo/research

	// looking at releases with no types and "crossref" in the extra_json in
	// fatcat1, this seems to often describe figures
	// TODO waiting on jefferson's call
	"journal-issue":   "",
	"journal-volume":  "",
	"other":           "",
	"reference-book":  "book",
	"reference-entry": "entry",
	"standard":        "standard",

	// non-CSL types
	"component": "component",
}

type Abstract struct {
	ReleaseID *uuid.UUID `json:"release_id"`
	Content   string     `json:"content"`
	SHA1      string     `json:"sha1"`
	Language  string     `json:"language"`
	MIMEType  string     `json:"mimetype"`
}

type ReleaseContrib struct {
	CreatorID      *uuid.UUID     `json:"creator_id"`
	ReleaseID      *uuid.UUID     `json:"release_id"`
	Position       int            `json:"position"`
	RawName        string         `json:"raw_name"`
	GivenName      string         `json:"given_name"`
	Surname        string         `json:"surname"`
	RawAffiliation string         `json:"raw_affiliation"`
	Role           string         `json:"role"`
	Extra          map[string]any `json:"extra"`
}

type ExternalID struct {
	ReleaseID *uuid.UUID `json:"release_id"`
	Type      string     `json:"id_type"`
	Value     string     `json:"id_value"`
}
type Container struct {
	ID          uuid.UUID      `json:"id,omitempty"`
	Name        string         `json:"name,omitempty"`
	Type        string         `json:"container_type,omitempty"`
	LegacyRevID uuid.UUID      `json:"legacy_rev_id,omitempty"`
	Publisher   string         `json:"publisher,omitempty"`
	ISSNL       string         `json:"issnl,omitempty"`
	ISSNE       string         `json:"issne,omitempty"`
	ISSNP       string         `json:"issnp,omitempty"`
	Source      string         `json:"source,omitempty"`
	Extra       map[string]any `json:"extra"`
}

// TODO split out into its own package
type Release struct {
	ID            uuid.UUID      `json:"id"`
	WorkID        *uuid.UUID     `json:"work_id"`
	Title         string         `json:"title,omitempty"`
	OriginalTitle string         `json:"original_title,omitempty"`
	Subtitle      string         `json:"subtitle,omitempty"`
	Type          string         `json:"release_type,omitempty"`
	Stage         string         `json:"release_stage,omitempty"`
	ReleaseDate   *time.Time     `json:"release_date"`
	ReleaseYear   int            `json:"release_year,omitempty"`
	Source        string         `json:"source,omitempty"`
	Volume        string         `json:"volume,omitempty"`
	Issue         string         `json:"issue,omitempty"`
	Pages         string         `json:"pages,omitempty"`
	Publisher     string         `json:"publisher,omitempty"`
	Language      string         `json:"language,omitempty"`
	LegacyRevID   uuid.UUID      `json:"legacy_rev_id,omitempty"`
	LicenseSlug   string         `json:"license_slug,omitempty"`
	Extra         map[string]any `json:"extra,omitempty"`

	// Foreign keys

	Refs        []RawRef         `json:"refs,omitempty"`
	Abstracts   []Abstract       `json:"abstracts,omitempty"`
	ContainerID *uuid.UUID       `json:"container_id"`
	ExternalIDs []ExternalID     `json:"extids,omitempty"`
	Contribs    []ReleaseContrib `json:"contribs,omitempty"`

	// unused in xref but may want later:
	// Pages string
	// WithdrawnStatus string
	// TODO
	// understand when the structured ReleaseRefs are added in the old system
}

func (r Release) DOI() string {
	for _, eid := range r.ExternalIDs {
		if eid.Type == "doi" {
			return eid.Value
		}
	}
	return ""
}

func (r Release) IsPaperlike() bool {
	paperLikeTypes := []string{
		"article-journal",
		"book",
		"paper-conference",
		"chapter",
		"report",
		"thesis",
	}

	return slices.Contains(paperLikeTypes, r.Type)
}

// FulltextURLs returns a list of possible locations for this release's
// fulltext PDF. These are generated from known URL patterns for the different
// external ID types. Should upstream patterns change, the URLs generated here
// might become useless. The URLs are sorted roughly by preference (IE,
// likelihood of success).
func (r Release) FulltextURLs() []string {
	// TODO this approach smells to me; I'm mostly just preserving what we were
	// doing in fatcat's entity worker. This is important code (drives our daily
	// crawling attempts) _and_ is volatile as it depends on upstream url
	// patterns. I'm keeping it like this for the first pass but having URL
	// templates in config per upstream might be useful to expose.
	/*
	   Relevant fatcat code (python/fatcat_tools/transforms/ingest.py):
	   # generate a URL where we expect to find fulltext
	   url = None
	   link_source = None
	   link_source_id = None
	   if release.ext_ids.arxiv and ingest_type == "pdf":
	       url = "https://arxiv.org/pdf/{}.pdf".format(release.ext_ids.arxiv)
	       link_source = "arxiv"
	       link_source_id = release.ext_ids.arxiv
	   elif release.ext_ids.pmcid and ingest_type == "pdf":
	       # TODO: how to tell if an author manuscript in PMC vs. published?
	       # url = "https://www.ncbi.nlm.nih.gov/pmc/articles/{}/pdf/".format(release.ext_ids.pmcid)
	       url = "http://europepmc.org/backend/ptpmcrender.fcgi?accid={}&blobtype=pdf".format(
	           release.ext_ids.pmcid
	       )
	       link_source = "pmc"
	       link_source_id = release.ext_ids.pmcid
	   elif release.ext_ids.doi:
	       url = "https://doi.org/{}".format(release.ext_ids.doi.lower())
	       link_source = "doi"
	       link_source_id = release.ext_ids.doi.lower()
	   elif release.ext_ids.doaj:
	       url = "https://doaj.org/article/{}".format(release.ext_ids.doaj.lower())
	       link_source = "doaj"
	       link_source_id = release.ext_ids.doaj.lower()
	   elif release.ext_ids.hdl:
	       url = "https://hdl.handle.net/{}".format(release.ext_ids.hdl.lower())
	       link_source = "hdl"
	       link_source_id = release.ext_ids.hdl.lower()
	*/

	out := []string{}
	if r.DOI() != "" {
		out = append(out, fmt.Sprintf("https://doi.org/%s", r.DOI()))
	}

	// TODO arxiv (NB cross reference with the hack i added to sandcrawler)
	// TODO pmcid (NB it used ncbi but switched to europepmc which mostly fails, now
	// TODO doaj
	// TODO hdl
	return out
}

// RawRef is stored in fatcat2's database as a json value in a release row
type RawRef struct {
	// TODO I don't like how this is structured (wayyy too much shoved in extra)
	// but just maintaining parity for now with legacy fatcat

	// NB no indication TargetReleaseID is ever set
	// TODO this is ending up as uuid.Nil
	//TargetReleaseID *uuid.UUID     `json:"target_release_id,omitempty"`
	Title         string         `json:"title,omitempty"`
	Index         int            `json:"index,omitempty"`
	Key           string         `json:"key,omitempty"`
	Year          int            `json:"year,omitempty"`
	ContainerName string         `json:"container_name,omitempty"`
	Locator       string         `json:"locator,omitempty"`
	Extra         map[string]any `json:"extra,omitempty"`
}

// TODO crossref structs
type crossrefRef struct {
	Year            string
	Key             string
	JournalTitle    string `json:"journal-title"`
	VolumeTitle     string `json:"volume-title"`
	DOI             string
	Author          string
	Editor          string
	Edition         string
	Authority       string
	Version         string
	Genre           string
	URL             string `json:"url"`
	Event           string
	ArticleTitle    string `json:"article-title"`
	FirstPage       string `json:"first-page"`
	Issue           string
	Volume          string
	Date            string
	AccessedDate    string `json:"accessed_date"`
	Issued          string
	Page            string
	Medium          string
	CollectionTitle string `json:"collection_title"`
	ChapterNumber   string `json:"chapter_number"`
	Unstructured    string
	SeriesTitle     string `json:"series-title"`
}
type crossrefLicense struct {
	URL            string
	ContentVersion string `json:"content-version"`
	Start          struct {
		DateTime string `json:"date-time"`
	} `json:"start"`
	DelayInDays int `json:"delay-in-days"`
}

type crossrefContributor struct {
	ORCID       string
	Name        string
	Family      string
	Given       string
	Sequence    string
	Affiliation []struct {
		Name string
	}
}

func (cc crossrefContributor) ToReleaseContrib(client *http.Client) (ReleaseContrib, error) {
	out := ReleaseContrib{
		GivenName: cc.Given,
		Surname:   cc.Family,
	}
	if cc.ORCID != "" {
		sp := strings.Split(cc.ORCID, "/")
		orcid := sp[len(sp)-1]
		id, err := lookupCreator(client, orcid)
		if err != nil {
			return out, err
		}
		if id != uuid.Nil {
			out.CreatorID = &id
		}
	}

	out.RawName = cc.Given
	if cc.Family != "" && cc.Given != "" {
		out.RawName = fmt.Sprintf("%s %s", cc.Given, cc.Family)
	} else if cc.Family != "" {
		out.RawName = cc.Family
	} else if cc.Name != "" {
		out.RawName = cc.Name
	}

	for _, a := range cc.Affiliation {
		if a.Name != "" {
			out.RawAffiliation = a.Name
			break
		}
	}

	// sigh
	extra := map[string]any{}

	if len(cc.Affiliation) > 1 {
		extra["more_affiliations"] = cc.Affiliation[1:]
	}

	if cc.Sequence != "" && cc.Sequence != "additional" {
		extra["seq"] = cc.Sequence
	}

	return out, nil
}

type crossrefDoc struct {
	ContainerTitle []string `json:"container-title"`
	DOI            string
	ISSN           []string
	ISBN           []string
	License        []crossrefLicense
	Reference      []crossrefRef
	Publisher      string
	OriginalTitle  []string
	Title          []string
	Subtitle       []string
	Type           string
	Abstract       string
	Language       string
	AlternativeID  []string
	Archive        []string
	Volume         string
	Issue          string
	Page           string
	Author         []crossrefContributor
	Editor         []crossrefContributor
	Translator     []crossrefContributor
	Issued         struct {
		DateParts [][]int `json:"date-parts"`
	}
	Funder []struct {
		Name  string
		Award []string
	}
	// in fatcat but didn't see in sample xref json
	// Subject     string
}

var ignoredTypes = []string{
	"",
	"database",
	"journal",
	"proceedings",
	"standard-series",
	"report-series",
	"book-series",
	"book-set",
	"book-track",
	"proceedings-series",
	"peer-review",
}

func (c crossrefDoc) IsSkippable() bool {
	if len(c.Title) == 0 {
		return true
	}

	if len(c.Title[0]) < 2 {
		return true
	}

	if strings.ToLower(c.Title[0]) == "oup accepted manuscript" {
		return true
	}

	if slices.Contains(ignoredTypes, c.Type) {
		return true
	}

	if c.DOI == "" {
		return true
	}

	if len(c.Abstract) > 100 {
		return true
	}

	if len(c.Reference) > 5000 {
		return true
	}

	if len(c.Author)+len(c.Editor)+len(c.Translator) > 2000 {
		return true
	}

	return false
}

type CrossrefCrawlInput struct {
	SKInput SKCrossrefInput
}

type releaseCounts struct {
	// Skipped is the count of lines in the upstream we knew we would never want
	Skipped int
	// Ignored is the count of lines in the upstream metadata we were already aware of
	Ignored int
	// Added is the count of lines from the upstream metadata we added to Fatcat
	Added int
	// CrawlWanted is the count of how many releases we tried to get from the web
	CrawlWanted int
	// Acquired is the count of PDFs we acquired from the upstream metadata
	Acquired int
}

type containerCounts struct {
	// Ignored is the count of containers fatcat already knew about
	Ignored int
	// Added is the count of containers we created in fatcat
	Added int
	// Skipped is the count of containers we did not create because of data issues
	Skipped int
}

type counts struct {
	Releases   releaseCounts
	Containers containerCounts
}

func (c counts) Add(other counts) counts {
	return counts{
		Releases: releaseCounts{
			Skipped:     c.Releases.Skipped + other.Releases.Skipped,
			Ignored:     c.Releases.Ignored + other.Releases.Ignored,
			Added:       c.Releases.Added + other.Releases.Added,
			CrawlWanted: c.Releases.CrawlWanted + other.Releases.CrawlWanted,
			Acquired:    c.Releases.Acquired + other.Releases.Acquired,
		},
		Containers: containerCounts{
			Ignored: c.Containers.Ignored + other.Containers.Ignored,
			Added:   c.Containers.Added + other.Containers.Added,
		},
	}
}

type lineBatchInput struct {
	// S3Key is a key to a .ndjson file in s3 storage
	S3Key string
	// Offsets is a list of pairs of [ReadOffset, Length]
	Offsets [][]int64
}

func lineBatchWorkflow(ctx workflow.Context, in lineBatchInput) (counts, error) {
	out := counts{}
	ao := workflow.ActivityOptions{
		StartToCloseTimeout: 8 * 60 * 60 * time.Second,                       // TODO tune, config maybe
		TaskQueue:           viper.GetString("crossref.internal_task_queue"), // TODO needed?
	}
	ctx = workflow.WithActivityOptions(ctx, ao)
	lin := lineInput{
		S3Key: in.S3Key,
	}
	for _, offset := range in.Offsets {
		lin.LineStart = offset[0]
		lin.Length = offset[1]

		var c counts

		err := workflow.ExecuteActivity(ctx, processLine, lin).Get(ctx, &c)
		if err != nil {
			return out, err
		}
		out = out.Add(c)
	}
	return out, nil
}

type lineInput struct {
	S3Key     string
	LineStart int64
	Length    int64
}

// TODO split this up into smaller activities
func processLine(ctx context.Context, in lineInput) (out counts, err error) {
	out = counts{}
	f, err := getS3Object(ctx, in.S3Key)
	if err != nil {
		return
	}
	defer f.Close()

	lineb := make([]byte, in.Length)
	n, err := f.ReadAt(lineb, in.LineStart)
	if err != nil {
		return
	}
	if n == 0 {
		return out, fmt.Errorf("read 0 bytes, expected %d", len(lineb))
	}

	l := activity.GetLogger(ctx)

	var xrefdoc crossrefDoc

	err = json.Unmarshal(lineb, &xrefdoc)
	if err != nil {
		return
	}

	l.Info(fmt.Sprintf("got a '%s' with doi '%s'", xrefdoc.Type, xrefdoc.DOI))

	// Should we skip even checking the DOI?

	if xrefdoc.IsSkippable() {
		out.Releases.Skipped++
		return out, nil
	}

	// Check the DOI

	xrefDOI := strings.ToLower(xrefdoc.DOI)

	client := &http.Client{}
	found, err := isExistingDOI(client, xrefDOI)
	if err != nil {
		return out, err
	}

	if found {
		out.Releases.Ignored++
		return out, nil
	}

	release := Release{
		Contribs:    []ReleaseContrib{},
		ExternalIDs: []ExternalID{},
		Extra:       map[string]any{},
		Publisher:   xrefdoc.Publisher,
		Volume:      xrefdoc.Volume,
		Issue:       xrefdoc.Issue,
		Pages:       xrefdoc.Page,
		Language:    xrefdoc.Language,
	}

	// if things get weird we'll put some stuff in here
	var releaseType string
	releaseType, ok := releaseTypeMap[xrefdoc.Type]
	if !ok {
		return out, fmt.Errorf("found unknown crossref type '%s'", xrefdoc.Type)
	}
	release.Type = releaseType

	var containerTitle string

	if len(xrefdoc.ContainerTitle) > 0 {
		// TODO fatcat importer is using ftfy to clean this value up; we can do
		// that on the server side on container creation.
		// TODO fatcat importer was arbitrarily using the first container title in
		// the list so I've continued that practice but it feels weird
		containerTitle = xrefdoc.ContainerTitle[0]
	}

	var issnl string
	for _, i := range xrefdoc.ISSN {
		issnl = issn.ISSN2ISSNL(i)
		if issnl != "" {
			break
		}
	}

	containerID := uuid.Nil

	if issnl != "" {
		// TODO could build a map of issnl->cid somewhere to save on requests
		containerID, err = lookupContainer(client, issnl)
		if err != nil {
			return out, err
		}
	}

	if containerID == uuid.Nil {
		if containerTitle != "" && issnl != "" {
			c := Container{
				Name:      containerTitle,
				ISSNL:     issnl,
				Publisher: xrefdoc.Publisher,
				Type:      containerTypeMap[releaseType],
			}
			containerID, err = createContainer(client, c)
			if err != nil {
				return out, err
			}
			out.Containers.Added++
		} else if containerTitle != "" {
			release.Extra["container_name"] = containerTitle
			out.Containers.Skipped++
		}
	} else {
		out.Containers.Ignored++
	}

	if containerID != uuid.Nil {
		release.ContainerID = &containerID
	}

	// licenses

	for _, lic := range xrefdoc.License {
		// the original fatcat code iterated over every license running code like
		// this; that means it would only ever take the last license in a list of
		// licenses. i've preserved that side effect here.
		if lic.ContentVersion != "vor" && lic.ContentVersion != "unspecified" {
			continue
		}
		release.LicenseSlug = licenseSlugLookup(lic.URL)
	}

	// references
	release.Refs = []RawRef{}

	for i, cref := range xrefdoc.Reference {
		rawRef := RawRef{
			Index:   i,
			Locator: cref.FirstPage,
			Title:   cref.ArticleTitle,
			Extra:   map[string]any{},
		}

		year, err := strconv.Atoi(cref.Year)
		if err == nil {
			rawRef.Year = year
		}

		if cref.Key != "" {
			key := strings.TrimPrefix(cref.Key, strings.ToUpper(xrefdoc.DOI)+"-")
			key = strings.TrimPrefix(cref.Key, strings.ToUpper(xrefdoc.DOI))
			rawRef.Key = key
		}

		rawRef.ContainerName = cref.VolumeTitle
		if rawRef.ContainerName == "" {
			rawRef.ContainerName = cref.JournalTitle
		}

		// "extra" stuff (i hate this)

		if cref.JournalTitle != "" {
			rawRef.Extra["journal-title"] = cref.JournalTitle
		}

		if cref.DOI != "" {
			rawRef.Extra["DOI"] = cref.DOI
		}

		if cref.Author != "" {
			// why is this a list?
			rawRef.Extra["authors"] = []string{cref.Author}
		}

		if cref.Editor != "" {
			rawRef.Extra["editor"] = cref.Editor
		}
		if cref.Edition != "" {
			rawRef.Extra["edition"] = cref.Edition
		}
		if cref.Authority != "" {
			rawRef.Extra["authority"] = cref.Authority
		}
		if cref.Version != "" {
			rawRef.Extra["version"] = cref.Version
		}
		if cref.Genre != "" {
			rawRef.Extra["genre"] = cref.Genre
		}
		if cref.URL != "" {
			rawRef.Extra["url"] = cref.URL
		}
		if cref.Event != "" {
			rawRef.Extra["event"] = cref.Event
		}
		if cref.Issue != "" {
			rawRef.Extra["issue"] = cref.Issue
		}
		if cref.Volume != "" {
			rawRef.Extra["volume"] = cref.Volume
		}
		if cref.Date != "" {
			rawRef.Extra["date"] = cref.Date
		}
		if cref.AccessedDate != "" {
			rawRef.Extra["accessed_date"] = cref.AccessedDate
		}
		if cref.Issue != "" {
			rawRef.Extra["issue"] = cref.Issue
		}
		if cref.Page != "" {
			rawRef.Extra["page"] = cref.Page
		}
		if cref.Medium != "" {
			rawRef.Extra["medium"] = cref.Medium
		}
		if cref.CollectionTitle != "" {
			rawRef.Extra["collection_title"] = cref.CollectionTitle
		}
		if cref.ChapterNumber != "" {
			rawRef.Extra["chapter_number"] = cref.ChapterNumber
		}
		if cref.Unstructured != "" {
			rawRef.Extra["unstructured"] = cref.Unstructured
		}
		if cref.SeriesTitle != "" {
			rawRef.Extra["series-title"] = cref.SeriesTitle
		}
		if cref.VolumeTitle != "" {
			rawRef.Extra["volume-title"] = cref.VolumeTitle
		}

		release.Refs = append(release.Refs, rawRef)
	}

	// abstracts

	// TODO find out if any release has more than one abstract
	release.Abstracts = []Abstract{}
	if len(xrefdoc.Abstract) > 10 {
		h := sha1.Sum([]byte(xrefdoc.Abstract))
		release.Abstracts = append(release.Abstracts, Abstract{
			MIMEType: "application/xml+jats",
			Content:  xrefdoc.Abstract,
			Language: xrefdoc.Language,
			SHA1:     fmt.Sprintf("%x", h),
		})
	}

	// TODO noticed this as the entire content of an abstract, should filter out stuff like this:
	// Dieser Artikel ist nur als PDF-Dokument verfügbar.
	// (This article is only available as a PDF document.)

	// "extra" stuff (ugh)
	if release.ContainerID == nil && len(xrefdoc.ContainerTitle) > 1 {
		release.Extra["container_name"] = xrefdoc.ContainerTitle[0]
	}

	xrefExtra := map[string]any{}
	if len(xrefdoc.AlternativeID) > 0 {
		xrefExtra["alternative-id"] = xrefdoc.AlternativeID
	}
	if xrefdoc.Type != "" {
		xrefExtra["type"] = xrefdoc.Type
	}
	if len(xrefdoc.Title) > 1 {
		xrefExtra["aliases"] = xrefdoc.Title[1:]
	}
	if len(xrefdoc.Archive) > 0 {
		xrefExtra["archive"] = xrefdoc.Archive
	}
	if len(xrefdoc.Funder) > 0 {
		xrefExtra["funder"] = xrefdoc.Funder
	}
	if len(xrefdoc.License) > 0 {
		// in original fatcat this value was added to the license key in crossref
		// extra but with the start key overwritten with its date-time subkey. i've
		// stopped that flattening and just left the start key only containing
		// date-time.
		xrefExtra["license"] = xrefdoc.License
	}
	release.Extra["crossref"] = xrefExtra

	// external IDs

	release.ExternalIDs = append(release.ExternalIDs, ExternalID{
		Type:  "doi",
		Value: xrefDOI,
	})
	if len(xrefdoc.ISBN) > 0 {
		for _, isbn := range xrefdoc.ISBN {
			if len(isbn) == 17 {
				release.ExternalIDs = append(release.ExternalIDs, ExternalID{
					Type:  "isbn13",
					Value: isbn,
				})
				break
			}
		}
	}

	// release status
	if slices.Contains([]string{
		"journal-article", "conference-proceedings", "book",
		"dissertation", "book-chapter"}, xrefdoc.Type) {
		release.Stage = "published"
	}

	// contribs

	for i, a := range xrefdoc.Author {
		contrib, err := a.ToReleaseContrib(client)
		if err != nil {
			return out, err
		}
		contrib.Position = i
		contrib.Role = "author"
		release.Contribs = append(release.Contribs, contrib)
	}
	for _, a := range xrefdoc.Editor {
		contrib, err := a.ToReleaseContrib(client)
		if err != nil {
			return out, err
		}
		contrib.Role = "editor"
		release.Contribs = append(release.Contribs, contrib)
	}
	for _, a := range xrefdoc.Translator {
		contrib, err := a.ToReleaseContrib(client)
		if err != nil {
			return out, err
		}
		contrib.Role = "translator"
		release.Contribs = append(release.Contribs, contrib)
	}

	// title, subtitle, original title

	if len(xrefdoc.Title) > 0 {
		release.Title = xrefdoc.Title[0]
	}

	if len(xrefdoc.Subtitle) > 0 {
		if len(xrefdoc.Subtitle[0]) > 1 {
			release.Subtitle = xrefdoc.Subtitle[0]
		}
	}

	if len(xrefdoc.OriginalTitle) > 0 {
		release.OriginalTitle = xrefdoc.OriginalTitle[0]
	}

	// date/year
	if len(xrefdoc.Issued.DateParts) > 0 {
		rawDate := xrefdoc.Issued.DateParts[0]
		if len(rawDate) == 3 {
			d, err := time.Parse("2006-01-02",
				fmt.Sprintf("%d-%02d-%02d", rawDate[0], rawDate[1], rawDate[2]))
			if err == nil {
				release.ReleaseDate = &d
			}
		} else if len(rawDate) > 0 {
			release.ReleaseYear = rawDate[0]
		}
	}

	id, err := createRelease(client, release)
	if err != nil {
		return out, err
	}

	out.Releases.Added++

	l.Debug("created release", id)

	if !isCrawlWanted(release) {
		return out, err
	}

	out.Releases.CrawlWanted++

	// porting the monster that is process_file from sandcrawler:python/sandcrawler/ingest_file.py
	spnClient, err := spnclient.NewDefaultClient(spnclient.SPNConfig{
		AccessKey: viper.GetString("spn.access_key"),
		SecretKey: viper.GetString("spn.secret_ket"),
		Endpoint:  viper.GetString("spn.endpoint"),
	})
	if err != nil {
		panic(err)
	}

	for _, u := range release.FulltextURLs() {
		crawler := PDFCrawler{
			SPNClient:  spnClient,
			MaxHops:    8,
			SimpleGets: viper.GetStringSlice("crawling.simple_get_list"),
			Blocklist:  viper.GetStringSlice("crawling.url_blocklist"),
		}

		res, err := crawler.Crawl(u)

		if err != nil {
			l.Info(fmt.Sprintf("%s: get failed: %s", release.ID, err.Error()))
			continue
		}

		l.Debug(fmt.Sprintf("%s: got result %v", release.ID, res))
		// TODO check result -- if success, break and continue. otherwise..?
		// results we care about later are going to be in the slog
		// question is if we should only use result for success and errors for failure
	}

	if err != nil {
		return out, nil
	}

	// TODO fetch pdf from wayback
	// TODO store pdf in seaweed? might not, might just shunt bytes to blobproc actually
	// TODO hand off to blob proc
	// TODO poll for blobproc result
	// TODO check metadata against FC record
	// TODO insert file into db (Acquired++)
	// TODO ingest PDF (Ingested++)
	return out, nil
}

type blockedError struct {
	RawURL    string
	ParsedURL string
	Pattern   string
}

func (e blockedError) Error() string {
	return fmt.Sprintf("blocked '%s' due to pattern '%s'", e.ParsedURL, e.Pattern)
}

type SPNError struct {
	Message string
	JobID   string
	URL     string
}

func (e SPNError) Error() string {
	return fmt.Sprintf("SPN job %s failed for '%s': %s", e.JobID, e.URL, e.Message)
}

// maybeRewriteURL looks for known patterns we can rewrite into direct PDF
// access. This would ideally be captured in the config file perhaps as sets of
// regular expressions with capture groups but is for now in a function for
// expediency.
func maybeRewriteURL(u string) string {
	if strings.HasPrefix(u, "https://arxiv.org/pdf/") && strings.HasSuffix(u, ".pdf") {
		return u[:len(u)-4]
	}
	if strings.HasPrefix(u, "https://onlinelibrary.wiley.com/doi/") {
		return strings.Replace(u, "doi", "doi/pdf", 1)
	}
	return u
}

type PDFCrawler struct {
	SPNClient  spnclient.Client
	MaxHops    int
	SimpleGets []string
	Blocklist  []string
}

type CrawlResult struct {
	// TODO
}

// TODO decide on CrawlResult
// TODO implement slog for crawl results

func (c PDFCrawler) Crawl(startURL string) (CrawlResult, error) {
	out := CrawlResult{}

	chain := []string{startURL}

	for len(chain) < c.MaxHops {
		u := chain[len(chain)-1]
		parsed, err := url.Parse(u)
		if err != nil {
			// TODO in the historical sandcrawler data there are a lot of URLs that
			// fail to parse -- none of them ever led to a hit. Thus, a fail here
			// should just give up on the PDF attempt and not bother with SPN.
			return out, fmt.Errorf("failed to parse url '%s': %w", startURL, err)
		}
		for _, p := range c.Blocklist {
			if strings.Contains(parsed.String(), p) {
				return out, blockedError{
					RawURL:    startURL,
					ParsedURL: parsed.String(),
					Pattern:   p,
				}
			}
		}

		u = c.maybeRewrite(u)

		// TODO keep filling in simple get list
		var simpleGet bool
		for _, prefix := range c.SimpleGets {
			if strings.HasPrefix(u, prefix) {
				simpleGet = true
				break
			}
		}

		req := spnclient.SaveRequest{
			URL:                u,
			CaptureAll:         true,
			ForceGet:           simpleGet,
			SkipFirstArchive:   true,
			DelayForJavascript: !simpleGet,
			JavascriptTimeout:  30,
		}

		// poll until we obtain a slot
		var jobID string
		for jobID == "" {
			resp, err := c.SPNClient.Save(req)
			if err != nil {
				// TODO slog
				return out, fmt.Errorf("spn api failure: %w", err)
			}

			if strings.Contains(resp.Message, "reached the limit of active sessions") {
				// TODO should we bail after N attempts?
				time.Sleep(6 * time.Second)
				continue
			}

			if strings.Contains(resp.Message, "The same snapshot had been made") {
				// TODO attempt to look up via cdx? or error?
			}

			if resp.JobID == "" {
				// TODO slog
				return out, SPNError{
					Message: resp.Message,
					URL:     resp.URL,
				}
			}

			jobID = resp.JobID
		}

		// poll until job completes
		var status spnclient.JobStatus
		for {
			status, err = c.SPNClient.StatusJob(jobID)
			if err != nil {
				// TODO slog
				return out, fmt.Errorf("could not get spn job status: %w", err)
			}

			if status.Status == "pending" {
				continue
			}

			break
		}

		if status.Status != "success" {
			// TODO slog
			return out, SPNError{
				Message: status.Message,
				URL:     status.OriginalURL,
				JobID:   status.JobID,
			}
		}

		// TODO status == success

		// TODO  original code had a check for 'original_url' starting with /; did
		// not see that coming up in the last month+ of sc logs so i'm leaving it
		// out. There was an additional check seeing if :// was not in a url; this
		// used a status called spn2-success-partial-url but I didn't see any cases
		// of that in the sc db

		// TODO evaluate this elsevier hack:
		/*
		   if "://pdf.sciencedirectassets.com/" in spn_result.request_url:
		   elsevier_pdf_cdx = wayback_client.cdx_client.lookup_best(
		       spn_result.request_url,
		       best_mimetype="application/pdf",
		   )
		   if elsevier_pdf_cdx and elsevier_pdf_cdx.mimetype == "application/pdf":
		       print("  Trying pdf.sciencedirectassets.com hack!", file=sys.stderr)
		       cdx_row = elsevier_pdf_cdx
		   else:
		       print("  Failed pdf.sciencedirectassets.com hack!", file=sys.stderr)
		       # print(elsevier_pdf_cdx, file=sys.stderr)
		*/

		// TODO cdx lookup by OriginalURL and Timestamp (old client's fetch)
		// TODO investigate high number of spn2-cdx-lookup-failure. Grabbing a
		// recent one, it's something we tried to get today and indeed have found
		// in the wayback machine now. The current delay for a CDX lookup is 9
		// seconds so we should up that; going to start with 30 seconds.

	}

	// is there value in consulting a database at this point?
	// what if we tracked success from an initial url? of course, the url will
	// have a distinctive path and likely be new/unique. but it should have some
	// kind of prefix.

	// lol.com/access/id/123/120931092830l12309123..123.123

	// domain is crude but still a nice indicator: how often does this domain
	// lead to a pdf? if it's less than 10%, we could skip
	// domain + 1 path element might be more useful but we can't rule out a top
	// level access path like lol.com/12039120390123 (which would be unique).

	// we could score each prefix based on number of attempts and number of successes

	// for a given domain + N paths, N >= 0
	// unknown: insufficient number of requests
	// likely: at least 10% hit rate
	// unlikely: <10% hit rate

	// lol.com: likely
	// lol.com/access: likely
	// lol.com/id: likely
	// lol.com/id/123: unknown
	// lol.com/id/123/12381283: unknown

	// ok.com: likely
	// ok.com/foo: unlikely
	// ok.com/foo/bar: unknown

	// paths just feel too volatile the more i think about it so i think domain
	// hit rate is more interesting

	// NB sandcrawler had a notion of "force_recrawl" which skipped the wayback
	// check. However, the daily crawls have force_recrawl set to false. I only
	// saw use of force_recrawl in a one-off script. It's unclear how often Bryan
	// may have relied on it in scripts not captured in git but I can't produce a
	// solid argument for having it. For now I'm always checking wayback. We can
	// introduce a "skip_wayback" type of argument to the workflow definition
	// when we find that we need it.

	// TODO wayback check

	// thoughts on crawling
	//
	// we're always starved for SPN slots. so, if we can request some % of our
	// urls without SPN that means, potentially, more papers in a day. However,
	// we are operating on the asusmption that we want headless browser for pdf
	// landing pages.
	//
	// if we didn't, we could just make plain http requests ourselves or via zeno
	// from 263 until we find a promising PDF link then submit the PDF link to
	// SPN.
	//
	// however, what if we used umbra for landing pages? it means more infra.

	// TODO

	return out, nil
}

func (c PDFCrawler) maybeRewrite(u string) string {
	if strings.HasPrefix(u, "https://arxiv.org/pdf/") && strings.HasSuffix(u, ".pdf") {
		return u[:len(u)-4]
	}
	if strings.HasPrefix(u, "https://onlinelibrary.wiley.com/doi/") {
		return strings.Replace(u, "doi", "doi/pdf", 1)
	}
	// TODO explore viewcontent.cgi rewrite opportunities
	// ie, if a doi.org link ends up as a viewcontent.cgi and there is a
	// consistent ID between the two we can cut out the doi.org hit and go
	// straight to viewcontent.
	return u
}

// isCrawlWanted returns true if we feel this release is worthy of a crawl attempt
func isCrawlWanted(release Release) bool {
	// TODO consider adding this to Release type
	doi := release.DOI()

	if doi == "" {
		return false
	}

	if !release.IsPaperlike() {
		return false
	}

	doiPrefixBlocklist := viper.GetStringSlice("crawling.doi_prefix_blocklist")
	for _, prefix := range doiPrefixBlocklist {
		if strings.HasPrefix(doi, prefix) {
			return false
		}
	}

	if len(release.FulltextURLs()) == 0 {
		return false
	}

	// TODO this check would only ever apply to releases that we already have
	// files for (see the is_preserved property) so I'm punting on it because it
	// doesn't make much sense. I think it's for processing a fatcat changelog in
	// a world where humans are updating things.
	/*
		  if (
		    es.get("publisher_type") == "big5"
		    and es.get("is_preserved")
		    and not (es["is_oa"] or in_acceptlist)
		):
		    return False
	*/

	// TODO these two checks seem to apply for datacite and arxiv, respectively. Punting on them for now:
	/*
	 # figshare
	 if doi and (doi.startswith("10.6084/") or doi.startswith("10.25384/")):
	     # don't crawl "most recent version" (aka "group") DOIs
	     if not release.version:
	         return False

	 # zenodo
	 if doi and doi.startswith("10.5281/"):
	     # if this is a "grouping" DOI of multiple "version" DOIs, do not crawl (will crawl the versioned DOIs)
	     if release.extra and release.extra.get("relations"):
	         for rel in release.extra["relations"]:
	             if rel.get("relationType") == "HasVersion" and rel.get(
	                 "relatedIdentifier", ""
	             ).startswith("10.5281/"):
	                 return False
	*/

	return false
}

func licenseSlugLookup(rawURL string) string {
	if rawURL == "" {
		return ""
	}

	rawURL = strings.ToLower(rawURL)
	rawURL = strings.TrimSuffix(rawURL, "/")
	rawURL = strings.ReplaceAll(rawURL, "https://", "//")
	rawURL = strings.ReplaceAll(rawURL, "http://", "//")
	if strings.Contains(rawURL, "creativecommons.org") {
		rawURL = strings.ReplaceAll(rawURL, "/legalcode", "")
		rawURL = strings.ReplaceAll(rawURL, "/uk", "")
	}
	return licenseSlugMap[rawURL]
}

// createContainer creates a new container in fc2 and returns its ID
func createContainer(client *http.Client, c Container) (uuid.UUID, error) {
	c.Source = "dev" // TODO thread this value through from invocation of workflow
	c.ID = uuid.New()

	legacy, err := lookupLegacyContainer(client, c.ISSNL)
	if err != nil {
		return uuid.Nil, fmt.Errorf("legacy lookup failed: %w", err)
	}

	if legacy != nil {
		c.ID = legacy.Ident
		c.LegacyRevID = legacy.Revision
	}

	fc2url := viper.GetString("fatcat2.endpoint")
	fc2key := viper.GetString("fatcat2.key")

	bs, err := json.Marshal(c)

	body := bytes.NewBuffer(bs)
	req, err := http.NewRequest("POST", fc2url+"/container", body)
	if err != nil {
		panic(err)
	}
	req.Header.Set("X-API-Key", fc2key)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return uuid.Nil, fmt.Errorf("container POST failed for '%#v': %w", c, err)
	}

	if resp.StatusCode != 201 {
		b, _ := io.ReadAll(resp.Body)
		return uuid.Nil, fmt.Errorf("unexpected status code for '%#v' POST: %d; body '%s'", c, resp.StatusCode, b)
	}

	return c.ID, nil
}

// createRelease creates a new release in fc2 and returns its ID
func createRelease(client *http.Client, r Release) (uuid.UUID, error) {
	r.Source = "dev" // TODO thread this value through from invocation of workflow
	r.ID = uuid.New()

	var doi string
	for _, eid := range r.ExternalIDs {
		if eid.Type == "doi" {
			doi = eid.Value
			break
		}
	}
	if doi == "" {
		panic("nothing without a doi should get to this point")
	}

	legacy, err := lookupLegacyRelease(client, doi)
	if err != nil {
		return uuid.Nil, fmt.Errorf("legacy lookup failed: %w", err)
	}

	if legacy != nil {
		r.ID = legacy.Ident
		r.LegacyRevID = legacy.Revision
	}

	fc2url := viper.GetString("fatcat2.endpoint")
	fc2key := viper.GetString("fatcat2.key")

	bs, err := json.Marshal(r)

	body := bytes.NewBuffer(bs)
	req, err := http.NewRequest("POST", fc2url+"/release", body)
	if err != nil {
		panic(err)
	}
	req.Header.Set("X-API-Key", fc2key)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return uuid.Nil, fmt.Errorf("release POST failed for '%#v': %w", r, err)
	}

	if resp.StatusCode != 201 {
		b, _ := io.ReadAll(resp.Body)
		return uuid.Nil, fmt.Errorf("unexpected status code for '%s' (%s) POST: %d; body '%s'",
			doi, r.LegacyRevID, resp.StatusCode, b)
	}

	return r.ID, nil
}

// TODO make a client wrapper
//type FC2Client http.Client

// TODO generalize lookup functions
type LegacyData struct {
	Ident    uuid.UUID
	Revision uuid.UUID
}

func fc2uuid(fatcatIdent string) (uuid.UUID, error) {
	i := strings.ToUpper(fatcatIdent + "======")
	decoded, err := base32.StdEncoding.DecodeString(i)
	if err != nil {
		return uuid.Nil, err
	}

	return uuid.FromBytes(decoded)
}

func lookupLegacy(c *http.Client, endpoint, idtype, idvalue string) (*LegacyData, error) {
	fc1url := viper.GetString("fatcat1.endpoint")
	req, err := http.NewRequest("GET", fc1url+"/"+endpoint, nil)
	if err != nil {
		panic(err)
	}
	q := req.URL.Query()
	q.Add("extid_type", idtype)
	q.Add("extid_value", idvalue)
	req.URL.RawQuery = q.Encode()
	resp, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fc1 lookup failed for %s of '%s': %w", idtype, idvalue, err)
	}
	if resp.StatusCode == 404 {
		return nil, nil
	}

	if resp.StatusCode != 200 {
		// NB invalid issnls crash the server (500). if we get bad data from
		// crossref it will stop the activity cold. might have to patch fc1 to
		// return a 400.
		return nil, fmt.Errorf(
			"did not get 200 nor 404 from fc1 for %s of '%s' lookup: %d",
			idtype, idvalue, resp.StatusCode)
	}

	bs, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var p struct {
		Ident    string
		Revision uuid.UUID
	}

	err = json.Unmarshal(bs, &p)
	if err != nil {
		return nil, fmt.Errorf("unmarshal failed for %s of '%s': %w", idtype, idvalue, err)
	}

	ident, err := fc2uuid(p.Ident)
	if err != nil {
		return nil, err
	}

	return &LegacyData{
		Ident:    ident,
		Revision: p.Revision,
	}, nil
}

func lookupLegacyContainer(c *http.Client, issnl string) (*LegacyData, error) {
	return lookupLegacy(c, "lookup_container", "issnl", issnl)
}

func lookupLegacyRelease(c *http.Client, doi string) (*LegacyData, error) {
	return lookupLegacy(c, "lookup_release", "doi", doi)
}

func lookupCreator(c *http.Client, orcid string) (uuid.UUID, error) {
	fc2url := viper.GetString("fatcat2.endpoint")
	req, err := http.NewRequest("GET", fc2url+"/creator/lookup", nil)
	if err != nil {
		panic(err)
	}

	q := req.URL.Query()
	q.Add("id_type", "orcid")
	q.Add("id_value", orcid)
	req.URL.RawQuery = q.Encode()
	resp, err := c.Do(req)
	if err != nil {
		return uuid.Nil, fmt.Errorf("fc2 lookup failed for '%s': %w", orcid, err)
	}

	if resp.StatusCode == 404 {
		return uuid.Nil, nil
	}

	if resp.StatusCode != 200 {
		return uuid.Nil, fmt.Errorf("did not get 200 nor 404 from fc2 for '%s' lookup: %d",
			orcid, resp.StatusCode)
	}

	bs, err := io.ReadAll(resp.Body)
	if err != nil {
		return uuid.Nil, err
	}

	var p struct {
		ID uuid.UUID
	}

	err = json.Unmarshal(bs, &p)
	if err != nil {
		return uuid.Nil, fmt.Errorf("unmarshal failed for '%s': %w", orcid, err)
	}

	return p.ID, nil
}

func lookupContainer(c *http.Client, issnl string) (uuid.UUID, error) {
	fc2url := viper.GetString("fatcat2.endpoint")
	req, err := http.NewRequest("GET", fc2url+"/container/lookup", nil)
	if err != nil {
		panic(err)
	}

	q := req.URL.Query()
	q.Add("id_type", "issnl")
	q.Add("id_value", issnl)
	req.URL.RawQuery = q.Encode()
	resp, err := c.Do(req)
	if err != nil {
		return uuid.Nil, fmt.Errorf("fc2 lookup failed for '%s': %w", issnl, err)
	}

	if resp.StatusCode == 404 {
		return uuid.Nil, nil
	}

	if resp.StatusCode != 200 {
		return uuid.Nil, fmt.Errorf("did not get 200 nor 404 from fc2 for '%s' lookup: %d",
			issnl, resp.StatusCode)
	}

	bs, err := io.ReadAll(resp.Body)
	if err != nil {
		return uuid.Nil, err
	}

	var p struct {
		ID uuid.UUID
	}

	err = json.Unmarshal(bs, &p)
	if err != nil {
		return uuid.Nil, fmt.Errorf("unmarshal failed for '%s': %w", issnl, err)
	}

	return p.ID, nil
}

func isExistingDOI(c *http.Client, doi string) (bool, error) {
	fc2url := viper.GetString("fatcat2.endpoint")
	req, err := http.NewRequest("GET", fc2url+"/release/lookup", nil)
	if err != nil {
		panic(err)
	}
	q := req.URL.Query()
	q.Add("id_type", "doi")
	q.Add("id_value", doi)
	req.URL.RawQuery = q.Encode()
	resp, err := c.Do(req)
	if err != nil {
		return false, fmt.Errorf("fc2 lookup failed for '%s': %w", doi, err)
	}

	if resp.StatusCode == http.StatusOK {
		return true, nil
	}

	if resp.StatusCode != 404 {
		err = fmt.Errorf("did not get 200 nor 404 from fc2 for '%s' lookup: %d",
			doi, resp.StatusCode)
	}

	return false, err
}

type findLineBatchInput struct {
	S3Key  string
	Offset int64
}

type findLineBatchOutput struct {
	Offsets   [][]int64
	BytesRead int64
	EOF       bool
}

func findLineBatch(ctx context.Context, in findLineBatchInput) (out findLineBatchOutput, err error) {
	out = findLineBatchOutput{}

	l := activity.GetLogger(ctx)
	l.Info(fmt.Sprintf("doing a range read from '%s'", in.S3Key))

	f, err := getS3Object(ctx, in.S3Key)
	if err != nil {
		return
	}
	defer f.Close()

	batchSize := 1000 // TODO set in config

	// TODO refactor this so it's unit testable
	chunkSize := 1024 * 100 // TODO set in config
	out.BytesRead = in.Offset
	curLineStart := in.Offset

	var done bool
	var curLineLength int64

	for !done {
		b := make([]byte, chunkSize)
		n, err := f.ReadAt(b, out.BytesRead)
		l.Debug(fmt.Sprintf("read %d bytes", n))
		if errors.Is(err, io.EOF) {
			l.Debug("saw EOF")
			out.EOF = true
			err = nil
		}
		if err != nil {
			return out, fmt.Errorf("range read of '%s' failed: %w", in.S3Key, err)
		}
		if n == 0 {
			return out, nil
		}
		for x := range n {
			out.BytesRead++
			curLineLength++
			if b[x] == '\n' {
				out.Offsets = append(out.Offsets, []int64{curLineStart, curLineLength})
				if len(out.Offsets) == batchSize {
					done = true
					break
				}
				curLineStart = out.BytesRead
				curLineLength = 0
			}
		}

		if out.EOF {
			done = true
		}
	}

	return
}

func crossrefCrawlWorkflow(ctx workflow.Context, in CrossrefCrawlInput) (counts, error) {
	l := workflow.GetLogger(ctx)
	out := counts{}

	// fetch crossref metadata from the upstream API and store in s3

	ao := workflow.ActivityOptions{
		StartToCloseTimeout: 8 * 60 * 60 * time.Second,
		TaskQueue:           viper.GetString("crossref.external_task_queue"),
	}
	ctx = workflow.WithActivityOptions(ctx, ao)

	var skOut skCrossrefOutput
	err := workflow.ExecuteActivity(ctx, skCrossref, in.SKInput).Get(ctx, &skOut)
	if err != nil {
		workflow.GetLogger(ctx).Error("scholkit crossref activity failed:", err)
		return out, err
	}
	workflow.GetLogger(ctx).Info("scholkit crossref s3key:", skOut.S3Key)

	ao = workflow.ActivityOptions{
		StartToCloseTimeout: 8 * 60 * 60 * time.Second,
		TaskQueue:           viper.GetString("crossref.internal_task_queue"),
	}
	ctx = workflow.WithActivityOptions(ctx, ao)

	batchInput := lineBatchInput{
		S3Key: skOut.S3Key,
	}
	findInput := findLineBatchInput{
		S3Key: skOut.S3Key,
	}
	findOutput := findLineBatchOutput{}
	childSelector := workflow.NewSelector(ctx)
	var childCount int

	var childErr error
	var childCounts counts
	for {
		err := workflow.ExecuteActivity(ctx, findLineBatch, findInput).Get(ctx, &findOutput)
		if err != nil {
			return out, err
		}
		if len(findOutput.Offsets) > 0 {
			findInput.Offset = findOutput.BytesRead
			batchInput.Offsets = findOutput.Offsets
			childWorkflowOptions := workflow.ChildWorkflowOptions{
				ParentClosePolicy: enums.PARENT_CLOSE_POLICY_ABANDON,
			}
			ctx = workflow.WithChildOptions(ctx, childWorkflowOptions)
			fut := workflow.ExecuteChildWorkflow(ctx, lineBatchWorkflow, batchInput)
			var cwe workflow.Execution
			err := fut.GetChildWorkflowExecution().Get(ctx, &cwe)
			if err != nil {
				return out, err
			}
			childSelector.AddFuture(fut, func(f workflow.Future) {
				childErr = f.Get(ctx, &childCounts)
			})
			childCount++
		}
		if findOutput.EOF {
			break
		}
	}

	for range childCount {
		childSelector.Select(ctx)
		if childErr != nil {
			return out, err
		}
		out = out.Add(childCounts)
		l.Info(fmt.Sprintf("child ignored %d lines", childCounts.Releases.Ignored))
	}

	l.Info(fmt.Sprintf("%#v", out))

	return out, nil

	// TODO handle the outcome of a crawl

	// TODO activity: for each fatcat ID, attempt to acquire a paper; each of these returns an s3 key for parsing
	// TODO activity: given an s3 key for a pdf, do text extraction; returns either s3 key or the textual result of parsing
	// TODO activity: bulk ingestion into ES of parsed stuff
}
