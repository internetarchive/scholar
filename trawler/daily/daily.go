package daily

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"git.archive.org/webgroup/scholar/trawler/arxiv"
	cdx "git.archive.org/webgroup/scholar/trawler/cdx/cdxclient"
	"git.archive.org/webgroup/scholar/trawler/counts"
	"git.archive.org/webgroup/scholar/trawler/crawling"
	"git.archive.org/webgroup/scholar/trawler/crossref"
	"git.archive.org/webgroup/scholar/trawler/datacite"
	"git.archive.org/webgroup/scholar/trawler/doaj"
	"git.archive.org/webgroup/scholar/trawler/fatcat2"
	"git.archive.org/webgroup/scholar/trawler/harvesting"
	"git.archive.org/webgroup/scholar/trawler/indexing"
	"git.archive.org/webgroup/scholar/trawler/pdf"
	"git.archive.org/webgroup/scholar/trawler/pubmed"
	"git.archive.org/webgroup/scholar/trawler/spn/spnclient"
	"github.com/google/uuid"
	"github.com/spf13/viper"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

type DailyCrawlWorkflowInput struct {
	// Day value in format 2006-01-02
	Day string
	// SourceOverride sets a static string to use instead of letting trawler
	// generate a source value dynamically
	SourceOverride string
	// Upstream is which API we're scraping
	Upstream string
}

func DailyCrawlWorkflow(ctx workflow.Context, in DailyCrawlWorkflowInput) (counts.Counts, error) {
	l := workflow.GetLogger(ctx)
	out := counts.Counts{}
	source := in.SourceOverride
	if source == "" {
		day := in.Day
		if day == "" {
			day = workflow.Now(ctx).AddDate(0, 0, -1).Format("2006-01-02")
		}
		rid := workflow.GetInfo(ctx).WorkflowExecution.RunID
		if len(rid) > 8 {
			rid = rid[:8]
		}
		source = fmt.Sprintf("%s-%s-%s", in.Upstream, day, rid)
	}

	// fetch metadata from the upstream API and store in s3

	ao := workflow.ActivityOptions{
		StartToCloseTimeout: 4 * 60 * 60 * time.Second,
		TaskQueue:           viper.GetString(fmt.Sprintf("%s.external_task_queue", in.Upstream)),
	}
	ctx = workflow.WithActivityOptions(ctx, ao)

	scrapeIn := scholkitScrapeInput{
		Day:      in.Day,
		Upstream: in.Upstream,
	}
	var scrapeOut scholkitScrapeOutput
	err := workflow.ExecuteActivity(ctx, ScholkitScrapeActivity, scrapeIn).Get(ctx, &scrapeOut)
	if err != nil {
		l.Error(fmt.Sprintf("scholkit %s activity failed: %s", in.Upstream, err))
		return out, err
	}
	l.Info(fmt.Sprintf("scholkit %s s3key: %s", in.Upstream, scrapeOut.S3Key))

	// process the harvested data

	ao = workflow.ActivityOptions{
		StartToCloseTimeout: 8 * 60 * 60 * time.Second,
		TaskQueue:           viper.GetString(fmt.Sprintf("%s.internal_task_queue", in.Upstream)),
	}
	ctx = workflow.WithActivityOptions(ctx, ao)

	batchInput := lineBatchInput{
		S3Key:    scrapeOut.S3Key,
		Source:   source,
		Upstream: in.Upstream,
	}
	findInput := harvesting.FindLineBatchInput{
		S3Key:     scrapeOut.S3Key,
		BatchSize: viper.GetInt("harvesting.batch_size"),
		ChunkSize: viper.GetInt("harvesting.chunk_size"),
	}
	findOutput := harvesting.FindLineBatchOutput{}
	childSelector := workflow.NewSelector(ctx)
	var childCount int

	var childErr error
	var childCounts counts.Counts
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
			fut := workflow.ExecuteChildWorkflow(ctx, LineBatchWorkflow, batchInput)
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
			return out, childErr
		}
		out = out.Add(childCounts)
		l.Info(fmt.Sprintf("child ignored %d lines", childCounts.Releases.Ignored))
	}

	l.Info(fmt.Sprintf("%#v", out))

	return out, nil
}

type lineBatchInput struct {
	// S3Key is a key to a .ndjson file in s3 storage
	S3Key string
	// Offsets is a list of pairs of [ReadOffset, Length]
	Offsets [][]int64
	// Source identifies what crawl led to the creation of records for provenance purposes
	Source string
	// Upstream is which API we're scraping
	Upstream string
}

func LineBatchWorkflow(ctx workflow.Context, in lineBatchInput) (counts.Counts, error) {
	out := counts.Counts{}
	ao := workflow.ActivityOptions{
		StartToCloseTimeout:    4 * time.Hour,
		ScheduleToCloseTimeout: 8 * time.Hour,
		HeartbeatTimeout:       2 * time.Minute,
		TaskQueue:              viper.GetString(fmt.Sprintf("%s.internal_task_queue", in.Upstream)),
		RetryPolicy: &temporal.RetryPolicy{
			BackoffCoefficient: 1.5,
			InitialInterval:    30 * time.Second,
			MaximumInterval:    90 * time.Second,
		},
	}
	ctx = workflow.WithActivityOptions(ctx, ao)
	lin := harvesting.ProcessLineInput{
		S3Key:    in.S3Key,
		Source:   in.Source,
		Upstream: in.Upstream,
	}
	for _, offset := range in.Offsets {
		lin.LineStart = offset[0]
		lin.Length = offset[1]

		var c counts.Counts

		// TODO can we afford two or three activities per line? if we can, i'd rather see:
		// - harvestUpstream
		// - crawl
		// - handlePDF
		// but for now i'll keep it one per line

		err := workflow.ExecuteActivity(ctx, ProcessLine, lin).Get(ctx, &c)
		if err != nil {
			return out, err
		}
		out = out.Add(c)
	}
	return out, nil
}

func ProcessLine(ctx context.Context, in harvesting.ProcessLineInput) (counts.Counts, error) {
	activity.RecordHeartbeat(ctx, "started")
	out := counts.Counts{}
	l := activity.GetLogger(ctx)

	lineb, err := harvesting.GetLine(ctx, in.S3Key, in.LineStart, in.Length)
	if err != nil {
		return out, fmt.Errorf("failed to read ndjson line from s3: %w", err)
	}
	activity.RecordHeartbeat(ctx, "got-line")

	// TODO use an input/output pointer arg for release

	var processLine func(context.Context, *http.Client, string, []byte) (counts.Counts, *fatcat2.Release, error)
	switch in.Upstream {
	case "arxiv":
		processLine = arxiv.ProcessLine
	case "crossref":
		processLine = crossref.ProcessLine
	case "datacite":
		processLine = datacite.ProcessLine
	case "doaj":
		processLine = doaj.ProcessLine
	case "pubmed":
		processLine = pubmed.ProcessLine
	default:
		panic("unknown upstream: " + in.Upstream)
	}

	client := &http.Client{}
	var release *fatcat2.Release

	activity.RecordHeartbeat(ctx, "pre-process-line")
	out, release, err = processLine(ctx, client, in.Source, lineb)
	if err != nil {
		return out, fmt.Errorf("%s processing failed: %w", in.Upstream, err)
	}
	activity.RecordHeartbeat(ctx, "post-process-line")

	if release == nil {
		return out, nil
	}

	doiPrefixBlocklist := viper.GetStringSlice("crawling.doi_prefix_blocklist")
	if ok, reason := shouldCrawlRelease(release, doiPrefixBlocklist); !ok {
		l.Info(fmt.Sprintf("skipping crawl for %q: %s", release.ID, reason))
		return out, nil
	}

	urls := release.FulltextURLs()

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

	existingFiles, err := fatcat2.ReleaseFiles(client, release.ID)
	if err != nil {
		return out, fmt.Errorf("failed to check files for release %q: %w", release.ID, err)
	}
	if len(existingFiles) > 0 {
		l.Info(fmt.Sprintf("skipping crawl for %q, already has %d files", release.ID, len(existingFiles)))
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
		activity.RecordHeartbeat(ctx, "top-of-crawl-loop")
		crawler := crawling.PDFCrawler{
			SPNClient:       spnClient,
			CDXClient:       cdxClient,
			MaxHops:         8,
			UserAgent:       viper.GetString("crawling.user_agent"),
			WaybackEndpoint: viper.GetString("wayback.replay_endpoint"),
			SimpleGets:      viper.GetStringSlice("crawling.simple_get_list"),
			Blocklist:       viper.GetStringSlice("crawling.url_blocklist"),
			Logger:          slog.Default(),
			Heartbeater: func(msg string) {
				activity.RecordHeartbeat(ctx, msg)
			},
		}

		res, err = crawler.Crawl(u)
		if err != nil {
			l.Info(fmt.Sprintf("crawl failed for %q, %q: %s", release.ID, u, err))
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

	fid, err := uuid.NewV7()
	if err != nil {
		return out, fmt.Errorf("uuid creation failed: %w", err)
	}

	file := fatcat2.File{
		ID:       fid,
		Releases: []fatcat2.Release{*release},
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

	// TODO check if file exists. This can happen if we're re-running this
	// workflow. NB--in that case, we've pulled all of the stuff from a previous
	// crawl from CDX and don't need to worry about wasting SPN time assuming
	// stuff is fresh enough which reminds me if we ever want to care about cdx
	// freshness...

	fileID, err := fatcat2.LookupSha256(client, file.Sha256)
	if err != nil {
		return out, fmt.Errorf("sha256 lookup failed: %w", err)
	}

	activity.RecordHeartbeat(ctx, "file-lookup")

	// TODO we could verify that the existing file is attached to the release ID
	// we're working with...

	if fileID == nil {
		fileID, err = fatcat2.CreateFile(client, &file)
		if err != nil {
			return out, fmt.Errorf("file creation failed: %w", err)
		}
		out.Releases.Acquired++
	}

	activity.RecordHeartbeat(ctx, "file-created")

	fileInES, err := indexing.ElasticDocExists(client,
		viper.GetString("indexing.fatcat_file_ix"), "sha1", file.Sha1)
	if err != nil {
		return out, fmt.Errorf("fatcat_file existence check failed: %w", err)
	}
	if !fileInES {
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
	}

	activity.RecordHeartbeat(ctx, "file-indexed")

	fulltextInES, err := indexing.ElasticDocExists(client,
		viper.GetString("indexing.fulltext_ix"), "fulltext.file_sha1", file.Sha1)
	if err != nil {
		return out, fmt.Errorf("scholar_fulltext existence check failed: %w", err)
	}
	if fulltextInES {
		return out, nil
	}

	pdfProcessor := pdf.Processor{
		Client: client,
		Heartbeater: func(msg string) {
			activity.RecordHeartbeat(ctx, msg)
		},
	}

	activity.RecordHeartbeat(ctx, "pre-pdf-process")
	pdfContent, err := pdfProcessor.Process(ctx, pdfBs, file.Sha1)
	if err != nil {
		return out, fmt.Errorf("blobproc processing failed: %w", err)
	}
	activity.RecordHeartbeat(ctx, "post-pdf-process")

	var container *fatcat2.Container
	if release.ContainerID != nil {
		c, err := fatcat2.GetContainer(client, *release.ContainerID)
		if err != nil {
			return out, fmt.Errorf("could not fetch container: %w", err)
		}
		container = &c
	}
	activity.RecordHeartbeat(ctx, "container-fetched")

	slog.Info("preparing full text es doc", "rid", release.ID, "xmlLen", len(pdfContent.GrobidXML), "pdfTextLen", len(pdfContent.PdfText))

	esDoc := indexing.PrepareFulltextDoc(indexing.FulltextTransformCtx{
		HttpClient: client,
		Release:    *release,
		File:       &file,
		PdfText:    pdfContent.PdfText,
		GrobidXML:  pdfContent.GrobidXML,
		Container:  container,
	})

	ftbs, err := json.Marshal(esDoc)
	if err != nil {
		return out, fmt.Errorf("marshaling fulltext doc failed: %w", err)
	}

	slog.Info("doing index", "rid", release.ID, "docKey", esDoc.Key, "ftLen", len(esDoc.Fulltext.Body))

	err = indexing.DoElasticIndex(client,
		viper.GetString("indexing.fulltext_ix"), esDoc.Key, ftbs)
	if err != nil {
		return out, fmt.Errorf("indexing fulltext failed: %w", err)
	}
	activity.RecordHeartbeat(ctx, "fulltext-indexed")

	out.Releases.Ingested++

	return out, nil
}

// shouldCrawlRelease returns false with a reason string when we should skip
// attempting to crawl a release's fulltext. It encapsulates the pure
// decision logic so it can be unit tested independently of ProcessLine's I/O.
func shouldCrawlRelease(release *fatcat2.Release, doiPrefixBlocklist []string) (bool, string) {
	if !release.IsPaperlike() {
		return false, "not-paperlike"
	}

	if len(release.ExternalIDs) == 0 {
		return false, "no-extids"
	}

	doi := release.DOI()
	if doi != "" {
		for _, prefix := range doiPrefixBlocklist {
			if strings.HasPrefix(doi, prefix) {
				return false, fmt.Sprintf("doi-prefix-blocked:%s", prefix)
			}
		}
	}

	if len(release.FulltextURLs()) == 0 {
		return false, "no-fulltext-urls"
	}

	return true, ""
}

type scholkitScrapeInput struct {
	// Day value in format 2006-01-02
	Day string
	// Upstream is which API we're scraping
	Upstream string
}

type scholkitScrapeOutput struct {
	// S3Key points to a large .ndjson file of metadata from an upstream source
	S3Key string
}

func ScholkitScrapeActivity(ctx context.Context, in scholkitScrapeInput) (scholkitScrapeOutput, error) {
	out := scholkitScrapeOutput{}
	l := activity.GetLogger(ctx)

	s3bucket := viper.GetString(fmt.Sprintf("%s.scholkit_s3_bucket", in.Upstream))

	var syncStart time.Time
	var syncEnd time.Time
	var err error
	if in.Day != "" {
		syncStart, err = time.Parse("2006-01-02", in.Day)
		if err != nil {
			return out, err
		}
		syncEnd = syncStart.AddDate(0, 0, 1)
	} else {
		syncEnd = time.Now()
		syncStart = syncEnd.AddDate(0, 0, -1)
	}

	s3Prefix := in.Upstream + "/" + syncEnd.Format("2006")

	skPath := viper.GetString("scholkit.path")
	// TODO verify binary path?

	scholkitArgs := []string{
		"-s", in.Upstream,
		"-d", viper.GetString("scholkit.data_dir"),
		"-s3-upload",
		"-s3-endpoint", viper.GetString("s3.endpoint"),
		"-s3-access-key", viper.GetString("s3.access_id"),
		"-s3-secret-key", viper.GetString("s3.secret_key"),
		"-s3-bucket", s3bucket,
		"-s3-prefix", s3Prefix,
		"-sync-start", syncStart.Format("2006-01-02"),
		"-sync-end", syncEnd.Format("2006-01-02"),
	}

	l.Info(fmt.Sprintf("sk cmd: %s %s", skPath, scholkitArgs))
	cmd := exec.Command(skPath, scholkitArgs...)
	bs, err := cmd.Output()
	if err != nil {
		if errors.Is(err, &exec.ExitError{}) {
			l.Error("************* scholkit stderr start ****************")
			l.Error(string(err.(*exec.ExitError).Stderr))
			l.Error("************* scholkit stderr end ******************")
		}
		return out, fmt.Errorf("sk failed: %w", err)
	}

	s3key := strings.TrimSpace(string(bs))

	if s3key == "" {
		return out, errors.New("empty s3 key from scholkit")
	}

	l.Info(fmt.Sprintf("scholkit %s data to s3 key %q", in.Upstream, s3key))

	out.S3Key = s3key
	return out, nil
}
