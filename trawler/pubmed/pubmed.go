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
	"git.archive.org/webgroup/scholar/trawler/crawling"
	"git.archive.org/webgroup/scholar/trawler/fatcat2"
	"git.archive.org/webgroup/scholar/trawler/harvesting"
	"git.archive.org/webgroup/scholar/trawler/indexing"
	"git.archive.org/webgroup/scholar/trawler/issn"
	"git.archive.org/webgroup/scholar/trawler/orcid"
	"git.archive.org/webgroup/scholar/trawler/s3"
	cdx "git.archive.org/webgroup/scholar/trawler/cdx/cdxclient"
	"git.archive.org/webgroup/scholar/trawler/spn/spnclient"
	"github.com/google/uuid"
	"github.com/internetarchive/scholar/pubmed2json"
	"github.com/spf13/viper"
	"go.temporal.io/sdk/activity"
	"io"
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
	"[in process citation]",
	"[not available]",
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
	title = strings.TrimRight(title, ".")
	title = strings.TrimPrefix(strings.TrimSuffix(title, "]"), "[")
	title = strings.TrimSpace(title)

	if title == "" {
		// check vernacular title as fallback before skipping
		vt := cleaning.CleanString(string(mc.Article.VernacularTitle))
		if vt == "" {
			return "empty-title"
		}
	}

	if slices.Contains(stubTitles, strings.ToLower(strings.TrimSpace(title))) {
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
func pubmedToFc(client *http.Client, article pubmed2json.PubmedArticle, source string) (fatcat2.Release, error) {
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
	if strings.HasPrefix(title, "[") && strings.HasSuffix(title, "]") {
		title = title[1 : len(title)-1]
	}

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
				Role:    "author",
				Extra:   map[string]any{},
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

	return release, nil
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

func ProcessLine(ctx context.Context, in harvesting.ProcessLineInput) (out counts.Counts, err error) {
	out = counts.Counts{}
	l := activity.GetLogger(ctx)

	lineb, err := harvesting.GetLine(ctx, in.S3Key, in.LineStart, in.Length)
	if err != nil {
		return out, fmt.Errorf("failed to read ndjson line from s3: %w", err)
	}

	var rec pubmed2json.Record
	if err := json.Unmarshal(lineb, &rec); err != nil {
		return out, fmt.Errorf("pubmed unmarshal: %w", err)
	}

	switch rec.Type {
	case "article":
		if rec.Article == nil {
			l.Warn("pubmed record type is article but article field is nil")
			out.Releases.Skipped++
			return out, nil
		}

		article := *rec.Article
		pmid := article.MedlineCitation.PMID.Value

		if reason := skipReason(article); reason != "" {
			l.Info("pubmed: skipping article", "pmid", pmid, "reason", reason)
			out.Releases.Skipped++
			return out, nil
		}

		client := &http.Client{}

		release, err := pubmedToFc(client, article, in.Source)
		if err != nil {
			return out, fmt.Errorf("pubmed->fc2 transform failed for pmid '%s': %w", pmid, err)
		}

		// Lookup by PMID first, then DOI as fallback
		foundID, err := fatcat2.LookupPmid(client, pmid)
		if err != nil {
			return out, fmt.Errorf("pmid lookup failed for '%s': %w", pmid, err)
		}

		if foundID == nil && release.DOI() != "" {
			foundID, err = fatcat2.LookupDoi(client, release.DOI())
			if err != nil {
				return out, fmt.Errorf("doi lookup failed for '%s': %w", release.DOI(), err)
			}
		}

		if foundID != nil {
			l.Debug("pubmed: found existing release", "pmid", pmid, "id", foundID)
			release, err = fatcat2.GetRelease(client, *foundID)
			if err != nil {
				return out, fmt.Errorf("could not fetch existing release: %w", err)
			}
			out.Releases.Ignored++
		} else {
			r, err := createRelease(client, &out, release, article)
			if err != nil {
				return out, fmt.Errorf("could not create release for pmid '%s': %w", pmid, err)
			}
			release = *r
			l.Debug("pubmed: created release", "pmid", pmid, "id", release.ID)
			out.Releases.Added++
		}

		if !release.IsPaperlike() {
			l.Info("pubmed: skipping crawl, not paperlike", "pmid", pmid, "type", release.Type)
			return out, nil
		}

		urls := release.FulltextURLs()
		if len(urls) == 0 {
			l.Info("pubmed: skipping crawl, no fulltext URLs", "pmid", pmid)
			return out, nil
		}

		out.Releases.CrawlWanted++

		spnClient, err := spnclient.NewDefaultClient(spnclient.SPNConfig{
			AccessKey: viper.GetString("spn.access_key"),
			SecretKey: viper.GetString("spn.secret_key"),
			Endpoint:  viper.GetString("spn.endpoint"),
			Debug:     true,
		})
		if err != nil {
			return out, fmt.Errorf("spn client creation failed: %w", err)
		}

		cdxClient := cdx.NewClient(cdx.Config{
			Auth:      viper.GetString("cdx.auth"),
			Endpoint:  viper.GetString("cdx.endpoint"),
			UserAgent: viper.GetString("cdx.user_agent"),
			Retries:   viper.GetInt("cdx.retries"),
			Backoff:   viper.GetDuration("cdx.backoff"),
			Debug:     true,
		})

		var res crawling.CrawlResult

		for _, u := range urls {
			crawler := crawling.PDFCrawler{
				SPNClient:       spnClient,
				CDXClient:       cdxClient,
				MaxHops:         8,
				UserAgent:       viper.GetString("crawling.user_agent"),
				WaybackEndpoint: viper.GetString("wayback.replay_endpoint"),
				SimpleGets:      viper.GetStringSlice("crawling.simple_get_list"),
				Blocklist:       viper.GetStringSlice("crawling.url_blocklist"),
				Logger:          slog.Default(),
			}

			res, err = crawler.Crawl(u)
			if err != nil {
				l.Info("pubmed: crawl failed", "pmid", pmid, "url", u, "err", err)
				continue
			}
			if res.Success {
				break
			}
		}

		if err != nil || !res.Success {
			return out, nil
		}

		mimetype, _, _ := strings.Cut(res.Mimetype, ";")

		fid := uuid.New()
		file := fatcat2.File{
			ID:       fid,
			Releases: []fatcat2.Release{release},
			Mimetype: mimetype,
			Source:   release.Source,
			URLs: []fatcat2.FileURL{
				{
					Rel:    "wayback",
					URL:    res.SnapshotUrl,
					FileID: fid,
				},
			},
		}

		pdfBs, err := io.ReadAll(res.Content)
		if err != nil {
			return out, fmt.Errorf("could not read pdf bytes: %w", err)
		}

		if err = file.SetMetadata(pdfBs); err != nil {
			return out, err
		}

		fileID, err := fatcat2.LookupSha256(client, file.Sha256)
		if err != nil {
			return out, fmt.Errorf("sha256 lookup failed: %w", err)
		}

		if fileID != nil {
			l.Debug("pubmed: ignoring known file", "sha256", file.Sha256, "pmid", pmid)
			return out, nil
		}

		_, err = fatcat2.CreateFile(client, &file)
		if err != nil {
			return out, fmt.Errorf("file creation failed: %w", err)
		}
		out.Releases.Acquired++

		fileDoc := indexing.PrepareFatcatFileDoc(file)
		bs, err := json.Marshal(fileDoc)
		if err != nil {
			return out, fmt.Errorf("failed to marshal file ES doc: %w", err)
		}
		err = indexing.DoElasticIndex(client,
			viper.GetString("indexing.fatcat_file_ix"), fileDoc.LegacyIdent, bs)
		if err != nil {
			return out, fmt.Errorf("failed to index file: %w", err)
		}

		blobprocEndpoint := viper.GetString("blobproc.endpoint")
		req, err := http.NewRequest("POST", blobprocEndpoint+"/spool",
			strings.NewReader(string(pdfBs)))
		if err != nil {
			return out, fmt.Errorf("could not form blobproc request: %w", err)
		}
		req.Header.Set("Content-Type", "application/pdf")

		resp, err := client.Do(req)
		if err != nil {
			return out, fmt.Errorf("blobproc request error: %w", err)
		}
		if resp.StatusCode != 202 {
			return out, fmt.Errorf("unexpected status from blobproc '%d'", resp.StatusCode)
		}

		loc := resp.Header.Get("Location")
		if loc == "" {
			return out, fmt.Errorf("got blank spool url from blobproc")
		}

		pollUrl := blobprocEndpoint + loc
		if !strings.Contains(pollUrl, file.Sha1) {
			return out, fmt.Errorf("expected sha1 '%s' in spool url '%s'", file.Sha1, pollUrl)
		}

		pollReq, err := http.NewRequest("GET", pollUrl, nil)
		if err != nil {
			return out, fmt.Errorf("could not form blobproc poll request: %w", err)
		}

		for {
			time.Sleep(viper.GetDuration("blobproc.poll_interval"))
			resp, err = client.Do(pollReq)
			if err != nil {
				return out, fmt.Errorf("error polling blobproc: %w", err)
			}
			if resp.StatusCode == 404 {
				break
			}
			l.Debug("pubmed: waiting on blobproc", "pmid", pmid)
		}

		s3bucket := viper.GetString("blobproc.s3bucket")

		grobidS3Key := fmt.Sprintf("%s/%s/%s/%s/%s.tei.xml",
			s3bucket, "grobid", file.Sha1[0:2], file.Sha1[2:4], file.Sha1)

		obj, err := s3.GetObject(ctx, grobidS3Key)
		if err != nil {
			return out, fmt.Errorf("blobproc grobid s3 read failed: %w", err)
		}
		grobidXML, err := io.ReadAll(obj)
		if err != nil {
			return out, fmt.Errorf("could not read grobid output: %w", err)
		}

		pdftotextS3Key := fmt.Sprintf("%s/%s/%s/%s/%s.txt",
			s3bucket, "text", file.Sha1[0:2], file.Sha1[2:4], file.Sha1)

		obj, err = s3.GetObject(ctx, pdftotextS3Key)
		if err != nil {
			return out, fmt.Errorf("blobproc pdftotext s3 read failed: %w", err)
		}
		pdfText, err := io.ReadAll(obj)
		if err != nil {
			return out, fmt.Errorf("could not read pdftotext output: %w", err)
		}

		var container *fatcat2.Container
		if release.ContainerID != nil {
			c, err := fatcat2.GetContainer(client, *release.ContainerID)
			if err != nil {
				return out, fmt.Errorf("could not fetch container: %w", err)
			}
			container = &c
		}

		esDoc := indexing.PrepareFulltextDoc(indexing.FulltextTransformCtx{
			HttpClient: client,
			Release:    release,
			File:       &file,
			PdfText:    pdfText,
			GrobidXML:  grobidXML,
			Container:  container,
		})

		bs, err = json.Marshal(esDoc)
		if err != nil {
			return out, fmt.Errorf("marshaling fulltext doc failed: %w", err)
		}

		err = indexing.DoElasticIndex(client,
			viper.GetString("indexing.fulltext_ix"), esDoc.Key, bs)
		if err != nil {
			return out, fmt.Errorf("indexing fulltext failed: %w", err)
		}

		out.Releases.Ingested++

	case "deleteCitation":
		if rec.DeleteCitation == nil {
			l.Warn("pubmed record type is deleteCitation but deleteCitation field is nil")
			out.Releases.Skipped++
			return out, nil
		}
		pmids := make([]string, 0, len(rec.DeleteCitation.PMIDs))
		for _, p := range rec.DeleteCitation.PMIDs {
			pmids = append(pmids, p.Value)
		}
		l.Info("pubmed: deleteCitation (no-op)", "count", len(pmids), "pmids", pmids)

	default:
		return out, fmt.Errorf("pubmed: unknown record type: %q", rec.Type)
	}

	return out, nil
}
