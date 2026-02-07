package crossref

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	cdx "git.archive.org/webgroup/scholar/trawler/cdx/cdxclient"
	"git.archive.org/webgroup/scholar/trawler/cleaning"
	"git.archive.org/webgroup/scholar/trawler/crawling"
	"git.archive.org/webgroup/scholar/trawler/fatcat2"
	"git.archive.org/webgroup/scholar/trawler/harvesting"
	"git.archive.org/webgroup/scholar/trawler/indexing"
	"git.archive.org/webgroup/scholar/trawler/issn"
	"git.archive.org/webgroup/scholar/trawler/s3"
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
		sp := strings.Split(cc.ORCID, "/")
		orcid := sp[len(sp)-1]
		id, err := fatcat2.LookupOrcid(client, orcid)
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

	if len(c.Reference) > 5000 {
		return true
	}

	if len(c.Author)+len(c.Editor)+len(c.Translator) > 2000 {
		return true
	}

	return false
}

// TODO counts should be in a common package

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
	// Ingested is the count of PDFs we successfully ingested into scholar's search index
	Ingested int
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
			Ingested:    c.Releases.Ingested + other.Releases.Ingested,
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

		// TODO can we afford two or three activities per line? if we can, i'd rather see:
		// - harvestUpstream
		// - crawl
		// - handlePDF
		// but for now i'll keep it one per line

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

func processLine(ctx context.Context, in lineInput) (out counts, err error) {
	out = counts{}
	f, err := s3.GetObject(ctx, in.S3Key)
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

	if xrefdoc.IsSkippable() {
		l.Debug(fmt.Sprintf("skipping doi '%s'", xrefdoc.DOI))
		out.Releases.Skipped++
		return out, nil
	}

	// Check the DOI
	client := &http.Client{}

	release, err := xrefToFc(client, xrefdoc)
	if err != nil {
		return out, fmt.Errorf("could not transform xref->fc2: %w", err)
	}

	foundId, err := fatcat2.LookupDoi(client, strings.ToLower(xrefdoc.DOI))
	if err != nil {
		return out, err
	}

	if foundId == nil {
		r, err := createRelease(client, &out, release, xrefdoc)
		if err != nil {
			return out, fmt.Errorf("failed to create release for doi '%s': %w", xrefdoc.DOI, err)
		}
		release = *r
		l.Debug(fmt.Sprintf("created release %s", release.ID))
		out.Releases.Added++
	} else {
		// TODO here is where we could update release with info from xref should we so desire
		release, err = fatcat2.GetRelease(client, *foundId)
		if err != nil {
			return out, fmt.Errorf("could not look up existing release: %w", err)
		}
		out.Releases.Ignored++

		l.Debug(fmt.Sprintf("found release %s", release.ID))
	}

	if !isCrawlWanted(release) {
		l.Debug(fmt.Sprintf("decided crawl was unwanted for release %s", release.ID))
		return out, err
	}

	out.Releases.CrawlWanted++

	// porting the monster that is process_file from sandcrawler:python/sandcrawler/ingest_file.py
	spnClient, err := spnclient.NewDefaultClient(spnclient.SPNConfig{
		AccessKey: viper.GetString("spn.access_key"),
		SecretKey: viper.GetString("spn.secret_key"),
		Endpoint:  viper.GetString("spn.endpoint"),
		Debug:     true,
	})
	if err != nil {
		l.Debug(err.Error())
		panic("spn client was not created")
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

	for _, u := range release.FulltextURLs() {
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
			l.Info(fmt.Sprintf("%s: get failed: %s", release.ID, err.Error()))
			continue
		}

		l.Debug(fmt.Sprintf("%s: got result %#v", release.ID, res))
		if res.Success {
			break
		}
		// TODO check result -- if success, break and continue. otherwise..?
		// results we care about later are going to be in the slog
		// question is if we should only use result for success and errors for failure
	}

	if err != nil || !res.Success {
		return out, nil
	}

	// TODO can share this pdf byte handling stuff between different upstreams

	mimetype, _, _ := strings.Cut(res.Mimetype, ";")

	file := fatcat2.File{
		Releases: []fatcat2.Release{release},
		Mimetype: mimetype,
		URLs: []fatcat2.FileURL{
			{
				Rel: "wayback",
				URL: res.SnapshotUrl,
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

	// TODO check if file exists. This can happen if we're re-running this
	// workflow. NB--in that case, we've pulled all of the stuff from a previous
	// crawl from CDX and don't need to worry about wasting SPN time assuming
	// stuff is fresh enough which reminds me if we ever want to care about cdx
	// freshness...
	fileID, err := fatcat2.LookupSha256(client, file.Sha256)
	if err != nil {
		return out, fmt.Errorf("failed to look up checksum '%s': %w", file.Sha256, err)
	}

	// TODO we could verify that the existing file is attached to the release ID
	// we're working with...

	if fileID != nil {
		l.Debug(fmt.Sprintf("ignoring known sha256 '%s' (rid: '%s'", file.Sha256, release.ID))
		return out, nil
	}

	fid, err := fatcat2.CreateFile(client, &file)
	if err != nil {
		return out, fmt.Errorf("fc2 api failed to make file for '%s': %w", file.URLs[0].URL, err)
	}
	l.Debug(fmt.Sprintf("created file %s", fid))
	out.Releases.Acquired++

	fileDoc := indexing.PrepareFatcatFileDoc(file)
	bs, err := json.Marshal(fileDoc)
	if err != nil {
		return out, fmt.Errorf("failed to marshal file es doc: %w", err)
	}

	err = indexing.DoElasticIndex(client,
		viper.GetString("indexing.fatcat_file_ix"), fileDoc.LegacyIdent, bs)
	if err != nil {
		return out, fmt.Errorf("failed to index doc for file '%s': %w", file.ID, err)
	}

	//	"Send your PDF payload to %s/spool - a 200 OK status only confirms
	//	receipt, not successful postprocessing, which may take more time. Check
	//	Location header for spool id."

	blobprocEndpoint := viper.GetString("blobproc.endpoint")
	req, err := http.NewRequest("POST", blobprocEndpoint, bytes.NewBuffer(pdfBs))
	if err != nil {
		return out, fmt.Errorf("could not form blobproc request: %w", err)
	}

	req.Header.Set("Content-Type", "application/pdf")

	// TODO do s3 read first to see if it's already done, *then* hit blobproc

	l.Debug(fmt.Sprintf("%s: submitting to blobproc", release.ID))
	resp, err := client.Do(req)
	if err != nil {
		return out, fmt.Errorf("blobproc request error: %w", err)
	}
	if resp.StatusCode != 202 {
		return out, fmt.Errorf("unexpected status from blobproc '%d'", resp.StatusCode)
	}

	loc := resp.Header.Get("Location")
	if loc == "" {
		return out, fmt.Errorf("got blank spool url")
	}

	pu, err := url.Parse(loc)
	if err != nil {
		panic(err)
	}

	pollUrl := blobprocEndpoint + pu.Path

	// blobproc uses sha1. it should be in the spoolUrl and it should match what we derived earlier
	if !strings.Contains(pollUrl, file.Sha1) {
		return out, fmt.Errorf("expected to see file sha1 '%s' in spool url '%s'", file.Sha1, loc)
	}

	req, err = http.NewRequest("GET", pollUrl, nil)
	if err != nil {
		panic(err)
	}

	l.Debug(fmt.Sprintf("%s: polling blobproc at %s", release.ID, pollUrl))

	for {
		time.Sleep(viper.GetDuration("blobproc.poll_interval"))
		resp, err = client.Do(req)
		if err != nil {
			return out, fmt.Errorf("error polling blobproc: %w", err)
		}

		if resp.StatusCode == 404 {
			break
		}
		l.Debug(fmt.Sprintf("%s: waiting on blobproc", release.ID))
	}

	// get blobproc stuff for ingestion

	/*
		thoughts...I just had a situation where blobproc couldn't start because of
		misconfigured s3 endpoint. the resulting behavior was that this code got
		all the way to s3 object getting. so i guess we got back a location header
		that immediately 404ed? but my file is still sitting in spool. so how did i
		get a 404? this warrants investigation...
	*/

	s3bucket := viper.GetString("blobproc.s3bucket")

	grobidS3Key := fmt.Sprintf("%s/%s/%s/%s/%s.tei.xml",
		s3bucket, "grobid", file.Sha1[0:2], file.Sha1[2:4], file.Sha1)

	obj, err := s3.GetObject(ctx, grobidS3Key)
	if err != nil {
		return out, fmt.Errorf("blobproc s3 read failed: %w", err)
	}

	grobidXML, err := io.ReadAll(obj)
	if err != nil {
		return out, fmt.Errorf("could not read '%s': %w", grobidS3Key, err)
	}

	pdftotextS3Key := fmt.Sprintf("%s/%s/%s/%s/%s.txt",
		s3bucket, "text", file.Sha1[0:2], file.Sha1[2:4], file.Sha1)

	obj, err = s3.GetObject(ctx, pdftotextS3Key)
	if err != nil {
		return out, fmt.Errorf("blobproc s3 read failed: %w", err)
	}

	pdfText, err := io.ReadAll(obj)
	if err != nil {
		return out, fmt.Errorf("could not read '%s': %w", pdftotextS3Key, err)
	}

	fmt.Printf("DBG %#v\n", string(pdfText[:100])+"...")

	// regarding ingestion

	/*
		The correct way to do ingestion is batching. for now, given time
		constraints and the fact that this is 80% proof of concept, i'm going to do
		one at a time. Looking back at 30 days of sandcrawler ingest rate graphs,
		we had peaks of 300 docs/sec; I assume at that point we ought to have batch
		processing. To get to the end of the year though I'm doing one off just to
		make sure it's all working; once it is, I'd like to have this workflow
		return a list of s3 keys to ingest so they can be done as a single batch.
	*/

	ictx := indexing.FulltextTransformCtx{
		HttpClient: client,
		Release:    release,
		File:       &file,
		PdfText:    pdfText,
		GrobidXML:  grobidXML,
	}

	if release.ContainerID != nil {
		container, err := fatcat2.GetContainer(client, *release.ContainerID)
		if err != nil {
			return out, fmt.Errorf("could not fetch container '%s': %w", release.ContainerID, err)
		}
		ictx.Container = &container
	}

	esDoc := indexing.PrepareFulltextDoc(ictx)

	//fmt.Println(esDoc)

	bs, err = json.Marshal(esDoc)
	if err != nil {
		return out, fmt.Errorf("marshaling fulltext doc failed: %w", err)
	}

	err = indexing.DoElasticIndex(client, viper.GetString("indexing.fulltext_ix"), esDoc.Key, bs)
	if err != nil {
		return out, fmt.Errorf("indexing fulltext failed: %w", err)
	}

	out.Releases.Ingested++

	return out, nil
}

func xrefToFc(client *http.Client, xrefdoc crossrefDoc) (fatcat2.Release, error) {
	release := fatcat2.Release{
		Contribs:    []fatcat2.ReleaseContrib{},
		ExternalIDs: []fatcat2.ExternalID{},
		Extra:       map[string]any{},
		Publisher:   xrefdoc.Publisher,
		Volume:      xrefdoc.Volume,
		Issue:       xrefdoc.Issue,
		Pages:       xrefdoc.Page,
		Language:    xrefdoc.Language,
		// TODO fix source setting
		Source: "dev",
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

func createRelease(client *http.Client, cs *counts, release fatcat2.Release, xrefdoc crossrefDoc) (*fatcat2.Release, error) {
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

// isCrawlWanted returns true if we feel this release is worthy of a crawl
// attempt; specific to things gleaned from crossref (the DOI check)
func isCrawlWanted(release fatcat2.Release) bool {
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

	return true
}

// TODO this should probably be refactored such that day and limit options are
// just part of the workflow args; I don't know that it's useful to have
// anything related to scholkit exposed at the workflow level

type CrossrefCrawlInput struct {
	SKInput SKCrossrefInput
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
	workflow.GetLogger(ctx).Info("scholkit crossref s3key: " + skOut.S3Key)

	ao = workflow.ActivityOptions{
		StartToCloseTimeout: 8 * 60 * 60 * time.Second,
		TaskQueue:           viper.GetString("crossref.internal_task_queue"),
	}
	ctx = workflow.WithActivityOptions(ctx, ao)

	batchInput := lineBatchInput{
		S3Key: skOut.S3Key,
	}
	findInput := harvesting.FindLineBatchInput{
		S3Key: skOut.S3Key,
	}
	findOutput := harvesting.FindLineBatchOutput{}
	childSelector := workflow.NewSelector(ctx)
	var childCount int

	var childErr error
	var childCounts counts
	for {
		err := workflow.ExecuteActivity(ctx, harvesting.FindLineBatch, findInput).Get(ctx, &findOutput)
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
}
