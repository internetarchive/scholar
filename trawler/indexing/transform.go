package indexing

import (
	"bytes"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"git.archive.org/webgroup/scholar/trawler/fatcat2"
	"github.com/PuerkitoBio/goquery"
	"github.com/google/uuid"
	"github.com/miku/grobidclient/tei"
)

const maxBodySize = 512 * 1024

// TODO i am once again worked up about multiple releases in works. are we
// currently adding releases to works when we find new versions? how do we
// determine new versions? or was that only through manual edits? the pipeline
// i'm augmenting with indexing right now is just adding new releases and thus
// new works so for now I can treat work <-> release as one to one. but i'd
// feel a lot better if i knew how many works in fatcat had >1 release (1 and
// 2) so i'm going to finish that line of inquiry. my notes say i asked this
// question back in january but i don't see an answer anywhere with cursory
// grepping.
//
// but, what i need to keep repeating to myself is that this is a code path for
// a PoC i want done asap and this code path is *just* for new to us DOIs so i
// should continue in that mindset

type IngestCtx struct {
	HttpClient *http.Client
	Release    fatcat2.Release
	File       fatcat2.File
	Container  *fatcat2.Container
	GrobidXML  []byte
	PdfText    []byte
	// TODO thumbnail?
}

func PrepareElasticDoc(client *http.Client, ictx IngestCtx) FulltextDocV1 {
	out := FulltextDocV1{}
	release := ictx.Release
	container := ictx.Container

	out.Type = "work"
	out.LegacyWorkIdent = fatcat2.UuidToLegacy(release.WorkID)
	out.Key = fmt.Sprintf("work_%s", out.LegacyWorkIdent)
	out.IndexTime = time.Now()
	out.CollapseKey = out.LegacyWorkIdent

	// biblio field
	out.Biblio = BiblioV1{}
	if release.Publisher != "" {
		out.Biblio.Publisher = release.Publisher
	} else {
		out.Biblio.Publisher = container.Publisher
	}

	out.Biblio.LegacyContainerIdent = fatcat2.UuidToLegacy(container.ID)
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
			if color, ok := sherpaRomeo.(map[string]string)["color"]; ok {
				out.Biblio.ContainerSherpaColor = color
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

	out.Biblio.LegacyReleaseIdent = fatcat2.UuidToLegacy(release.ID)

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
	out.Biblio.ReleaseDate = release.ReleaseDate
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
	} else {
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
	out.Fulltext = FulltextV1{}

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

	out.Fulltext.LegacyReleaseIdent = fatcat2.UuidToLegacy(release.ID)
	out.Fulltext.FileSha1 = ictx.File.Sha1
	out.Fulltext.LegacyFileIdent = fatcat2.UuidToLegacy(ictx.File.ID)
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
		out.Abstracts = append(out.Abstracts, AbstractV1{
			Body:     body,
			Language: a.Language,
		})
		seenLangs = append(seenLangs, a.Language)
	}

	if len(out.Abstracts) == 0 && len(gdoc.Abstract) > 0 {
		out.Abstracts = append(out.Abstracts, AbstractV1{
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
	out.Releases = []ReleaseV1{
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
	out.Access = []AccessV1{
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

func generateTags(biblio BiblioV1, container *fatcat2.Container) []string {
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

func contribToName(c *http.Client, contrib fatcat2.ReleaseContrib) string {
	if contrib.CreatorID != uuid.Nil {
		creator, err := fatcat2.GetCreator(c, contrib.CreatorID)
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
