package crossref

import (
	"context"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"git.archive.org/webgroup/scholar/trawler/cleaning"
	"git.archive.org/webgroup/scholar/trawler/counts"
	"git.archive.org/webgroup/scholar/trawler/fatcat2"
	"git.archive.org/webgroup/scholar/trawler/indexing"
	"git.archive.org/webgroup/scholar/trawler/issn"
	"git.archive.org/webgroup/scholar/trawler/orcid"
	"github.com/google/uuid"
	"github.com/spf13/viper"
	"go.temporal.io/sdk/activity"
)

// scrape crossref for a day's worth of metadata: huge ndjson file in s3, each line a paper-like entity
// for each entity, create an entry in fatcat2
// for each entity, if there's a suitable URL, try and obtain a PDF
// for each obtained PDF
// - extract metadata and make sure it matches the fatcat2 record
// - create a file entry in fatcat2
// - extract fulltext and ingest into elasticsearch

const minAbstractLength = 75

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

func (cc crossrefContributor) ToReleaseContrib(client *http.Client) (fatcat2.ReleaseContrib, error) {
	out := fatcat2.ReleaseContrib{
		GivenName: cc.Given,
		Surname:   cc.Family,
	}
	if cc.ORCID != "" {
		orcidVal := orcid.Normalize(cc.ORCID)
		id, err := fatcat2.LookupOrcid(client, orcidVal)
		if err != nil {
			return out, err
		}
		out.CreatorID = id
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

// SkipReason returns a reason to skip a DOI or empty string if it's not skippable
func (c crossrefDoc) SkipReason() string {
	if len(c.Title) == 0 {
		return "no-titles"
	}

	if len(c.Title[0]) < 2 {
		return "short-title"
	}

	if strings.ToLower(c.Title[0]) == "oup accepted manuscript" {
		return "filtered-title"
	}

	if slices.Contains(ignoredTypes, c.Type) {
		return "filtered-type"
	}

	if c.DOI == "" {
		return "empty-doi"
	}

	if len(c.Reference) > 5000 {
		return "too-many-refs"
	}

	if len(c.Author)+len(c.Editor)+len(c.Translator) > 2000 {
		return "too-many-authors"
	}

	return ""
}

func ProcessLine(ctx context.Context, client *http.Client, source string, lineb []byte) (counts.Counts, *fatcat2.Release, error) {
	out := counts.Counts{}
	l := activity.GetLogger(ctx)

	var release *fatcat2.Release

	var xrefdoc crossrefDoc
	err := json.Unmarshal(lineb, &xrefdoc)
	if err != nil {
		return out, release, err
	}

	l.Info(fmt.Sprintf("got a '%s' with doi '%s'", xrefdoc.Type, xrefdoc.DOI))

	if reason := xrefdoc.SkipReason(); reason != "" {
		l.Info(fmt.Sprintf("skipping doi '%s': %s", xrefdoc.DOI, reason))
		out.Releases.Skipped++
		return out, release, nil
	}

	// Check the DOI

	release, err = xrefToFc(client, xrefdoc)
	if err != nil {
		return out, release, fmt.Errorf("could not transform xref->fc2: %w", err)
	}

	foundId, err := fatcat2.LookupDoi(client, strings.ToLower(xrefdoc.DOI))
	if err != nil {
		return out, release, err
	}

	if foundId == nil {
		release.Source = source
		release, err = createRelease(client, &out, *release, xrefdoc)
		if err != nil {
			return out, release, fmt.Errorf("failed to create release for doi '%s': %w", xrefdoc.DOI, err)
		}
		l.Debug(fmt.Sprintf("created release %s", release.ID))
		out.Releases.Added++
	} else {
		// TODO here is where we could update release with info from xref should we so desire
		r, err := fatcat2.GetRelease(client, *foundId)
		if err != nil {
			return out, release, fmt.Errorf("could not look up existing release: %w", err)
		}
		release = &r
		out.Releases.Ignored++

		l.Debug(fmt.Sprintf("found release %s", release.ID))
	}

	return out, release, nil
}

func xrefToFc(client *http.Client, xrefdoc crossrefDoc) (*fatcat2.Release, error) {
	release := &fatcat2.Release{
		Contribs:    []fatcat2.ReleaseContrib{},
		ExternalIDs: []fatcat2.ExternalID{},
		Extra:       map[string]any{},
		Publisher:   xrefdoc.Publisher,
		Volume:      xrefdoc.Volume,
		Issue:       xrefdoc.Issue,
		Pages:       xrefdoc.Page,
		Language:    xrefdoc.Language,
	}

	var releaseType string
	releaseType, ok := releaseTypeMap[xrefdoc.Type]
	if !ok {
		return release, fmt.Errorf("found unknown crossref type '%s'", xrefdoc.Type)
	}
	release.Type = releaseType

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
	release.Refs = []fatcat2.RawRef{}

	for i, cref := range xrefdoc.Reference {
		rawRef := fatcat2.RawRef{
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

	// TODO find out if any release has more than one abstract in database
	release.Abstracts = []fatcat2.Abstract{}
	abs := cleaning.CleanString(cleaning.DeTag(xrefdoc.Abstract))
	if len(abs) > minAbstractLength {
		h := sha1.Sum([]byte(abs))
		release.Abstracts = append(release.Abstracts, fatcat2.Abstract{
			MIMEType: "application/xml+jats",
			Content:  abs,
			Language: xrefdoc.Language,
			SHA1:     fmt.Sprintf("%x", h),
		})
	}

	// TODO noticed this as the entire content of an abstract, should filter out stuff like this:
	// Dieser Artikel ist nur als PDF-Dokument verfügbar.
	// (This article is only available as a PDF document.)

	// "extra" stuff (ugh)
	if release.ContainerID != nil && len(xrefdoc.ContainerTitle) > 1 {
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

	release.ExternalIDs = append(release.ExternalIDs, fatcat2.ExternalID{
		Type:  "doi",
		Value: strings.ToLower(xrefdoc.DOI),
	})

	if len(xrefdoc.ISBN) > 0 {
		for _, isbn := range xrefdoc.ISBN {
			if len(isbn) == 17 {
				release.ExternalIDs = append(release.ExternalIDs, fatcat2.ExternalID{
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
			return release, err
		}
		contrib.Position = i
		contrib.Role = "author"
		release.Contribs = append(release.Contribs, contrib)
	}
	for _, a := range xrefdoc.Editor {
		contrib, err := a.ToReleaseContrib(client)
		if err != nil {
			return release, err
		}
		contrib.Role = "editor"
		release.Contribs = append(release.Contribs, contrib)
	}
	for _, a := range xrefdoc.Translator {
		contrib, err := a.ToReleaseContrib(client)
		if err != nil {
			return release, err
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
				rd := fatcat2.ReleaseDate(d)
				release.ReleaseDate = &rd
			}
		} else if len(rawDate) > 0 {
			release.ReleaseYear = rawDate[0]
		}
	}

	return release, nil
}

func createRelease(client *http.Client, cs *counts.Counts, release fatcat2.Release, xrefdoc crossrefDoc) (*fatcat2.Release, error) {
	var containerTitle string

	if len(xrefdoc.ContainerTitle) > 0 {
		// TODO fatcat importer is using ftfy to clean this value up; we can do
		// that on the server side on container creation.
		// TODO fatcat importer was arbitrarily using the first container title in
		// the list so I've continued that practice but it feels weird
		containerTitle = cleaning.CleanString(cleaning.DeTag(xrefdoc.ContainerTitle[0]))
	}

	var issnl string
	for _, i := range xrefdoc.ISSN {
		issnl = issn.ISSN2ISSNL(i)
		if issnl != "" {
			break
		}
	}

	var containerID *uuid.UUID
	var err error

	if issnl != "" {
		// TODO could build a map of issnl->cid somewhere to save on requests
		containerID, err = fatcat2.LookupIssnl(client, issnl)
		if err != nil {
			return nil, err
		}
	}

	if containerID == nil {
		if containerTitle != "" && issnl != "" {
			c := fatcat2.Container{
				Name:      containerTitle,
				ISSNL:     issnl,
				Publisher: xrefdoc.Publisher,
				Type:      containerTypeMap[release.Type],
				Source:    release.Source,
			}
			// TODO clean this up. Create* should modify a referenced struct, not
			// return a UUID; we should create a container in scope for later instaed
			// of re-fetching it with GetContainer for indexing
			containerID, err = fatcat2.CreateContainer(client, &c)
			if err != nil {
				return nil, err
			}
			cs.Containers.Added++

			// NB if this fails we won't hit this code path on re-run. a smell that
			// suggests we should just be consulting changelog for this indexing.
			containerDoc := indexing.PrepareFatcatContainerDoc(c)
			bs, err := json.Marshal(containerDoc)
			if err != nil {
				return nil, fmt.Errorf("could not marshal container doc: %w", err)
			}
			err = indexing.DoElasticIndex(client, viper.GetString("indexing.fatcat_container_ix"),
				containerDoc.LegacyIdent, bs)
			if err != nil {
				return nil, fmt.Errorf("could not index container '%s': %w", c.ID, err)
			}
		} else if containerTitle != "" {
			release.Extra["container_name"] = containerTitle
			cs.Containers.Skipped++
		}
	} else {
		cs.Containers.Ignored++
	}

	release.ContainerID = containerID

	// TODO CreateRelease needs to return the fully hydrated release with things like work id set, not just the ID
	id, err := fatcat2.CreateRelease(client, release)
	if err != nil {
		return nil, err
	}

	r, err := fatcat2.GetRelease(client, *id)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch new release '%s': %w", id, err)
	}

	release = r

	releaseDoc, err := indexing.PrepareFatcatReleaseDoc(client, release)
	if err != nil {
		return nil, fmt.Errorf("failed to transform release '%s' into ES doc: %w", release.ID, err)
	}

	bs, err := json.Marshal(releaseDoc)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal release es doc: %w", err)
	}

	err = indexing.DoElasticIndex(client,
		viper.GetString("indexing.fatcat_release_ix"), releaseDoc.LegacyIdent, bs)
	if err != nil {
		return nil, fmt.Errorf("failed to index doc for release '%s': %w", release.ID, err)
	}

	return &release, nil
}
