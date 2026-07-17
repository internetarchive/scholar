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

	// The fields below carry state across ContinueAsNew. Callers starting a
	// fresh day leave them at their zero values; the workflow populates them and
	// threads them through each ContinueAsNew so a day resumes where it left off
	// without re-scraping or re-deriving its provenance label.

	// S3Key points to the scraped .ndjson in s3. Empty on the first run, which
	// is the signal to run the scrape activity; populated and carried forward
	// thereafter.
	S3Key string
	// Source is the provenance label for created records. Pinned on the first
	// run (it embeds the RunID, which changes on every ContinueAsNew) and carried
	// forward so a day's records all share one source string.
	Source string
	// Offset is the byte position in the .ndjson to resume reading from.
	Offset int64
	// Counts accumulates results across every run in the ContinueAsNew chain so
	// the final run returns the day's grand total.
	Counts counts.Counts

	// LinesPerCAN, BatchSize, ChunkSize, and the task queue names are
	// viper-sourced settings captured once on the first run (see the SideEffect
	// below) and threaded across ContinueAsNew so they stay fixed for the whole
	// chain. We snapshot them rather than reading viper in the workflow body on
	// every run because viper reads aren't deterministic across replay: a config
	// change in Ansible would otherwise desync a running workflow's history -- or
	// silently shift its behavior between runs. A zero BatchSize means we haven't
	// captured yet. ExternalTaskQueue is only used by the first-run scrape but is
	// snapshotted with the rest for the same determinism reason.
	LinesPerCAN       int
	BatchSize         int
	ChunkSize         int
	InternalTaskQueue string
	ExternalTaskQueue string
}

// dailyConfig is the snapshot of viper-sourced settings the workflow captures
// via SideEffect on the first run of a ContinueAsNew chain.
type dailyConfig struct {
	LinesPerCAN       int
	BatchSize         int
	ChunkSize         int
	InternalTaskQueue string
	ExternalTaskQueue string
}

func DailyCrawlWorkflow(ctx workflow.Context, in DailyCrawlWorkflowInput) (counts.Counts, error) {
	l := workflow.GetLogger(ctx)

	// Snapshot config once, on the first run, and thread it across
	// ContinueAsNew so it's fixed for the whole chain. SideEffect records the
	// read in history, so replays return the recorded values instead of
	// re-reading viper -- which is what keeps an in-flight chain deterministic
	// even when the deployed config changes underneath it.
	if in.BatchSize == 0 {
		var cfg dailyConfig
		if err := workflow.SideEffect(ctx, func(workflow.Context) interface{} {
			return dailyConfig{
				LinesPerCAN:       viper.GetInt("daily.lines_per_can"),
				BatchSize:         viper.GetInt("harvesting.batch_size"),
				ChunkSize:         viper.GetInt("harvesting.chunk_size"),
				InternalTaskQueue: viper.GetString(fmt.Sprintf("%s.internal_task_queue", in.Upstream)),
				ExternalTaskQueue: viper.GetString(fmt.Sprintf("%s.external_task_queue", in.Upstream)),
			}
		}).Get(&cfg); err != nil {
			return in.Counts, err
		}
		if cfg.LinesPerCAN <= 0 {
			cfg.LinesPerCAN = 2000
		}
		in.LinesPerCAN = cfg.LinesPerCAN
		in.BatchSize = cfg.BatchSize
		in.ChunkSize = cfg.ChunkSize
		in.InternalTaskQueue = cfg.InternalTaskQueue
		in.ExternalTaskQueue = cfg.ExternalTaskQueue
	}

	// First run of the ContinueAsNew chain only: pin the source label and scrape
	// the upstream. Both are threaded through `in` on every subsequent run so we
	// never re-scrape and a day's records all share one source string.
	if in.S3Key == "" {
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
		in.Source = source

		// fetch metadata from the upstream API and store in s3
		scrapeCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
			StartToCloseTimeout: 4 * 60 * 60 * time.Second,
			TaskQueue:           in.ExternalTaskQueue,
			RetryPolicy: &temporal.RetryPolicy{
				InitialInterval:    time.Second,
				BackoffCoefficient: 10.0,
				MaximumInterval:    time.Second * 480,
				MaximumAttempts:    6,
			},
		})
		scrapeIn := scholkitScrapeInput{
			Day:      in.Day,
			Upstream: in.Upstream,
		}
		var scrapeOut scholkitScrapeOutput
		if err := workflow.ExecuteActivity(scrapeCtx, ScholkitScrapeActivity, scrapeIn).Get(scrapeCtx, &scrapeOut); err != nil {
			l.Error(fmt.Sprintf("scholkit %s activity failed: %s", in.Upstream, err))
			return in.Counts, err
		}
		if scrapeOut.S3Key == "" {
			return in.Counts, fmt.Errorf("scholkit %s returned an empty s3 key", in.Upstream)
		}
		in.S3Key = scrapeOut.S3Key
		l.Info(fmt.Sprintf("scholkit %s s3key: %s", in.Upstream, in.S3Key))
	}

	// Process the harvested data serially, resuming at in.Offset. We call
	// ProcessLine once per line and ContinueAsNew periodically so no single run
	// accumulates enough history to approach Temporal's limits. Only the byte
	// offset and running totals cross the ContinueAsNew boundary -- never the
	// (potentially tens-of-MB) offset list.

	findCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 8 * 60 * 60 * time.Second,
		TaskQueue:           in.InternalTaskQueue,
	})
	procCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout:    4 * time.Hour,
		ScheduleToCloseTimeout: 8 * time.Hour,
		HeartbeatTimeout:       2 * time.Minute,
		TaskQueue:              in.InternalTaskQueue,
		RetryPolicy: &temporal.RetryPolicy{
			BackoffCoefficient: 1.5,
			InitialInterval:    30 * time.Second,
			MaximumInterval:    90 * time.Second,
		},
	})

	linesPerCAN := in.LinesPerCAN

	findInput := harvesting.FindLineBatchInput{
		S3Key:     in.S3Key,
		Offset:    in.Offset,
		BatchSize: in.BatchSize,
		ChunkSize: in.ChunkSize,
	}
	lin := harvesting.ProcessLineInput{
		S3Key:    in.S3Key,
		Source:   in.Source,
		Upstream: in.Upstream,
	}

	var linesThisRun int
	for {
		var findOutput harvesting.FindLineBatchOutput
		if err := workflow.ExecuteActivity(findCtx, harvesting.FindLineBatch, findInput).Get(findCtx, &findOutput); err != nil {
			return in.Counts, err
		}

		for _, offset := range findOutput.Offsets {
			lin.LineStart = offset[0]
			lin.Length = offset[1]

			var c counts.Counts
			if err := workflow.ExecuteActivity(procCtx, ProcessLine, lin).Get(procCtx, &c); err != nil {
				return in.Counts, err
			}
			in.Counts = in.Counts.Add(c)
			linesThisRun++

			// Advance the resume point to just past the line we finished.
			// offset[0]+offset[1] is the byte after this line's trailing newline
			// (see harvesting.chunk), so a mid-batch ContinueAsNew neither
			// re-processes nor skips a line.
			in.Offset = offset[0] + offset[1]
			findInput.Offset = in.Offset

			// Hand off to a fresh run to keep history bounded once we've
			// processed enough lines (or the server suggests it). Carry only
			// the byte offset and accumulated counts. This check lives inside
			// the per-line loop on purpose: a batch_size larger than linesPerCAN
			// would otherwise force the entire batch through ProcessLine before
			// the first CAN check, bloating a single run's history.
			if linesThisRun >= linesPerCAN || workflow.GetInfo(ctx).GetContinueAsNewSuggested() {
				l.Info(fmt.Sprintf("continue-as-new for source %q at offset %d (%d lines this run)",
					in.Source, in.Offset, linesThisRun))
				return in.Counts, workflow.NewContinueAsNewError(ctx, DailyCrawlWorkflow, in)
			}
		}

		in.Offset = findOutput.BytesRead
		findInput.Offset = findOutput.BytesRead

		if findOutput.EOF {
			l.Info(fmt.Sprintf("day complete for source %q: %#v", in.Source, in.Counts))
			return in.Counts, nil
		}
	}
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

	pdfProcessor, err := pdf.NewProcessor(func(msg string) {
		activity.RecordHeartbeat(ctx, msg)
	})
	if err != nil {
		return out, fmt.Errorf("pdf processor init failed: %w", err)
	}

	activity.RecordHeartbeat(ctx, "pre-pdf-process")
	pdfContent, err := pdfProcessor.Process(ctx, pdfBs, file.Sha1)
	if err != nil {
		return out, fmt.Errorf("pdf processing failed: %w", err)
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
	var ee *exec.ExitError
	if err != nil {
		if errors.As(err, &ee) {
			l.Error("************* scholkit stderr start ****************")
			l.Error(string(ee.Stderr))
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
