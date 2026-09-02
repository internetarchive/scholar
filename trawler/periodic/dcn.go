package periodic

/*
One-off ingest of the Digital Commons Network sweep (dcn-sweep-2026-08).

This differs from PeriodicIngestWorkflow in where the work comes from and how
much of fatcat it has to build. Periodic ingest walks CDX rows out of petabox
WARCs and only ever *attaches* a file to a release that already exists — a PDF
whose GROBID header yields no known external ID is skipped. The DCN sweep is the
opposite case: 1.16M institutional-repository PDFs, most of which fatcat has
never seen, arriving with real upstream metadata (OAI title/date/creators, a DOI
for ~19%, an ISSN or container name for ~42%). So this pipeline creates
releases and containers as well as files.

Input is the enriched sweep file, one JSON record per line, already uploaded to
S3 — see DCNRecord for the shape. Records are addressed by byte offset through
harvesting.FindLineBatch, the same way periodic ingest reads cached CDX, so the
workflow can continue-as-new mid-file without re-processing or skipping a line.

Ordering within a record is deliberate: resolve or create the release first
(cheap, metadata-only, and it establishes the dedupe key), and only then go to
Wayback for the PDF. Fetching bytes is by far the most expensive step and is
pointless if we cannot attach the result to anything.
*/

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"git.archive.org/webgroup/scholar/trawler/cdx/cdxclient"
	"git.archive.org/webgroup/scholar/trawler/cleaning"
	"git.archive.org/webgroup/scholar/trawler/counts"
	"git.archive.org/webgroup/scholar/trawler/fatcat2"
	"git.archive.org/webgroup/scholar/trawler/harvesting"
	"git.archive.org/webgroup/scholar/trawler/indexing"
	"git.archive.org/webgroup/scholar/trawler/issn"
	"git.archive.org/webgroup/scholar/trawler/pdf"
	"github.com/google/uuid"
	"github.com/miku/grobidclient/tei"
	"github.com/spf13/viper"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// dcnLinesPerCAN bounds how many records one workflow run handles before
// continue-as-new, keeping event history small over a 1.16M-line file.
const dcnLinesPerCAN = 1000

// dcnContextRe pulls the bepress host and series slug out of a sweep base_url
// (https://<host>/context/<series>/article/<n>/viewcontent/<file>.pdf). The
// series is the closest thing the data has to a container, and the article id
// is what Wayback captured the PDF under.
var dcnContextRe = regexp.MustCompile(`^https?://([^/]+)/context/([^/]+)/article/(\d+)/`)

// DCNRecord is one line of the enriched sweep file. Fields beyond those the
// ingest reads are left out; unknown JSON keys are ignored.
type DCNRecord struct {
	BaseURL      string `json:"base_url"`
	Category     string `json:"category"`
	LinkSourceID string `json:"link_source_id"`
	ExtIDs       struct {
		OAI string `json:"oai"`
		DOI string `json:"doi"`
	} `json:"ext_ids"`
	EditExtra struct {
		OAIHost    string   `json:"oai_host"`
		OAISets    []string `json:"oai_sets"`
		Title      string   `json:"title"`
		Date       string   `json:"date"`
		ISSN       string   `json:"issn"`
		ISSNSource string   `json:"issn_source"`
		// ContainerID is a fatcat container we already matched by ISSN
		// offline; when set it saves a lookup.
		ContainerID string `json:"container_id"`
		// ContainerName is set for journal-like series we could not resolve
		// to an ISSN. It goes on release.extra rather than becoming a
		// container, matching what crossref ingest does with a title and no
		// ISSN-L.
		ContainerName string `json:"container_name"`
		SeriesName    string `json:"series_name"`
	} `json:"edit_extra"`
	// OAIDC is the raw oai_dc field map (creator, description, publisher,
	// subject, ...). Values are lists because Dublin Core repeats elements.
	OAIDC map[string][]string `json:"oai_dc"`
}

type DCNIngestInput struct {
	// S3Key is the enriched sweep file (one DCNRecord per line)
	S3Key string
	// SourceOverride overrides the default source name generation
	SourceOverride string
	// TaskQueue is which temporal task queue to use
	TaskQueue string
	// Offset is the byte offset within S3Key to resume from
	Offset int64
	// Counts is the running total across continue-as-new runs
	Counts counts.Counts
}

// DCNIngestWorkflow pages through the enriched sweep file and dispatches one
// activity per record.
func DCNIngestWorkflow(ctx workflow.Context, in DCNIngestInput) (counts.Counts, error) {
	l := workflow.GetLogger(ctx)

	if in.S3Key == "" {
		return in.Counts, fmt.Errorf("S3Key is required")
	}

	source := in.SourceOverride
	if source == "" {
		now := workflow.Now(ctx).Format("20060102")
		runid := workflow.GetInfo(ctx).WorkflowExecution.RunID
		if len(runid) > 8 {
			runid = runid[:8]
		}
		source = fmt.Sprintf("periodic-dcn-sweep-%s-%s", now, runid)
	}

	findCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 8 * time.Hour,
		TaskQueue:           in.TaskQueue,
	})

	// A record can involve a Wayback CDX query, a PDF fetch, GROBID, and
	// several fatcat writes, so the per-record budget is generous; the
	// retry policy handles the transient half of that.
	recCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout:    1 * time.Hour,
		ScheduleToCloseTimeout: 4 * time.Hour,
		HeartbeatTimeout:       15 * time.Minute,
		TaskQueue:              in.TaskQueue,
		RetryPolicy: &temporal.RetryPolicy{
			BackoffCoefficient: 1.5,
			InitialInterval:    30 * time.Second,
			MaximumInterval:    90 * time.Second,
		},
	})

	findInput := harvesting.FindLineBatchInput{
		S3Key:     in.S3Key,
		Offset:    in.Offset,
		BatchSize: 10000,
		ChunkSize: 102400,
	}

	linesThisRun := 0
	for {
		var findOutput harvesting.FindLineBatchOutput
		if err := workflow.ExecuteActivity(findCtx, harvesting.FindLineBatch, findInput).Get(findCtx, &findOutput); err != nil {
			return in.Counts, err
		}

		for _, offset := range findOutput.Offsets {
			rin := ProcessDCNRecordInput{
				S3Key:       in.S3Key,
				LineStart:   offset[0],
				Length:      offset[1],
				SourceLabel: source,
			}
			var c counts.Counts
			if err := workflow.ExecuteActivity(recCtx, ProcessDCNRecordActivity, rin).Get(recCtx, &c); err != nil {
				return in.Counts, err
			}
			in.Counts = in.Counts.Add(c)
			linesThisRun++

			// Advance past the line we finished so a mid-batch
			// continue-as-new neither repeats nor skips it.
			in.Offset = offset[0] + offset[1]
			findInput.Offset = in.Offset

			if linesThisRun >= dcnLinesPerCAN || workflow.GetInfo(ctx).GetContinueAsNewSuggested() {
				l.Info(fmt.Sprintf("continue-as-new for %q at offset %d (%d lines this run)",
					in.S3Key, in.Offset, linesThisRun))
				return in.Counts, workflow.NewContinueAsNewError(ctx, DCNIngestWorkflow, in)
			}
		}

		in.Offset = findOutput.BytesRead
		findInput.Offset = findOutput.BytesRead

		if findOutput.EOF {
			l.Info(fmt.Sprintf("dcn ingest complete for %q: %#v", in.S3Key, in.Counts))
			break
		}
	}

	return in.Counts, nil
}

type ProcessDCNRecordInput struct {
	S3Key       string
	LineStart   int64
	Length      int64
	SourceLabel string
}

// ProcessDCNRecordActivity turns one sweep record into (at most) a container, a
// release, and a file, then indexes the file and its fulltext.
//
// Every step is written to be re-runnable: lookups precede creates, and an
// entity that already exists is reused rather than duplicated. Data problems
// (no PDF in Wayback, a PDF GROBID cannot read) increment a Skipped/Failed
// count and return nil, because a retry would not fix them; only genuine
// infrastructure errors are returned so Temporal retries them.
func ProcessDCNRecordActivity(ctx context.Context, in ProcessDCNRecordInput) (counts.Counts, error) {
	out := counts.Counts{}
	l := activity.GetLogger(ctx)
	client := &http.Client{Timeout: 10 * time.Minute}

	jsonline, err := harvesting.GetLine(ctx, in.S3Key, in.LineStart, in.Length)
	if err != nil {
		return out, fmt.Errorf("failed to read line in %q at %d for %d bytes: %w",
			in.S3Key, in.LineStart, in.Length, err)
	}

	var rec DCNRecord
	if err := json.Unmarshal(jsonline, &rec); err != nil {
		// A torn line costs one line, not the run.
		l.Warn("skipping unparseable record", "err", err.Error())
		out.Releases.Skipped++
		return out, nil
	}

	if rec.EditExtra.Title == "" {
		l.Warn("skipping record with no title", "oai", rec.ExtIDs.OAI)
		out.Releases.Skipped++
		return out, nil
	}

	//
	// 1. Resolve the release. A DOI that fatcat already knows means the work
	//    is catalogued and we only owe it a file; that is ~12% of the sweep,
	//    and skipping the create here is what keeps us from minting six
	//    figures of duplicates.
	//
	activity.RecordHeartbeat(ctx, "release")
	release, created, err := resolveDCNRelease(client, &out, rec, in.SourceLabel)
	if err != nil {
		return out, err
	}
	if created {
		out.Releases.Added++
	} else {
		out.Releases.Ignored++
	}

	//
	// 2. Find the PDF in Wayback. The sweep's base_url is the modern bepress
	//    download path, but captures overwhelmingly live under the older
	//    cgi/viewcontent.cgi form, so both are tried.
	//
	activity.RecordHeartbeat(ctx, "cdx")
	row, err := findDCNCapture(rec)
	if err != nil {
		return out, fmt.Errorf("cdx lookup for %q: %w", rec.BaseURL, err)
	}
	if row == nil {
		l.Info("no wayback capture", "url", rec.BaseURL, "oai", rec.ExtIDs.OAI)
		out.Pdfs.Skipped++
		return out, nil
	}

	//
	// 3. If fatcat already has this sha1, we may be able to stop early.
	//
	sha1hex := row.SHA1hex
	if sha1hex == "" && row.SHA1b32 != "" {
		if s, err := decodeSha1Base32(row.SHA1b32); err == nil {
			sha1hex = s
		}
	}

	fileInES := false
	fulltextInES := false
	var extantFid *uuid.UUID
	if sha1hex != "" {
		fileInES, err = indexing.ElasticDocExists(client,
			viper.GetString("indexing.fatcat_file_ix"), "sha1", sha1hex)
		if err != nil {
			return out, fmt.Errorf("fatcat_file existence check failed: %w", err)
		}
		fulltextInES, err = indexing.ElasticDocExists(client,
			viper.GetString("indexing.fulltext_ix"), "fulltext.file_sha1", sha1hex)
		if err != nil {
			return out, fmt.Errorf("scholar_fulltext existence check failed: %w", err)
		}
		extantFid, err = fatcat2.LookupSha1(client, sha1hex)
		if err != nil {
			return out, fmt.Errorf("could not look up sha1 in fc2: %w", err)
		}
		if fileInES && fulltextInES && extantFid != nil {
			l.Info("skipping known and indexed pdf", "sha1", sha1hex)
			out.Pdfs.Skipped++
			return out, nil
		}
	}

	//
	// 4. Fetch the bytes and run the PDF pipeline.
	//
	activity.RecordHeartbeat(ctx, "fetch-pdf")
	wbURL := toWaybackURL(row.Datetime.UTC().Format("20060102150405"), row.URL)
	pdfBs, err := fetchWaybackPDF(ctx, client, wbURL)
	if err != nil {
		return out, fmt.Errorf("wayback fetch %q: %w", wbURL, err)
	}
	if len(pdfBs) == 0 || !bytes.HasPrefix(pdfBs, []byte("%PDF")) {
		l.Warn("wayback body is not a pdf", "url", wbURL)
		out.Pdfs.Skipped++
		return out, nil
	}

	// Trust our own digest over the CDX row's; the replayed body is what we
	// are actually storing a record of.
	sum := sha1.Sum(pdfBs)
	sha1hex = hex.EncodeToString(sum[:])
	if extantFid == nil {
		extantFid, err = fatcat2.LookupSha1(client, sha1hex)
		if err != nil {
			return out, fmt.Errorf("could not look up sha1 in fc2: %w", err)
		}
	}

	processor, err := pdf.NewProcessor(func(msg string) {
		activity.RecordHeartbeat(ctx, msg)
	})
	if err != nil {
		return out, fmt.Errorf("could not create pdf processor: %w", err)
	}

	activity.RecordHeartbeat(ctx, "pdf-process")
	pdfContent, err := processor.Process(ctx, pdfBs, sha1hex)
	if err != nil {
		return out, fmt.Errorf("pdf processing failed: %w", err)
	}
	if len(pdfContent.GrobidXML) == 0 ||
		bytes.Contains(pdfContent.GrobidXML, []byte("[BAD_INPUT_DATA]")) {
		// ~17% of sweep captures fail GROBID this way. It is a property of
		// the PDF, so retrying is wasted work.
		l.Warn("grobid could not read pdf", "sha1", sha1hex, "url", wbURL)
		out.Pdfs.Failed++
		return out, nil
	}

	// Parsed purely as a validity gate: PrepareFulltextDoc takes the raw TEI
	// and cannot report a parse failure, so check it here rather than index a
	// document built from XML we could not read. The header itself is not used
	// for identity — see resolveDCNRelease.
	if _, err := tei.ParseDocument(bytes.NewReader(pdfContent.GrobidXML)); err != nil {
		return out, fmt.Errorf("bad grobid xml for %s: %w", sha1hex, err)
	}

	//
	// 5. File: attach a URL to the existing record, or create one.
	//
	activity.RecordHeartbeat(ctx, "file")
	file, err := upsertDCNFile(client, &out, extantFid, release, pdfBs, sha1hex, wbURL, in.SourceLabel)
	if err != nil {
		return out, err
	}
	if file == nil {
		return out, nil
	}

	//
	// 6. Index the file and its fulltext.
	//
	if !fileInES {
		fileDoc := indexing.PrepareFatcatFileDoc(*file)
		bs, err := json.Marshal(fileDoc)
		if err != nil {
			return out, fmt.Errorf("failed to marshal file ES doc: %w", err)
		}
		if err := indexing.DoElasticIndex(client,
			viper.GetString("indexing.fatcat_file_ix"), fileDoc.LegacyIdent, bs); err != nil {
			return out, fmt.Errorf("failed to index file: %w", err)
		}
	}

	activity.RecordHeartbeat(ctx, "file-indexed")

	if !fulltextInES {
		var container *fatcat2.Container
		if release.ContainerID != nil {
			c, err := fatcat2.GetContainer(client, *release.ContainerID)
			if err != nil {
				return out, fmt.Errorf("failed to look up container %q: %w", release.ContainerID, err)
			}
			container = &c
		}
		esDoc := indexing.PrepareFulltextDoc(indexing.FulltextTransformCtx{
			HttpClient: client,
			Release:    release,
			Container:  container,
			File:       file,
			GrobidXML:  pdfContent.GrobidXML,
			PdfText:    pdfContent.PdfText,
		})
		bs, err := json.Marshal(esDoc)
		if err != nil {
			return out, fmt.Errorf("es doc serialization failed: %w", err)
		}
		if err := indexing.DoElasticIndex(client,
			viper.GetString("indexing.fulltext_ix"), esDoc.Key, bs); err != nil {
			return out, fmt.Errorf("failed to full text ingest %q: %w", file.ID, err)
		}
		out.Releases.Ingested++
	}

	out.Pdfs.Processed++
	return out, nil
}

// resolveDCNRelease finds the release this record belongs to, creating it from
// the sweep's OAI metadata when fatcat does not already have it. The bool
// reports whether a release was created.
//
// The OAI metadata is preferred over the GROBID header because it is
// depositor-supplied rather than parsed off the page: on this corpus GROBID
// recovers a DOI for only ~63% of records that have one and essentially no
// journal titles.
func resolveDCNRelease(client *http.Client, out *counts.Counts, rec DCNRecord, source string) (fatcat2.Release, bool, error) {
	doi := cleaning.NormalizeDOI(rec.ExtIDs.DOI)
	if doi != "" {
		rid, err := fatcat2.LookupRelease(client, "doi", doi)
		if err != nil {
			return fatcat2.Release{}, false, fmt.Errorf("fc2 release lookup failed: %w", err)
		}
		if rid != nil {
			r, err := fatcat2.GetRelease(client, *rid)
			if err != nil {
				return fatcat2.Release{}, false, fmt.Errorf("could not get release %q: %w", rid, err)
			}
			return r, false, nil
		}
	}

	rid, err := uuid.NewV7()
	if err != nil {
		return fatcat2.Release{}, false, fmt.Errorf("uuid creation failed: %w", err)
	}

	release := fatcat2.Release{
		ID:       rid,
		Title:    cleaning.CleanString(cleaning.DeTag(rec.EditExtra.Title)),
		Type:     dcnReleaseType(rec),
		Stage:    "published",
		Source:   source,
		Extra:    map[string]any{},
		Contribs: []fatcat2.ReleaseContrib{},
		ExternalIDs: []fatcat2.ExternalID{
			{ReleaseID: &rid, Type: "oai", Value: rec.ExtIDs.OAI},
		},
	}
	if doi != "" {
		release.ExternalIDs = append(release.ExternalIDs,
			fatcat2.ExternalID{ReleaseID: &rid, Type: "doi", Value: doi})
	}
	if pub := firstDC(rec.OAIDC, "publisher"); pub != "" {
		release.Publisher = pub
	}
	if lang := firstDC(rec.OAIDC, "language"); lang != "" {
		release.Language = cleaning.NormalizeLanguage(lang)
	}
	setDCNDate(&release, rec.EditExtra.Date)

	for i, name := range rec.OAIDC["creator"] {
		name = cleaning.CleanString(name)
		if name == "" {
			continue
		}
		release.Contribs = append(release.Contribs, fatcat2.ReleaseContrib{
			ReleaseID: &rid,
			RawName:   name,
			Position:  i,
			Role:      "author",
		})
	}

	// Provenance for a one-off import that creates a lot of records.
	release.Extra["oai_host"] = rec.EditExtra.OAIHost
	if len(rec.EditExtra.OAISets) > 0 {
		release.Extra["oai_sets"] = rec.EditExtra.OAISets
	}

	cid, err := resolveDCNContainer(client, out, rec, source)
	if err != nil {
		return fatcat2.Release{}, false, err
	}
	release.ContainerID = cid
	if cid == nil && rec.EditExtra.ContainerName != "" {
		// Named but unlinked, the same fallback crossref ingest uses when it
		// has a title and no ISSN-L.
		release.Extra["container_name"] = rec.EditExtra.ContainerName
		out.Containers.Skipped++
	}

	newID, err := fatcat2.CreateRelease(client, release)
	if err != nil {
		return fatcat2.Release{}, false, fmt.Errorf("failed to create release for %q: %w", rec.ExtIDs.OAI, err)
	}

	hydrated, err := fatcat2.GetRelease(client, *newID)
	if err != nil {
		return fatcat2.Release{}, false, fmt.Errorf("failed to fetch new release %q: %w", newID, err)
	}

	releaseDoc, err := indexing.PrepareFatcatReleaseDoc(client, hydrated)
	if err != nil {
		return fatcat2.Release{}, false, fmt.Errorf("failed to transform release %q: %w", hydrated.ID, err)
	}
	bs, err := json.Marshal(releaseDoc)
	if err != nil {
		return fatcat2.Release{}, false, fmt.Errorf("failed to marshal release es doc: %w", err)
	}
	if err := indexing.DoElasticIndex(client,
		viper.GetString("indexing.fatcat_release_ix"), releaseDoc.LegacyIdent, bs); err != nil {
		return fatcat2.Release{}, false, fmt.Errorf("failed to index release %q: %w", hydrated.ID, err)
	}

	return hydrated, true, nil
}

// resolveDCNContainer maps a record to a fatcat container, creating one only
// when we have both a name and an ISSN — an ISSN-less container has no lookup
// key in the v2 API and could never be found again.
func resolveDCNContainer(client *http.Client, out *counts.Counts, rec DCNRecord, source string) (*uuid.UUID, error) {
	// Offline matching already resolved most of these; trust it.
	if rec.EditExtra.ContainerID != "" {
		id, err := uuid.Parse(rec.EditExtra.ContainerID)
		if err == nil {
			out.Containers.Ignored++
			return &id, nil
		}
	}

	raw := strings.ToUpper(strings.TrimSpace(rec.EditExtra.ISSN))
	if raw == "" {
		return nil, nil
	}
	issnl := issn.ISSN2ISSNL(raw)
	if issnl == "" {
		issnl = raw
	}

	cid, err := fatcat2.LookupIssnl(client, issnl)
	if err != nil {
		return nil, fmt.Errorf("container lookup for %q: %w", issnl, err)
	}
	if cid != nil {
		out.Containers.Ignored++
		return cid, nil
	}

	name := rec.EditExtra.ContainerName
	if name == "" {
		name = rec.EditExtra.SeriesName
	}
	if name == "" {
		out.Containers.Skipped++
		return nil, nil
	}

	c := fatcat2.Container{
		Name:      cleaning.CleanString(cleaning.DeTag(name)),
		ISSNL:     issnl,
		Publisher: firstDC(rec.OAIDC, "publisher"),
		Type:      "journal",
		Source:    source,
	}
	cid, err = fatcat2.CreateContainer(client, &c)
	if err != nil {
		return nil, fmt.Errorf("could not create container %q: %w", c.Name, err)
	}
	c.ID = *cid
	out.Containers.Added++

	containerDoc := indexing.PrepareFatcatContainerDoc(c)
	bs, err := json.Marshal(containerDoc)
	if err != nil {
		return nil, fmt.Errorf("could not marshal container doc: %w", err)
	}
	if err := indexing.DoElasticIndex(client,
		viper.GetString("indexing.fatcat_container_ix"), containerDoc.LegacyIdent, bs); err != nil {
		return nil, fmt.Errorf("could not index container %q: %w", c.ID, err)
	}

	return cid, nil
}

// upsertDCNFile attaches wbURL to an existing file record for this sha1, or
// creates a new one pointing at the release. A nil file with a nil error means
// the record was counted and needs no further work.
func upsertDCNFile(client *http.Client, out *counts.Counts, extantFid *uuid.UUID,
	release fatcat2.Release, pdfBs []byte, sha1hex, wbURL, source string) (*fatcat2.File, error) {

	if extantFid != nil {
		f, err := fatcat2.GetFile(client, *extantFid)
		if err != nil {
			return nil, fmt.Errorf("could not get file %q: %w", extantFid, err)
		}
		for _, u := range f.URLs {
			if u.URL == wbURL {
				return &f, nil
			}
		}
		newURL := fatcat2.FileURL{URL: wbURL, Rel: "webarchive", FileID: f.ID}
		added, err := fatcat2.AddFileURL(client, f.ID, newURL)
		if err != nil {
			return nil, fmt.Errorf("could not update file %q with url %q: %w", f.ID, wbURL, err)
		}
		if added {
			f.URLs = append(f.URLs, newURL)
		}
		return &f, nil
	}

	fid, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("uuid creation failed: %w", err)
	}
	file := &fatcat2.File{
		ID:       fid,
		Releases: []fatcat2.Release{release},
		Mimetype: "application/pdf",
		Source:   source,
		URLs:     []fatcat2.FileURL{{Rel: "webarchive", URL: wbURL, FileID: fid}},
	}
	if err := file.SetMetadata(pdfBs); err != nil {
		return nil, fmt.Errorf("could not compute pdf metadata: %w", err)
	}
	if _, err := fatcat2.CreateFile(client, file); err != nil {
		return nil, fmt.Errorf("failed to create file %q: %w", file.Sha1, err)
	}
	out.Releases.Acquired++
	return file, nil
}

// findDCNCapture looks for an archived PDF for a sweep record. The base_url is
// bepress's modern download path; Wayback's captures are almost entirely under
// the older cgi/viewcontent.cgi?article=N&context=SERIES form, which is
// derivable from the same url, so both are queried.
func findDCNCapture(rec DCNRecord) (*cdxclient.CDXRow, error) {
	c := cdxclient.NewClient(cdxclient.Config{
		Endpoint:  viper.GetString("cdx.endpoint"),
		Auth:      viper.GetString("cdx.auth"),
		UserAgent: viper.GetString("cdx.user_agent"),
	})

	candidates := []string{rec.BaseURL}
	if m := dcnContextRe.FindStringSubmatch(rec.BaseURL); m != nil {
		candidates = append(candidates,
			fmt.Sprintf("https://%s/cgi/viewcontent.cgi?article=%s&context=%s", m[1], m[3], m[2]))
	}

	for _, u := range candidates {
		rows, err := c.Query(cdxclient.QueryParams{
			URL:       u,
			MatchType: "exact",
			Limit:     5,
			Filters:   []string{"statuscode:200", "mimetype:application/pdf"},
		})
		if err != nil {
			return nil, err
		}
		if len(rows) > 0 {
			return &rows[0], nil
		}
	}
	return nil, nil
}

// fetchWaybackPDF pulls the raw archived bytes (id_ replay, no Wayback
// rewriting) for a capture.
func fetchWaybackPDF(ctx context.Context, client *http.Client, wbURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", wbURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// dcnReleaseType maps what the sweep knows about an item to a fatcat release
// type. Most of the corpus is article-shaped; theses are the one large
// exception worth typing correctly.
func dcnReleaseType(rec DCNRecord) string {
	switch rec.Category {
	case "thesis":
		return "thesis"
	case "collection":
		return "document"
	}
	for _, t := range rec.OAIDC["type"] {
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "thesis", "dissertation":
			return "thesis"
		case "book":
			return "book"
		}
	}
	return "article-journal"
}

// setDCNDate fills in release date/year from an OAI datestamp
// (2015-01-01T08:00:00Z). The sweep has a tail of nonsense years, so anything
// outside a plausible range is left unset rather than asserted.
func setDCNDate(release *fatcat2.Release, raw string) {
	if raw == "" {
		return
	}
	release.Extra["raw_date"] = raw
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		if len(raw) >= 10 {
			t, err = time.Parse("2006-01-02", raw[:10])
		}
		if err != nil {
			return
		}
	}
	if t.Year() < 1500 || t.Year() > time.Now().Year()+1 {
		return
	}
	rd := fatcat2.ReleaseDate(t)
	release.ReleaseDate = &rd
	release.ReleaseYear = t.Year()
}

// firstDC returns the first value for a Dublin Core element, or "".
func firstDC(m map[string][]string, key string) string {
	if vs := m[key]; len(vs) > 0 {
		return cleaning.CleanString(vs[0])
	}
	return ""
}
