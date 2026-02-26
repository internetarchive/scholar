package pubmed

import (
	"context"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"log/slog"
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
	"github.com/internetarchive/scholar/pubmed2json"
	"github.com/spf13/viper"
	"go.temporal.io/sdk/activity"
)

const minAbstractLength = 75

// pubmedReleaseTypeMap maps PubMed PublicationType strings to Fatcat release types.
// Types not in this map result in a nil release type (unknown).
// Special cases (retraction, withdrawn, correction) are handled separately.
var pubmedReleaseTypeMap = map[string]string{
	"Journal Article":              "article-journal",
	"Classical Article":            "article-journal",
	"Historical Article":           "article-journal",
	"Introductory Journal Article": "article-journal",
	"Newspaper Article":            "article-newspaper",
	"Editorial":                    "editorial",
	"Letter":                       "letter",
	"Dataset":                      "dataset",
	"Technical Report":             "report",
	"Interview":                    "interview",
	"Lecture":                      "speech",
	"Address":                      "speech",
	"Autobiography":                "book",
	"Biography":                    "book",
	"Legal Case":                   "legal_case",
	"Legislation":                  "legislation",
}

// marcLangMap maps MARC 3-letter language codes to ISO 639-1 2-letter codes.
var marcLangMap = map[string]string{
	"eng": "en",
	"fre": "fr",
	"ger": "de",
	"spa": "es",
	"ita": "it",
	"por": "pt",
	"rus": "ru",
	"jpn": "ja",
	"chi": "zh",
	"kor": "ko",
	"dut": "nl",
	"pol": "pl",
	"swe": "sv",
	"dan": "da",
	"nor": "no",
	"fin": "fi",
	"ara": "ar",
	"heb": "he",
	"tur": "tr",
	"cze": "cs",
	"hun": "hu",
	"rum": "ro",
	"gre": "el",
	"ukr": "uk",
	"hrv": "hr",
	"slv": "sl",
	"bul": "bg",
	"cat": "ca",
	"vie": "vi",
	"ind": "id",
	"per": "fa",
	"hin": "hi",
	"lat": "la",
	"wel": "cy",
	"geo": "ka",
	"afr": "af",
	"slo": "sk",
	"lit": "lt",
	"lav": "lv",
	"est": "et",
	"alb": "sq",
	"ice": "is",
	"mac": "mk",
	"ser": "sr",
	"bos": "bs",
	"mon": "mn",
	"ben": "bn",
	"tam": "ta",
	"glg": "gl",
	"scr": "hr", // deprecated MARC code for Croatian
	"scc": "sr", // deprecated MARC code for Serbian
}

// containerTypeMap maps Fatcat release types to their parent container type.
var containerTypeMap = map[string]string{
	"article-journal":  "journal",
	"paper-conference": "conference",
	"book":             "book-series",
}

// stubTitles are titles that indicate a placeholder record, not a real article.
var stubTitles = []string{
	"in process citation",
	"not available",
	"oup accepted manuscript",
}

// skipReason returns a non-empty string describing why this article should be
// skipped, or empty string if it should be processed.
func skipReason(article pubmed2json.PubmedArticle) string {
	mc := article.MedlineCitation

	if mc.PMID.Value == "" {
		return "no-pmid"
	}

	title := cleaning.CleanString(string(mc.Article.ArticleTitle))
	if title == "" {
		title = cleaning.CleanString(string(mc.Article.VernacularTitle))
		if title == "" {
			return "empty-title"
		}
	}
	// curious logic from original system
	title = strings.TrimRight(title, ".")
	title = strings.TrimPrefix(strings.TrimSuffix(title, "]"), "[")
	title = strings.TrimSpace(title)

	if slices.Contains(stubTitles, strings.ToLower(title)) {
		return "stub-title"
	}

	if mc.Article.AuthorList != nil && len(mc.Article.AuthorList.Authors) > 2000 {
		return "too-many-authors"
	}

	if article.PubmedData != nil && len(article.PubmedData.ReferenceList) > 5000 {
		return "too-many-refs"
	}

	return ""
}

// parsePubDate converts a pubmed2json.PubDate into a year int and optional
// ISO date string. Returns (0, "") if the date cannot be parsed.
func parsePubDate(pd pubmed2json.PubDate) (year int, isoDate string) {
	if pd.Year != "" {
		y, err := strconv.Atoi(pd.Year)
		if err != nil || y < 1300 || y > time.Now().Year()+5 {
			slog.Warn("pubmed: invalid year in PubDate", "year", pd.Year)
			return 0, ""
		}
		year = y
		if pd.Month != "" && pd.Day != "" {
			monthAbbr := map[string]string{
				"Jan": "01", "Feb": "02", "Mar": "03", "Apr": "04",
				"May": "05", "Jun": "06", "Jul": "07", "Aug": "08",
				"Sep": "09", "Oct": "10", "Nov": "11", "Dec": "12",
			}
			m := pd.Month
			if mapped, ok := monthAbbr[m]; ok {
				m = mapped
			}
			d, err := time.Parse("2006-01-02", fmt.Sprintf("%d-%s-%02s", y, m, pd.Day))
			if err != nil {
				slog.Warn("pubmed: could not parse full date", "year", pd.Year, "month", pd.Month, "day", pd.Day)
				return year, ""
			}
			isoDate = d.Format("2006-01-02")
		}
		return year, isoDate
	}

	if pd.MedlineDate != "" {
		s := strings.TrimSpace(pd.MedlineDate)
		if len(s) >= 4 && func() bool {
			_, err := strconv.Atoi(s[:4])
			return err == nil
		}() {
			y, _ := strconv.Atoi(s[:4])
			if y < 1300 || y > time.Now().Year()+5 {
				slog.Warn("pubmed: out-of-range year in MedlineDate", "medlineDate", s)
				return 0, ""
			}
			return y, ""
		}
		slog.Warn("pubmed: unparsable MedlineDate", "medlineDate", s)
	}

	return 0, ""
}

// pubmedToFc transforms a pubmed2json.PubmedArticle into a fatcat2.Release.
func pubmedToFc(client *http.Client, article pubmed2json.PubmedArticle, source string) (*fatcat2.Release, error) {
	mc := article.MedlineCitation
	art := mc.Article

	release := fatcat2.Release{
		Contribs:    []fatcat2.ReleaseContrib{},
		ExternalIDs: []fatcat2.ExternalID{},
		Extra:       map[string]any{},
		Source:      source,
	}

	// --- Identifiers ---

	pmid := strings.TrimSpace(mc.PMID.Value)
	release.ExternalIDs = append(release.ExternalIDs, fatcat2.ExternalID{
		Type:  "pmid",
		Value: pmid,
	})

	var doi, pmcid string
	if article.PubmedData != nil {
		for _, aid := range article.PubmedData.ArticleIds {
			switch aid.IdType {
			case "doi":
				doi = strings.ToLower(strings.TrimSpace(aid.Value))
			case "pmc":
				pmcid = strings.ToUpper(strings.TrimSpace(aid.Value))
			}
		}
	}

	// Fallback: check ELocationIDs for DOI
	if doi == "" {
		for _, eloc := range art.ELocationIDs {
			if eloc.EIdType == "doi" && eloc.ValidYN != "N" {
				doi = strings.ToLower(strings.TrimSpace(eloc.Value))
				break
			}
		}
	}

	if doi != "" {
		release.ExternalIDs = append(release.ExternalIDs, fatcat2.ExternalID{
			Type:  "doi",
			Value: doi,
		})
	}
	if pmcid != "" {
		release.ExternalIDs = append(release.ExternalIDs, fatcat2.ExternalID{
			Type:  "pmcid",
			Value: pmcid,
		})
	}

	// --- Publication type and stage ---

	pubTypeValues := make([]string, 0, len(art.PublicationTypes))
	for _, pt := range art.PublicationTypes {
		pubTypeValues = append(pubTypeValues, pt.Value)
	}

	// First match in the map wins for release type
	for _, ptv := range pubTypeValues {
		if rt, ok := pubmedReleaseTypeMap[ptv]; ok {
			release.Type = rt
			break
		}
	}

	// Default stage: all MEDLINE content is published
	release.Stage = "published"

	extraPubmed := map[string]any{}
	if len(pubTypeValues) > 0 {
		extraPubmed["pub_types"] = pubTypeValues
	}

	if slices.Contains(pubTypeValues, "Corrected and Republished Article") {
		release.Stage = "updated"
	}

	if slices.Contains(pubTypeValues, "Retraction of Publication") {
		release.Type = "retraction"
		release.Stage = "retraction"
		for _, cc := range mc.CommentsCorrectionsList {
			if cc.RefType == "RetractionOf" {
				if cc.RefSource != "" {
					extraPubmed["retraction_of_raw"] = cc.RefSource
				}
				if cc.PMID != nil {
					extraPubmed["retraction_of_pmid"] = cc.PMID.Value
				}
				break
			}
		}
	}

	if slices.Contains(pubTypeValues, "Retracted Publication") {
		release.WithdrawnStatus = "retracted"
	} else {
		for _, cc := range mc.CommentsCorrectionsList {
			if cc.RefType == "ExpressionOfConcernIn" {
				release.WithdrawnStatus = "concern"
				break
			}
		}
	}

	// --- Title ---

	title := cleaning.CleanString(string(art.ArticleTitle))
	title = strings.TrimRight(title, ".")
	title = strings.TrimSpace(title)
	title = strings.TrimPrefix(strings.TrimSuffix(title, "]"), "[")

	originalTitle := cleaning.CleanString(string(art.VernacularTitle))
	originalTitle = strings.TrimRight(originalTitle, ".")
	originalTitle = strings.TrimSpace(originalTitle)

	if title == "" && originalTitle != "" {
		title = originalTitle
		originalTitle = ""
	}

	release.Title = title
	release.OriginalTitle = originalTitle

	// --- Language ---

	if len(art.Language) > 0 {
		marc := art.Language[0]
		if marc != "und" && marc != "un" {
			if iso, ok := marcLangMap[marc]; ok {
				release.Language = iso
			} else {
				slog.Warn("pubmed: unknown MARC language code", "code", marc, "pmid", pmid)
			}
		}
	}

	// --- Volume / Issue / Pages ---

	release.Volume = art.Journal.JournalIssue.Volume
	release.Issue = art.Journal.JournalIssue.Issue
	if art.Pagination != nil {
		release.Pages = art.Pagination.MedlinePgn
	}

	// --- Date ---

	var releaseYear int
	var releaseDateStr string

	// Prefer ArticleDate (electronic pub date)
	if len(art.ArticleDates) > 0 {
		ad := art.ArticleDates[0]
		pd := pubmed2json.PubDate{
			Year:  ad.Year,
			Month: ad.Month,
			Day:   ad.Day,
		}
		releaseYear, releaseDateStr = parsePubDate(pd)
	}

	// Fallback to JournalIssue PubDate
	if releaseYear == 0 {
		releaseYear, releaseDateStr = parsePubDate(art.Journal.JournalIssue.PubDate)
	}

	release.ReleaseYear = releaseYear
	if releaseDateStr != "" {
		d, err := time.Parse("2006-01-02", releaseDateStr)
		if err == nil {
			rd := fatcat2.ReleaseDate(d)
			release.ReleaseDate = &rd
		}
	}

	// --- Abstracts ---

	release.Abstracts = []fatcat2.Abstract{}

	if art.Abstract != nil && len(art.Abstract.Texts) > 0 {
		var content string
		isStructured := art.Abstract.Texts[0].NlmCategory != ""

		if isStructured {
			parts := make([]string, 0, len(art.Abstract.Texts))
			for _, at := range art.Abstract.Texts {
				if at.Text != "" {
					parts = append(parts, at.Text)
				}
			}
			content = strings.Join(parts, "\n")
			if len(content) >= minAbstractLength {
				h := sha1.Sum([]byte(content))
				release.Abstracts = append(release.Abstracts, fatcat2.Abstract{
					Content:  content,
					MIMEType: "text/plain",
					Language: "en",
					SHA1:     fmt.Sprintf("%x", h),
				})
			}
		} else {
			for _, at := range art.Abstract.Texts {
				if len(at.Text) < minAbstractLength {
					continue
				}
				h := sha1.Sum([]byte(at.Text))
				release.Abstracts = append(release.Abstracts, fatcat2.Abstract{
					Content:  at.Text,
					MIMEType: "text/plain",
					Language: "en",
					SHA1:     fmt.Sprintf("%x", h),
				})
			}
		}
	}

	for _, oa := range mc.OtherAbstracts {
		lang := "en"
		if oa.Language != "" {
			if iso, ok := marcLangMap[oa.Language]; ok {
				lang = iso
			}
		}
		for _, at := range oa.Texts {
			if len(at.Text) < minAbstractLength {
				continue
			}
			h := sha1.Sum([]byte(at.Text))
			release.Abstracts = append(release.Abstracts, fatcat2.Abstract{
				Content:  at.Text,
				MIMEType: "text/plain",
				Language: lang,
				SHA1:     fmt.Sprintf("%x", h),
			})
		}
	}

	// --- Contribs ---

	release.Contribs = []fatcat2.ReleaseContrib{}

	if art.AuthorList != nil {
		position := 0
		for _, author := range art.AuthorList.Authors {
			contrib := fatcat2.ReleaseContrib{
				Role:  "author",
				Extra: map[string]any{},
			}

			contrib.Surname = author.LastName
			contrib.GivenName = author.ForeName

			switch {
			case author.ForeName != "" && author.LastName != "":
				contrib.RawName = fmt.Sprintf("%s %s", author.ForeName, author.LastName)
			case author.LastName != "":
				contrib.RawName = author.LastName
			case author.CollectiveName != "":
				contrib.RawName = author.CollectiveName
			}

			// ORCID
			for _, id := range author.Identifiers {
				if id.Source == "ORCID" {
					orcidVal := orcid.Normalize(id.Value)
					contrib.Extra["orcid"] = orcidVal
					creatorID, err := fatcat2.LookupOrcid(client, orcidVal)
					if err != nil {
						slog.Warn("pubmed: orcid lookup failed", "orcid", orcidVal, "pmid", pmid, "err", err)
					} else {
						contrib.CreatorID = creatorID
					}
					break
				}
			}

			// Affiliations
			if len(author.AffiliationInfo) > 0 {
				contrib.RawAffiliation = author.AffiliationInfo[0].Affiliation
				if len(author.AffiliationInfo) > 1 {
					more := make([]string, 0, len(author.AffiliationInfo)-1)
					for _, ai := range author.AffiliationInfo[1:] {
						more = append(more, ai.Affiliation)
					}
					contrib.Extra["more_affiliations"] = more
				}
			}

			contrib.Position = position
			position++
			release.Contribs = append(release.Contribs, contrib)
		}

		if art.AuthorList.CompleteYN == "N" {
			release.Contribs = append(release.Contribs, fatcat2.ReleaseContrib{
				RawName: "et al.",
				Role:    "author",
			})
		}
	}

	// --- References ---

	release.Refs = []fatcat2.RawRef{}

	if article.PubmedData != nil {
		for i, ref := range article.PubmedData.ReferenceList {
			rawRef := fatcat2.RawRef{
				Index: i,
				Extra: map[string]any{},
			}
			if ref.Citation != "" {
				rawRef.Extra["unstructured"] = ref.Citation
			}
			for _, aid := range ref.ArticleIds {
				switch aid.IdType {
				case "doi":
					rawRef.Extra["doi"] = strings.ToLower(strings.TrimSpace(aid.Value))
				case "pubmed":
					rawRef.Extra["pmid"] = strings.TrimSpace(aid.Value)
				}
			}
			release.Refs = append(release.Refs, rawRef)
		}
	}

	// --- Extra ---

	if len(extraPubmed) > 0 {
		release.Extra["pubmed"] = extraPubmed
	}

	return &release, nil
}

// createRelease handles container lookup/creation, creates the release in
// fatcat2, and indexes it to Elasticsearch.
func createRelease(client *http.Client, cs *counts.Counts, release fatcat2.Release, article pubmed2json.PubmedArticle) (*fatcat2.Release, error) {
	mc := article.MedlineCitation
	art := mc.Article

	// --- Container ---

	// Prefer ISSNLinking; fallback to Journal ISSN
	var issnRaw string
	if mc.MedlineJournalInfo.ISSNLinking != "" {
		issnRaw = mc.MedlineJournalInfo.ISSNLinking
	} else if art.Journal.ISSN != nil {
		issnRaw = art.Journal.ISSN.Value
	}

	var issnl string
	if issnRaw != "" {
		issnl = issn.ISSN2ISSNL(issnRaw)
	}

	containerTitle := cleaning.CleanString(art.Journal.Title)

	var containerID *uuid.UUID
	var err error

	if issnl != "" {
		containerID, err = fatcat2.LookupIssnl(client, issnl)
		if err != nil {
			return nil, fmt.Errorf("container issnl lookup failed: %w", err)
		}
	}

	if containerID == nil && containerTitle != "" && issnl != "" {
		containerExtra := map[string]any{}
		if mc.MedlineJournalInfo.Country != "" {
			containerExtra["country_name"] = mc.MedlineJournalInfo.Country
		}

		var issnp string
		if art.Journal.ISSN != nil && art.Journal.ISSN.IssnType == "Print" {
			issnp = art.Journal.ISSN.Value
		}

		c := fatcat2.Container{
			Name:   containerTitle,
			ISSNL:  issnl,
			ISSNP:  issnp,
			Type:   containerTypeMap[release.Type],
			Source: release.Source,
			Extra:  containerExtra,
		}

		containerID, err = fatcat2.CreateContainer(client, &c)
		if err != nil {
			return nil, fmt.Errorf("container creation failed: %w", err)
		}
		cs.Containers.Added++

		containerDoc := indexing.PrepareFatcatContainerDoc(c)
		bs, err := json.Marshal(containerDoc)
		if err != nil {
			return nil, fmt.Errorf("could not marshal container doc: %w", err)
		}
		err = indexing.DoElasticIndex(client, viper.GetString("indexing.fatcat_container_ix"),
			containerDoc.LegacyIdent, bs)
		if err != nil {
			return nil, fmt.Errorf("could not index container: %w", err)
		}
	} else if containerID != nil {
		cs.Containers.Ignored++
	} else if containerTitle != "" {
		release.Extra["container_name"] = containerTitle
		cs.Containers.Skipped++
	}

	release.ContainerID = containerID

	id, err := fatcat2.CreateRelease(client, release)
	if err != nil {
		return nil, fmt.Errorf("release creation failed for pmid '%s': %w", release.ExternalIDs[0].Value, err)
	}

	r, err := fatcat2.GetRelease(client, *id)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch new release '%s': %w", id, err)
	}

	releaseDoc, err := indexing.PrepareFatcatReleaseDoc(client, r)
	if err != nil {
		return nil, fmt.Errorf("failed to transform release into ES doc: %w", err)
	}

	bs, err := json.Marshal(releaseDoc)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal release ES doc: %w", err)
	}

	err = indexing.DoElasticIndex(client,
		viper.GetString("indexing.fatcat_release_ix"), releaseDoc.LegacyIdent, bs)
	if err != nil {
		return nil, fmt.Errorf("failed to index release: %w", err)
	}

	return &r, nil
}

func ProcessLine(ctx context.Context, client *http.Client, source string, lineb []byte) (counts.Counts, *fatcat2.Release, error) {
	out := counts.Counts{}
	var release *fatcat2.Release
	var err error
	l := activity.GetLogger(ctx)

	var rec pubmed2json.Record
	if err = json.Unmarshal(lineb, &rec); err != nil {
		return out, release, fmt.Errorf("pubmed unmarshal: %w", err)
	}

	if rec.Type == "deleteCitation" {
		if rec.DeleteCitation == nil {
			l.Warn("pubmed record type is deleteCitation but deleteCitation field is nil")
			out.Releases.Skipped++
			return out, release, nil
		}
		pmids := make([]string, 0, len(rec.DeleteCitation.PMIDs))
		for _, p := range rec.DeleteCitation.PMIDs {
			pmids = append(pmids, p.Value)
		}
		l.Info("pubmed: deleteCitation (no-op)", "count", len(pmids), "pmids", pmids)
		return out, release, nil
	}

	if rec.Type != "article" {
		return out, release, fmt.Errorf("pubmed: unknown record type: %q", rec.Type)
	}

	if rec.Article == nil {
		l.Warn("pubmed record type is article but article field is nil")
		out.Releases.Skipped++
		return out, release, nil
	}

	article := *rec.Article
	pmid := article.MedlineCitation.PMID.Value

	if reason := skipReason(article); reason != "" {
		l.Info("pubmed: skipping article", "pmid", pmid, "reason", reason)
		out.Releases.Skipped++
		return out, release, nil
	}

	release, err = pubmedToFc(client, article, source)
	if err != nil {
		return out, release, fmt.Errorf("pubmed->fc2 transform failed for pmid '%s': %w", pmid, err)
	}

	// Lookup by PMID first, then DOI as fallback
	foundID, err := fatcat2.LookupPmid(client, pmid)
	if err != nil {
		return out, release, fmt.Errorf("pmid lookup failed for '%s': %w", pmid, err)
	}

	if foundID == nil && release.DOI() != "" {
		foundID, err = fatcat2.LookupDoi(client, release.DOI())
		if err != nil {
			return out, release, fmt.Errorf("doi lookup failed for '%s': %w", release.DOI(), err)
		}
	}

	if foundID != nil {
		l.Debug("pubmed: found existing release", "pmid", pmid, "id", foundID)
		r, err := fatcat2.GetRelease(client, *foundID)
		if err != nil {
			return out, release, fmt.Errorf("could not fetch existing release: %w", err)
		}
		release = &r
		out.Releases.Ignored++
	} else {
		r, err := createRelease(client, &out, *release, article)
		if err != nil {
			return out, release, fmt.Errorf("could not create release for pmid '%s': %w", pmid, err)
		}
		release = r
		l.Debug("pubmed: created release", "pmid", pmid, "id", release.ID)
		out.Releases.Added++
	}

	return out, release, nil
}
