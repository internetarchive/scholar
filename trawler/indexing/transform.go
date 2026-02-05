package indexing

import (
	"bytes"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	fc2 "git.archive.org/webgroup/scholar/trawler/fatcat2"
	"github.com/PuerkitoBio/goquery"
	"github.com/google/uuid"
	"github.com/miku/grobidclient/tei"
	"golang.org/x/net/publicsuffix"
)

const maxBodySize = 512 * 1024

type FulltextTransformCtx struct {
	HttpClient *http.Client
	Release    fc2.Release
	File       *fc2.File
	Container  *fc2.Container
	GrobidXML  []byte
	PdfText    []byte
	// TODO thumbnail?
}

func PrepareFatcatReleaseDoc(client *http.Client, release fc2.Release) (FatcatReleaseDocV1, error) {
	out := FatcatReleaseDocV1{}
	extra := release.Extra
	if extra == nil {
		extra = map[string]any{}
	}

	var container *fc2.Container
	if release.ContainerID != nil {
		c, err := fc2.GetContainer(client, *release.ContainerID)
		if err != nil {
			return out, err
		}
		container = &c
	}

	files, err := fc2.ReleaseFiles(client, release.ID)
	if err != nil {
		return out, err
	}

	// TODO conceivable we want to continue using this to mark deletion but hardcoding for now
	out.State = "active"
	out.IndexTime = time.Now()
	out.LegacyIdent = fc2.UuidToLegacy(release.ID)
	out.LegacyWorkIdent = fc2.UuidToLegacy(*release.WorkID)
	out.Title = release.Title
	out.Subtitle = release.Subtitle
	out.OriginalTitle = release.OriginalTitle
	out.Type = release.Type
	out.Stage = release.Stage
	out.WithdrawnStatus = release.WithdrawnStatus
	out.Language = release.Language
	out.Volume = release.Volume
	out.Issue = release.Issue
	out.Pages = release.Pages
	out.Number = release.Number
	out.License = release.LicenseSlug
	if container != nil {
		out.ContainerName = container.Name
	}
	out.Version = release.Version

	out.Publisher = release.Publisher
	if out.Publisher == "" {
		out.Publisher = container.Publisher
	}

	// NB copypasta
	for _, eid := range release.ExternalIDs {
		if eid.Type == "doi" {
			out.DOI = eid.Value
			out.DOIPrefix = doiPrefix(eid.Value)
			if release.Extra != nil {
				if _, ok := release.Extra["crossref"]; ok {
					// TODO post-xref-poc other registrars
					// TODO is there even evidence that this is useful
					out.DOIRegistrar = "crossref"
				}
			}
		}
		// TODO post-xref-poc
		//PMID    string `json:"pmid,omitempty"`
		//PMCID   string `json:"pmcid,omitempty"`
		//ISBN13  string `json:"isbn13,omitempty"`
		//ArxivID string `json:"arxiv_id,omitempty"`
		//JstorID string `json:"jstor_id,omitempty"`
		//DoajID  string `json:"doaj_id,omitempty"`
		//DblpID  string `json:"dblp_id,omitempty"`
		//OAIID   string `json:"oai_id,omitempty"`
	}

	out.InDOAJ = out.DoajID != ""
	out.InJSTOR = out.JstorID != ""

	// TODO post-xref-poc
	// based on a note in the old code bryan thought that all crossref stuff is
	// in kbart so we'll continue this line of thinking?
	if _, ok := extra["crossref"]; ok {
		out.InKBART = true
	}

	out.ReleaseYear = release.ReleaseYear
	if release.ReleaseDate != nil {
		out.ReleaseDate = release.ReleaseDate.Format("2006-01-02")
	}

	if len(release.Abstracts) > 0 {
		out.AnyAbstract = true
	}

	out.RefCount = len(release.Refs)

	out.ContribCount = len(release.Contribs)
	out.ContribNames = []string{}
	out.CreatorLegacyIdents = []string{}
	out.Affiliations = []string{}
	for _, c := range release.Contribs {
		out.ContribNames = append(out.ContribNames, contribToName(client, c))
		if c.CreatorID != uuid.Nil {
			out.CreatorLegacyIdents = append(out.CreatorLegacyIdents, fc2.UuidToLegacy(c.CreatorID))
		}
		if c.RawAffiliation != "" {
			out.Affiliations = append(out.Affiliations, c.RawAffiliation)
		}
	}

	if len(files) > 0 {
		anyUrl := ""
		out.FileCount = len(files)
		for _, f := range files {
			if strings.Contains(out.BestPdfUrl, "//web.archive.org") {
				break
			}
			for _, u := range f.URLs {
				if strings.Contains(u.URL, "//web.archive.org") {
					out.BestPdfUrl = u.URL
					out.IaPdfUrl = u.URL
					out.InIA = true
					break
				} else if strings.Contains(u.URL, "//archive.org") {
					out.BestPdfUrl = u.URL
					out.IaPdfUrl = u.URL
					out.InIA = true
				} else {
					anyUrl = u.URL
				}
			}
		}

		if out.BestPdfUrl == "" {
			out.BestPdfUrl = anyUrl
		}
	}

	out.FirstPage, _, _ = strings.Cut(release.Pages, "-")

	license := strings.ToLower(release.LicenseSlug)
	// TODO if this index sticks around this logic should be consolidated with
	// whats in generateTags
	if strings.HasPrefix(license, "cc-") {
		out.IsOA = true
	} else if strings.HasPrefix(license, "arxiv-") {
		out.IsOA = true
	} else if container != nil && container.Extra != nil {
		if dl, ok := container.Extra["default_license"]; ok {
			if strings.HasPrefix(strings.ToLower(dl.(string)), "cc-") {
				out.IsOA = true
			}
		}
	} else if _, ok := extra["crossref"]; ok {
		if len(files) > 0 {
			// we found this release via xref and found a file for it; that implies OA
			// TODO this logic is questionable but ok for now
			out.IsOA = true
		}
	} else if eoa, ok := extra["is_oa"]; ok {
		out.IsOA = eoa.(bool)
	} else if loa, ok := extra["longtail_oa"]; ok && loa.(bool) {
		out.IsOA = true
		out.IsLongtailOA = true
	} else if out.InDOAJ {
		out.IsOA = true
	}

	if out.InIA || out.InKBART || out.InJSTOR || out.PMCID != "" || out.ArxivID != "" {
		out.IsPreserved = true
	}

	if out.InIA {
		out.Preservation = "bright"
	} else if out.IsPreserved {
		out.Preservation = "dark"
	} else {
		out.Preservation = "none"
	}

	// NB i am leaving out the in_shadows prop for now

	// NB these were never set
	// Tags  []string `json:"tags"`
	// InWeb bool `json:"in_web"`
	// RefReleaseLegacyIdents []string `json:"ref_release_ids"`
	// RefLinkedCount         int      `json:"ref_linked_count"`

	// TODO post-xref-poc
	// InIASim      bool `json:"in_ia_sim"`

	return out, nil
}

func PrepareFatcatContainerDoc(container fc2.Container) FatcatContainerDocV1 {
	out := FatcatContainerDocV1{}
	// TODO post-xref-poc handle other states
	out.State = "active"

	out.IndexTime = time.Now()
	out.LegacyIdent = fc2.UuidToLegacy(container.ID)
	out.Name = container.Name
	out.Publisher = container.Publisher
	out.Type = container.Type
	out.Issnl = container.ISSNL
	out.Issne = container.ISSNE
	out.Issnp = container.ISSNP
	out.Issns = []string{}
	if out.Issnl != "" {
		out.Issns = append(out.Issns, out.Issnl)
	}
	if out.Issne != "" {
		out.Issns = append(out.Issns, out.Issne)
	}
	if out.Issnp != "" {
		out.Issns = append(out.Issns, out.Issnp)
	}

	/*
		  TODO post-xref-poc handle anything in container.extra which for xref is unused
			Languages         []string  `json:"languages"`
			SimPubID          string    `json:"sim_pubid,omitempty"`
			IaSimCollection   string    `json:"ia_sim_collection,omitempty"`
			IsOA              bool      `json:"is_oa"`
			IsLongtailOA      bool      `json:"is_longtail_oa"`
	*/
	return out
}

func PrepareFatcatFileDoc(file fc2.File) FatcatFileDocV1 {
	// TODO i'm wary of the multiple releases per file thing, should investigate
	out := FatcatFileDocV1{}
	out.LegacyIdent = fc2.UuidToLegacy(file.ID)
	// TODO post-xref-poc
	out.State = "active"

	out.IndexTime = time.Now()
	for _, r := range file.Releases {
		out.ReleaseLegacyIdents = append(out.ReleaseLegacyIdents, fc2.UuidToLegacy(r.ID))
	}
	out.ReleaseCount = len(file.Releases)
	out.Mimetype = file.Mimetype
	out.Size = file.Size
	out.Sha1 = file.Sha1
	out.Sha256 = file.Sha256
	out.Md5 = file.Md5

	if len(file.URLs) == 0 {
		return out
	}

	out.Hosts = []string{}
	out.Domains = []string{}
	out.Rels = []string{}

	for _, u := range file.URLs {
		out.Rels = append(out.Rels, u.Rel)
		pu, err := url.Parse(u.URL)
		if err != nil {
			continue
		}
		out.Hosts = append(out.Hosts, pu.Host)
		d, err := publicsuffix.EffectiveTLDPlusOne(pu.Host)
		if err != nil {
			continue
		}

		out.Domains = append(out.Domains, d)

		if strings.Contains(d, "archive.org") {
			out.InIA = true
			out.BestURL = u.URL
		}

		if out.BestURL == "" {
			out.BestURL = u.URL
		}
	}

	if out.BestURL == "" {
		// pick an arbitrary one
		out.BestURL = file.URLs[0].URL
	}

	out.InIaPetabox = slices.Contains(out.Hosts, "archive.org")

	return out
}

func PrepareFulltextDoc(ictx FulltextTransformCtx) ScholarDocV1 {
	out := ScholarDocV1{}
	release := ictx.Release
	container := ictx.Container
	client := ictx.HttpClient

	out.Type = "work"
	out.LegacyWorkIdent = fc2.UuidToLegacy(*release.WorkID)
	out.Key = fmt.Sprintf("work_%s", out.LegacyWorkIdent)
	out.IndexTime = time.Now()
	out.CollapseKey = out.LegacyWorkIdent

	// biblio field
	out.Biblio = ScholarBiblioV1{}
	if release.Publisher != "" {
		out.Biblio.Publisher = release.Publisher
	} else if container != nil {
		out.Biblio.Publisher = container.Publisher
	}

	if container != nil {
		out.Biblio.LegacyContainerIdent = fc2.UuidToLegacy(container.ID)
		out.Biblio.ContainerType = container.Type
		out.Biblio.ContainerISSNL = container.ISSNL
		out.Biblio.ISSNs = []string{}
		issns := []string{container.ISSNL, container.ISSNE, container.ISSNP}
		for _, i := range issns {
			// NB this is another spot where the original code used container.extra and
			// ignored row level fields
			if i != "" {
				out.Biblio.ISSNs = append(out.Biblio.ISSNs, i)
			}
		}
		if container.Extra != nil {
			if publisherType, ok := container.Extra["publisher_type"]; ok {
				out.Biblio.PublisherType = publisherType.(string)
			}
			if originalName, ok := container.Extra["original_name"]; ok {
				out.Biblio.ContainerOriginalName = originalName.(string)
			}
			// NB unlikely to be called for anything new since this system seems fully abandoned
			if sherpaRomeo, ok := container.Extra["sherpa_romeo"]; ok {
				if color, ok := sherpaRomeo.(map[string]any)["color"]; ok {
					out.Biblio.ContainerSherpaColor = color.(string)
				}
			}
		}
	}

	if release.Extra != nil {
		if country, ok := release.Extra["country"]; ok {
			out.Biblio.CountryCode = country.(string)
		}
	}

	out.Biblio.FirstPage, _, _ = strings.Cut(release.Pages, "-")
	out.Biblio.FirstPageInt = aToSmallInt(out.Biblio.FirstPage)

	out.Biblio.LegacyReleaseIdent = fc2.UuidToLegacy(release.ID)

	// NB these fields used clean_str to run ftfy (unicode cleanup),
	// beautifulsoup (html strip), some regexes, whitespace collapsing. i'd like
	// to see this become an issue before worrying about it again.
	out.Biblio.Title = release.Title
	out.Biblio.OriginalTitle = release.OriginalTitle
	out.Biblio.Subtitle = release.Subtitle
	out.Biblio.OriginalTitle = release.OriginalTitle

	// NB these fields used to get set to nil if the year was greater than 2025
	// (lol) or less than 1500. That disturbed me; we should endeavour to clean
	// that data prior to indexing. I have left it out.
	if release.ReleaseDate != nil {
		t := time.Time(*release.ReleaseDate)
		out.Biblio.ReleaseDate = &t
	}
	out.Biblio.ReleaseYear = release.ReleaseYear

	out.Biblio.ReleaseType = release.Type
	out.Biblio.ReleaseStage = release.Stage
	out.Biblio.WithdrawnStatus = release.WithdrawnStatus
	out.Biblio.Language = release.Language

	out.Biblio.Volume = release.Volume
	out.Biblio.VolumeInt = aToSmallInt(release.Volume)
	out.Biblio.Issue = release.Issue
	out.Biblio.IssueInt = aToSmallInt(release.Issue)
	out.Biblio.Pages = release.Pages
	out.Biblio.Number = release.Number

	out.Biblio.LicenseSlug = release.LicenseSlug
	out.Biblio.Publisher = release.Publisher

	for _, eid := range release.ExternalIDs {
		if eid.Type == "doi" {
			out.Biblio.DOI = eid.Value
			out.Biblio.DOIPrefix = doiPrefix(eid.Value)
			if release.Extra != nil {
				if _, ok := release.Extra["crossref"]; ok {
					// TODO post-xref-poc other registrars
					// TODO is there even evidence that this is useful
					out.Biblio.DOIRegistrar = "crossref"
				}
			}
		}
	}
	// TODO post-xref-poc non-DOI ext ids

	out.Biblio.ContribNames = []string{}
	for _, contrib := range release.Contribs {
		name := contribToName(client, contrib)
		if name != "" {
			out.Biblio.ContribNames = append(out.Biblio.ContribNames, name)
		}

		if contrib.Position > 0 {
			out.Biblio.ContribCount++
		}
	}

	// NB affiliations never set in old code; it's just a TODO

	// biblio metadata "hacks" ported from old system

	// NB ported directly from "biblio_metadata_hacks" function, unsure if really needed
	if slices.Contains([]string{"10.6084", "10.25384"}, out.Biblio.DOIPrefix) {
		out.Biblio.ContainerName = "Figshare"
	} else if out.Biblio.DOIPrefix == "10.5281" {
		out.Biblio.ContainerName = "Zenodo"
	} else if container != nil {
		out.Biblio.ContainerName = container.Name
	}

	// NB ported directly from "biblio_metadata_hacks" function, unsure if really needed
	// TODO this whole stanza seems wrong and like it ought to be handled at an earlier stage
	// biorxiv/medrxiv
	if out.Biblio.DOIPrefix == "10.1101" {
		if out.Biblio.ContainerName == "" {
			out.Biblio.ContainerName = "biorxiv/medrxiv"
		}
		if out.Biblio.ReleaseStage == "" {
			out.Biblio.ReleaseStage = "submitted"
		}
		if out.Biblio.ReleaseType == "post" {
			out.Biblio.ReleaseType = "article"
		}
	}

	// TODO post-xref-poc arxiv_id hack

	// NB ported directly from "biblio_metadata_hacks" function, unsure if really needed
	// IEEE
	if out.Biblio.DOIPrefix == "10.1109" {
		if out.Biblio.ReleaseStage == "" &&
			(strings.Contains(out.Biblio.ContainerName, "IEEE") ||
				strings.Contains(out.Biblio.ContainerName, "Conference") ||
				strings.Contains(out.Biblio.ContainerName, "Proceedings") ||
				out.Biblio.ReleaseType == "paper-conference") {
			out.Biblio.ReleaseStage = "published"
		}
	}

	// NB ported directly from "biblio_metadata_hacks" function, unsure if really needed
	// ACM
	if out.Biblio.DOIPrefix == "10.1145" {
		if out.Biblio.ReleaseStage == "" &&
			(strings.Contains(out.Biblio.ContainerName, "ACM") ||
				strings.Contains(out.Biblio.ContainerName, "Conference") ||
				strings.Contains(out.Biblio.ContainerName, "Proceedings")) {
			out.Biblio.ReleaseStage = "published"
		}
	}

	// NB ported directly from "biblio_metadata_hacks" function, unsure if really needed
	// IOP, ACM, IEEE, AIP, World Scientific (large conference publishers)
	if slices.Contains([]string{"10.1145", "10.1109", "10.1117", "10.1063", "10.1142"},
		out.Biblio.DOIPrefix) {
		if out.Biblio.ReleaseStage == "" && out.Biblio.ReleaseType == "paper-conference" {
			out.Biblio.ReleaseStage = "published"
		}
	}

	// NB ported directly from "biblio_metadata_hacks" function, unsure if really needed
	// F1000
	if out.Biblio.DOIPrefix == "10.3510" {
		if strings.HasPrefix(out.Biblio.Title, "Faculty of 1000 evaluation for") {
			out.Biblio.ReleaseType = "peer_review"
			out.Biblio.ReleaseStage = "published"
		}
	}
	// NB ported directly from "biblio_metadata_hacks" function, unsure if really needed
	// protocols.io
	if out.Biblio.DOIPrefix == "10.17504" {
		if out.Biblio.ReleaseStage == "" {
			out.Biblio.ReleaseStage = "published"
		}
	}

	// fulltext
	out.Fulltext = ScholarFulltextV1{}

	gdoc, err := tei.ParseDocument(bytes.NewReader(ictx.GrobidXML))
	if err == nil {
		out.Fulltext.Language = gdoc.LanguageCode
		out.Fulltext.Body = gdoc.Body
		out.Fulltext.Acknowledgement = gdoc.Acknowledgement
		out.Fulltext.Annex = gdoc.Annex
	} else {
		// TODO logging would be nice
	}

	if out.Fulltext.Language == "" {
		out.Fulltext.Language = release.Language
	}

	if out.Fulltext.Body == "" {
		out.Fulltext.Body = string(ictx.PdfText)
	}

	if len(out.Fulltext.Body) > maxBodySize {
		out.Fulltext.Body = out.Fulltext.Body[:maxBodySize]
	}

	out.Fulltext.LegacyReleaseIdent = fc2.UuidToLegacy(release.ID)
	out.Fulltext.FileSha1 = ictx.File.Sha1
	out.Fulltext.LegacyFileIdent = fc2.UuidToLegacy(ictx.File.ID)
	out.Fulltext.FileMimetype = ictx.File.Mimetype
	out.Fulltext.Size = ictx.File.Size

	// TODO post-xref-poc might be other urls in here but for now there's only going to be one
	out.Fulltext.AccessURL = ictx.File.URLs[0].URL
	out.Fulltext.AccessType = "wayback"

	// abstracts
	unwantedAbstractPrefixes := []string{
		"Abstract No Abstract ",
		"Publisher Summary ",
		"Abstract ",
		"ABSTRACT ",
		"Summary ",
		"Background: ",
		"Background ",
		"N/a.",
		"No abstract.",
		"Introduction: ",
		"ACKNOWLEDGEMENTS ",
		"a b s t r a c t ",
	}

	seenLangs := []string{}
	for _, a := range release.Abstracts {
		if slices.Contains(seenLangs, a.Language) {
			continue
		}
		body := cleanString(deTag(a.Content))
		for _, up := range unwantedAbstractPrefixes {
			body = strings.TrimPrefix(body, up)
		}
		if body == "" || len(strings.Fields(body)) <= 1 {
			continue
		}
		out.Abstracts = append(out.Abstracts, ScholarAbstractV1{
			Body:     body,
			Language: a.Language,
		})
		seenLangs = append(seenLangs, a.Language)
	}

	if len(out.Abstracts) == 0 && len(gdoc.Abstract) > 0 {
		out.Abstracts = append(out.Abstracts, ScholarAbstractV1{
			Language: gdoc.LanguageCode,
			Body:     cleanString(deTag(gdoc.Abstract)),
		})
	}

	// NB there was a concept of excluding fulltext access for certain things
	// (check_exclude_web) but upon closer inspection all it did was hide stuff
	// with a sherpa color of white. the rest was a no-op (ie, it supported a
	// list of containers/publishers to exclude for but those lists are empty).
	// since sherpa stuff is deprecated it seemed pointless to me to port over.

	out.Tags = generateTags(out.Biblio, container)
	// this gets my goat. works only end up with one release in practice but
	// bryan went with a slightly different schema for releases in here. keeping
	// the pattern around because i don't want to break any code that expects
	// this shape of the releases payload.
	out.Releases = []ScholarReleaseV1{
		{
			BiblioCommonV1: out.Biblio.BiblioCommonV1,
			LegacyIdent:    out.Biblio.LegacyReleaseIdent,
			// NB I'm leaving out revision
		},
	}

	// NB I'm leaving out Thumbnail since it's synthesizable from sha1 and other info.

	// NB I hate this whole idea of multiple access points; I also hate how we're
	// using ES as a data store. ES should have a bare minimum of stuff in it --
	// release ID, fulltext. the rest can be gotten from PG.
	out.Access = []ScholarAccessV1{
		{
			Type:               out.Fulltext.AccessType,
			Url:                out.Fulltext.AccessURL,
			Mimetype:           out.Fulltext.FileMimetype,
			LegacyFileIdent:    out.Fulltext.LegacyFileIdent,
			LegacyReleaseIdent: out.Fulltext.LegacyReleaseIdent,
		},
	}

	return out
}

func generateTags(biblio ScholarBiblioV1, container *fc2.Container) []string {
	tags := map[string]bool{}
	if strings.HasPrefix(strings.ToLower(biblio.LicenseSlug), "cc-") {
		tags["oa"] = true
	}

	if biblio.DOIPrefix == "10.2307" || biblio.JstorID != "" {
		tags["jstor"] = true
	}

	if container != nil && container.Extra != nil {
		if _, ok := container.Extra["doaj"]; ok {
			tags["doaj"] = true
			tags["oa"] = true
		}
		if _, ok := container.Extra["road"]; ok {
			tags["road"] = true
			tags["oa"] = true
		}
		if _, ok := container.Extra["szczepanski"]; ok {
			tags["szczepanski"] = true
			if biblio.PublisherType != "big5" {
				tags["oa"] = true
			}
		}
		if ia, ok := container.Extra["ia"]; ok {
			if _, ok := ia.(map[string]any)["longtail_oa"]; ok {
				tags["longtail"] = true
				tags["oa"] = true
			}
		}
		if dl, ok := container.Extra["default_license"]; ok {
			if strings.HasPrefix(strings.ToLower(dl.(string)), "cc-") {
				tags["oa"] = true
			}
		}
		if p, ok := container.Extra["platform"]; ok {
			tags[strings.ToLower(p.(string))] = true
		}
	}

	return slices.Collect(maps.Keys(tags))
}

// deTag takes a string, parses it as HTML, then returns just its rendered text
func deTag(s string) string {
	doc, err := goquery.NewDocumentFromReader(bytes.NewBufferString(s))
	if err != nil {
		// TODO fallback to a naive regex
	}
	return doc.Text()
}

var singleQuotes = []string{"`", "‘", "’", "‛", "⸂", "⸃", "⸌", "⸍", "⸜", "⸝"}

func cleanString(s string) string {
	// i wouldn't be this inefficient in python but shrug
	s = strings.ReplaceAll(s, "…", "...")
	for _, sq := range singleQuotes {
		s = strings.ReplaceAll(s, sq, "'")
	}
	s = strings.ReplaceAll(s, "„", "\"")
	s = strings.ReplaceAll(s, "“", "\"")
	s = strings.ReplaceAll(s, "''", "\"")
	s = strings.ReplaceAll(s, ",,", "\"")

	return s
}

func contribToName(c *http.Client, contrib fc2.ReleaseContrib) string {
	if contrib.CreatorID != uuid.Nil {
		creator, err := fc2.GetCreator(c, contrib.CreatorID)
		if err == nil && creator.DisplayName != "" {
			return creator.DisplayName
		}
	} else if contrib.RawName != "" {
		return contrib.RawName
	} else if contrib.GivenName != "" && contrib.Surname != "" {
		return fmt.Sprintf("%s %s", contrib.GivenName, contrib.Surname)
	} else if contrib.Surname != "" {
		return contrib.Surname
	} else if contrib.GivenName != "" {
		return contrib.GivenName
	}

	return ""
}

func aToSmallInt(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	i, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	if i < 0 {
		return 0
	}

	// just preserving the curious logic from the old system...
	if i > 30000 {
		return 0
	}

	return i
}

func doiPrefix(d string) string {
	out, _, _ := strings.Cut(d, "/")
	return out
}
