package arxiv

import (
	"context"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"io"

	cdx "git.archive.org/webgroup/scholar/trawler/cdx/cdxclient"
	"git.archive.org/webgroup/scholar/trawler/cleaning"
	"git.archive.org/webgroup/scholar/trawler/counts"
	"git.archive.org/webgroup/scholar/trawler/crawling"
	"git.archive.org/webgroup/scholar/trawler/fatcat2"
	"git.archive.org/webgroup/scholar/trawler/indexing"
	"git.archive.org/webgroup/scholar/trawler/pdf"
	"git.archive.org/webgroup/scholar/trawler/spn/spnclient"
	"github.com/google/uuid"
	"github.com/spf13/viper"
	"go.temporal.io/sdk/activity"
)

const minAbstractLength = 75
const maxAuthors = 2000

// arxivRecord is the deserializable flat representation of an arXiv OAI-PMH
// record as written by scholkit's ArxivHarvester. Each ndjson line is one
// arxivRecord.
type arxivRecord struct {
	Identifier string        `json:"identifier"` // "oai:arXiv.org:2301.12345"
	Status     string        `json:"status"`     // "deleted" or ""
	Datestamp  string        `json:"datestamp"`  // "2023-01-15"
	SetSpec    []string      `json:"set_spec"`
	ID         string        `json:"id"` // "2301.12345" (no version suffix)
	Title      string        `json:"title"`
	Authors    []arxivAuthor `json:"authors"`
	Categories string        `json:"categories"` // space-separated "cs.AI cs.LG"
	Abstract   string        `json:"abstract"`
	DOI        string        `json:"doi"`
	Created    string        `json:"created"` // "2007-04-02"
	Updated    string        `json:"updated"`
	Comments   string        `json:"comments"`
	License    string        `json:"license"`
	JournalRef string        `json:"journal_ref"`
	ReportNo   string        `json:"report_no"`
}

type arxivAuthor struct {
	KeyName     string `json:"keyname"`
	ForeName    string `json:"forename"`
	Suffix      string `json:"suffix"`
	Affiliation string `json:"affiliation"`
}

// arxivID returns the base arxiv ID without "oai:arXiv.org:" prefix or version suffix.
func arxivID(rec *arxivRecord) string {
	if rec.ID != "" {
		return rec.ID
	}
	return strings.TrimPrefix(rec.Identifier, "oai:arXiv.org:")
}

// versionedArxivID returns the arxiv ID with a version suffix. The arXiv OAI
// feed provides base IDs only (no version info), so we default to "v1". This
// satisfies the fatcat2 requirement that stored arxiv IDs be versioned.
func versionedArxivID(rec *arxivRecord) string {
	id := arxivID(rec)
	if id == "" {
		return ""
	}
	// If the ID already has a version suffix (e.g. from a future data source), keep it.
	if i := strings.LastIndex(id, "v"); i > 0 {
		suffix := id[i+1:]
		if suffix != "" && strings.IndexFunc(suffix, func(r rune) bool { return r < '0' || r > '9' }) == -1 {
			return id
		}
	}
	return id + "v1"
}

// skipReason returns a non-empty string explaining why this record should be
// skipped, or empty string if it should be processed.
func skipReason(rec *arxivRecord) string {
	if arxivID(rec) == "" {
		return "no-arxiv-id"
	}
	if rec.Status == "deleted" {
		return "deleted"
	}
	if strings.TrimSpace(rec.Title) == "" {
		return "empty-title"
	}
	if len(rec.Authors) > maxAuthors {
		return "too-many-authors"
	}
	return ""
}

// releaseType determines the fatcat release type from arxiv metadata.
// Defaults to "article-journal"; detects conference papers from journal-ref
// and reports from report-no.
func releaseType(rec *arxivRecord) string {
	jr := strings.ToLower(rec.JournalRef)
	if strings.Contains(jr, "conf.") || strings.Contains(jr, "proc.") ||
		strings.Contains(jr, "proceedings") || strings.Contains(jr, "workshop") {
		return "paper-conference"
	}
	if rec.ReportNo != "" {
		return "report"
	}
	return "article-journal"
}

// releaseDate parses an arxiv date string ("2006-01-02") into a fatcat2.ReleaseDate.
func releaseDate(s string) *fatcat2.ReleaseDate {
	if s == "" {
		return nil
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return nil
	}
	rd := fatcat2.ReleaseDate(t)
	return &rd
}

// releaseYear extracts the year from a date string, returning 0 on failure.
func releaseYear(s string) int {
	if len(s) < 4 {
		return 0
	}
	var y int
	if _, err := fmt.Sscanf(s[:4], "%d", &y); err != nil {
		return 0
	}
	return y
}

// doiFromRecord returns a normalised DOI from the record's DOI field.
func doiFromRecord(rec *arxivRecord) string {
	d := strings.TrimSpace(rec.DOI)
	d = strings.TrimPrefix(d, "doi:")
	d = strings.TrimPrefix(d, "https://doi.org/")
	d = strings.TrimPrefix(d, "http://doi.org/")
	d = strings.TrimPrefix(d, "https://dx.doi.org/")
	d = strings.TrimPrefix(d, "http://dx.doi.org/")
	if strings.HasPrefix(d, "10.") {
		return strings.ToLower(d)
	}
	return ""
}

// contribs converts arxiv author records to fatcat2 contribs. There is no
// ORCID in the arXiv OAI format.
func contribs(authors []arxivAuthor) []fatcat2.ReleaseContrib {
	out := make([]fatcat2.ReleaseContrib, 0, len(authors))
	for i, a := range authors {
		c := fatcat2.ReleaseContrib{
			Role:     "author",
			Position: i,
			Extra:    map[string]any{},
		}
		c.Surname = a.KeyName
		c.GivenName = a.ForeName
		switch {
		case a.ForeName != "" && a.KeyName != "":
			c.RawName = fmt.Sprintf("%s %s", a.ForeName, a.KeyName)
		case a.KeyName != "":
			c.RawName = a.KeyName
		}
		if a.Affiliation != "" {
			c.RawAffiliation = a.Affiliation
		}
		if a.Suffix != "" {
			c.Extra["suffix"] = a.Suffix
		}
		out = append(out, c)
	}
	return out
}

// abstract builds a fatcat2.Abstract from the record's abstract text if it
// meets the minimum length.
func abstract(text string) []fatcat2.Abstract {
	text = strings.TrimSpace(text)
	if len(text) < minAbstractLength {
		return nil
	}
	h := sha1.Sum([]byte(text))
	return []fatcat2.Abstract{
		{
			Content:  text,
			MIMEType: "text/plain",
			SHA1:     fmt.Sprintf("%x", h),
		},
	}
}

// categories returns the list of arxiv subject categories.
func categories(rec *arxivRecord) []string {
	cats := strings.Fields(rec.Categories)
	if len(cats) == 0 {
		cats = rec.SetSpec
	}
	return cats
}

// arxivToFc transforms an arxivRecord into a fatcat2.Release.
func arxivToFc(rec *arxivRecord, source string) fatcat2.Release {
	release := fatcat2.Release{
		Contribs:    []fatcat2.ReleaseContrib{},
		ExternalIDs: []fatcat2.ExternalID{},
		Extra:       map[string]any{},
		Abstracts:   []fatcat2.Abstract{},
		Refs:        []fatcat2.RawRef{},
		Source:      source,
	}

	// --- Identifiers ---

	// fatcat2 requires versioned arxiv IDs (e.g. "2301.12345v1"). The arXiv
	// OAI feed only provides the base ID, so we append "v1" by default.
	vid := versionedArxivID(rec)
	release.ExternalIDs = append(release.ExternalIDs, fatcat2.ExternalID{
		Type:  "arxiv",
		Value: vid,
	})

	doi := doiFromRecord(rec)
	if doi != "" {
		release.ExternalIDs = append(release.ExternalIDs, fatcat2.ExternalID{
			Type:  "doi",
			Value: doi,
		})
	}

	// --- Type and stage ---

	release.Type = releaseType(rec)
	release.Stage = "submitted"

	// --- Title ---

	release.Title = cleaning.CleanString(rec.Title)

	// --- Language ---

	// arXiv OAI "arXiv" prefix does not include language; default to English.
	release.Language = "en"

	// --- Date ---

	dateStr := rec.Created
	if dateStr == "" {
		dateStr = rec.Datestamp
	}
	release.ReleaseDate = releaseDate(dateStr)
	release.ReleaseYear = releaseYear(dateStr)

	// --- Abstract ---

	release.Abstracts = abstract(rec.Abstract)

	// --- Contribs ---

	release.Contribs = contribs(rec.Authors)

	// --- Extra ---

	cats := categories(rec)
	arxivExtra := map[string]any{}
	if len(cats) > 0 {
		arxivExtra["categories"] = cats
	}
	if rec.Comments != "" {
		arxivExtra["comments"] = rec.Comments
	}
	if rec.JournalRef != "" {
		arxivExtra["journal_ref"] = rec.JournalRef
	}
	if rec.ReportNo != "" {
		arxivExtra["report_no"] = rec.ReportNo
	}
	if len(arxivExtra) > 0 {
		release.Extra["arxiv"] = arxivExtra
	}

	return release
}

// createRelease creates the release in fatcat2 and indexes it to Elasticsearch.
func createRelease(client *http.Client, cs *counts.Counts, release fatcat2.Release) (*fatcat2.Release, error) {
	id, err := fatcat2.CreateRelease(client, release)
	if err != nil {
		return nil, fmt.Errorf("release creation failed: %w", err)
	}

	r, err := fatcat2.GetRelease(client, *id)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch new release %q: %w", id, err)
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

func ProcessLine(ctx context.Context, source string, lineb []byte) (out counts.Counts, err error) {
	out = counts.Counts{}
	l := activity.GetLogger(ctx)

	var rec arxivRecord
	if err := json.Unmarshal(lineb, &rec); err != nil {
		return out, fmt.Errorf("arxiv unmarshal: %w", err)
	}

	id := arxivID(&rec)
	vid := versionedArxivID(&rec)

	if reason := skipReason(&rec); reason != "" {
		l.Info("arxiv: skipping record", "id", id, "reason", reason)
		out.Releases.Skipped++
		return out, nil
	}

	client := &http.Client{}

	release := arxivToFc(&rec, source)

	// Lookup by versioned arxiv ID first, then DOI as fallback.
	foundID, err := fatcat2.LookupArxiv(client, vid)
	if err != nil {
		return out, fmt.Errorf("arxiv lookup failed for %q: %w", vid, err)
	}

	if foundID == nil && release.DOI() != "" {
		foundID, err = fatcat2.LookupDoi(client, release.DOI())
		if err != nil {
			return out, fmt.Errorf("doi lookup failed for %q: %w", release.DOI(), err)
		}
	}

	if foundID != nil {
		l.Debug("arxiv: found existing release", "id", vid, "release_id", foundID)
		release, err = fatcat2.GetRelease(client, *foundID)
		if err != nil {
			return out, fmt.Errorf("could not fetch existing release: %w", err)
		}
		out.Releases.Ignored++
	} else {
		r, err := createRelease(client, &out, release)
		if err != nil {
			return out, fmt.Errorf("could not create release for arxiv %q: %w", vid, err)
		}
		release = *r
		l.Debug("arxiv: created release", "id", vid, "release_id", release.ID)
		out.Releases.Added++
	}

	if !release.IsPaperlike() {
		l.Info("arxiv: skipping crawl, not paperlike", "id", id, "type", release.Type)
		return out, nil
	}

	urls := release.FulltextURLs()
	if len(urls) == 0 {
		l.Info("arxiv: skipping crawl, no fulltext URLs", "id", id)
		return out, nil
	}

	existingFiles, err := fatcat2.ReleaseFiles(client, release.ID)
	if err != nil {
		return out, fmt.Errorf("failed to check files for release %q: %w", release.ID, err)
	}
	if len(existingFiles) > 0 {
		l.Info("arxiv: skipping crawl, release already has files", "release_id", release.ID)
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
			l.Info("arxiv: crawl failed", "id", id, "url", u, "err", err)
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
		l.Debug("arxiv: ignoring known file", "sha256", file.Sha256, "id", id)
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

	pdfContent, err := pdf.Process(ctx, client, pdfBs, file.Sha1)
	if err != nil {
		return out, fmt.Errorf("blobproc processing failed: %w", err)
	}

	esDoc := indexing.PrepareFulltextDoc(indexing.FulltextTransformCtx{
		HttpClient: client,
		Release:    release,
		File:       &file,
		PdfText:    pdfContent.PdfText,
		GrobidXML:  pdfContent.GrobidXML,
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

	return out, nil
}
