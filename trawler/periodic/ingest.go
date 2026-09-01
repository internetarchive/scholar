package periodic

/*
This package is like `daily` but for the ingestion of PDFs from warcs on
petabox instead of the live web. We complement the daily crawling with periodic
heritrix (or whatever) based wide crawls in the hope of capturing PDFs.

The entry workflow is PeriodicIngestWorkflow. It pages through a crawl collection on petabox, pulls CDX files, then reads and processes any PDFs within.
*/

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"git.archive.org/webgroup/scholar/trawler/cdx/cdxfile"
	"git.archive.org/webgroup/scholar/trawler/cleaning"
	"git.archive.org/webgroup/scholar/trawler/fatcat2"
	"git.archive.org/webgroup/scholar/trawler/harvesting"
	"git.archive.org/webgroup/scholar/trawler/ia"
	"git.archive.org/webgroup/scholar/trawler/indexing"
	"git.archive.org/webgroup/scholar/trawler/pdf"
	"git.archive.org/webgroup/scholar/trawler/s3"
	"github.com/google/uuid"
	warc "github.com/internetarchive/gowarc"
	"github.com/miku/grobidclient/tei"
	"github.com/spf13/viper"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

const (
	itemsPerPage = 100
)

// ErrTruncatedCapture marks a WARC record the crawler flagged as truncated
// (WARC-Truncated header): the stored payload is incomplete, so callers skip
// the row rather than treating the short HTTP body as a hard error.
var ErrTruncatedCapture = errors.New("warc capture truncated at crawl time")

type PeriodicCounts struct {
	// Total is number of PDF CDX line in a collection
	Lines int
	// PdfsSkipped is how many PDF lines we passed on without adding to any DB or index
	PdfsSkipped int
	// FilesUpdated is how many existing file records we added a URL to
	FilesUpdated int
	// PdfsFailed is how many PDFs grobid choked on
	PdfsFailed int
	// PdfsAddedToDB is how many new file records we created
	PdfsAddedToDB int
	//PdfsAddedToES is how many PDFs we added to fatcat's file index
	PdfsAddedToES int
	// FulltextAddedToES is how many PDFs whose full text we ingested into ES
	FulltextAddedToES int
}

func (c PeriodicCounts) Add(oc PeriodicCounts) PeriodicCounts {
	return PeriodicCounts{
		Lines:             c.Lines + oc.Lines,
		PdfsSkipped:       c.PdfsSkipped + oc.PdfsSkipped,
		PdfsAddedToDB:     c.PdfsAddedToDB + oc.PdfsAddedToDB,
		PdfsAddedToES:     c.PdfsAddedToES + oc.PdfsAddedToES,
		PdfsFailed:        c.PdfsFailed + oc.PdfsFailed,
		FulltextAddedToES: c.FulltextAddedToES + oc.FulltextAddedToES,
		FilesUpdated:      c.FilesUpdated + oc.FilesUpdated,
	}
}

type PeriodicIngestInput struct {
	// CollectionName is which petabox collection of warcs/cdx to ingest
	CollectionName string
	// SourceOverride overrides the default source name generation
	SourceOverride string
	// TaskQueue is which temporal task queue to use
	TaskQueue string
	// ItemIdMap is a dictionary mapping item IDs to s3keys where their PDF CDX lines can be found
	ItemIdMap map[string]string
	// ItemId is the current item whence cdx lines are being pulled
	ItemId string
	// Offset is the offset within the current item's cdx lines
	Offset int64
	// Counts is the running total of pdf lines processed
	Counts PeriodicCounts
}

func PeriodicIngestWorkflow(ctx workflow.Context, in PeriodicIngestInput) (PeriodicCounts, error) {
	l := workflow.GetLogger(ctx)
	taskQueue := in.TaskQueue
	source := in.SourceOverride
	if source == "" {
		now := workflow.Now(ctx).Format("20060102")
		runid := workflow.GetInfo(ctx).WorkflowExecution.RunID
		if len(runid) > 8 {
			runid = runid[:8]
		}
		source = fmt.Sprintf("periodic-%s-%s-%s", now, runid, in.CollectionName)
	}

	itemIds := []string{}

	if len(in.ItemIdMap) == 0 {
		in.ItemIdMap = map[string]string{}
		ao := workflow.ActivityOptions{
			StartToCloseTimeout: 2 * time.Hour,
			TaskQueue:           taskQueue,
			HeartbeatTimeout:    10 * time.Minute,
			RetryPolicy: &temporal.RetryPolicy{
				BackoffCoefficient: 1.5,
				InitialInterval:    30 * time.Second,
				MaximumInterval:    180 * time.Second,
			},
		}
		var listOut ListCollectionOutput
		err := workflow.ExecuteActivity(
			workflow.WithActivityOptions(ctx, ao),
			ListCollectionActivity,
			ListCollectionInput{CollectionName: in.CollectionName}).Get(ctx, &listOut)
		if err != nil {
			return in.Counts, err
		}

		for _, itemId := range listOut.ItemIds {
			var s3key string
			err = workflow.ExecuteActivity(
				workflow.WithActivityOptions(ctx, ao),
				CacheItemPdfActivity,
				CacheItemPdfActivityInput{CollectionName: in.CollectionName, ItemId: itemId},
			).Get(ctx, &s3key)
			if err != nil {
				return in.Counts, err
			}
			in.ItemIdMap[itemId] = s3key
		}
	}

	for k := range maps.Keys(in.ItemIdMap) {
		itemIds = append(itemIds, k)
	}
	slices.Sort(itemIds)

	if len(itemIds) == 0 {
		l.Warn("found no items in collection, exiting")
		return in.Counts, nil
	}

	linesPerCAN := 1000
	linesThisRun := 0
	findCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 8 * 60 * 60 * time.Second,
		TaskQueue:           taskQueue,
	})

	if in.ItemId == "" {
		in.ItemId = itemIds[0]
	}

	currentItemIx := slices.Index(itemIds, in.ItemId)
	if currentItemIx == -1 {
		l.Warn("item id not found in list", "itemId", in.ItemId)
		return in.Counts, nil
	}
	left := itemIds[currentItemIx:]

	lCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout:    4 * time.Hour,
		ScheduleToCloseTimeout: 8 * time.Hour,
		HeartbeatTimeout:       20 * time.Minute,
		TaskQueue:              taskQueue,
		RetryPolicy: &temporal.RetryPolicy{
			BackoffCoefficient: 1.5,
			InitialInterval:    30 * time.Second,
			MaximumInterval:    90 * time.Second,
		},
	})

	for _, itemId := range left {
		in.ItemId = itemId
		for {
			findInput := harvesting.FindLineBatchInput{
				S3Key:     in.ItemIdMap[itemId],
				Offset:    in.Offset,
				BatchSize: 10000,
				ChunkSize: 102400,
			}
			var findOutput harvesting.FindLineBatchOutput
			if err := workflow.ExecuteActivity(findCtx, harvesting.FindLineBatch, findInput).Get(findCtx, &findOutput); err != nil {
				return in.Counts, err
			}
			in.Counts.Lines += len(findOutput.Offsets)
			for _, offset := range findOutput.Offsets {
				lin := ProcessPdfLineInput{
					S3Key:          in.ItemIdMap[itemId],
					LineStart:      offset[0],
					Length:         offset[1],
					CollectionName: in.CollectionName,
					ItemId:         in.ItemId,
					SourceLabel:    source,
				}
				var c PeriodicCounts
				if err := workflow.ExecuteActivity(lCtx, ProcessPdfLineActivity, lin).Get(lCtx, &c); err != nil {
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

				if linesThisRun >= linesPerCAN || workflow.GetInfo(ctx).GetContinueAsNewSuggested() {
					l.Info(fmt.Sprintf("continue-as-new for '%s/%s' at offset %d (%d lines this run)",
						in.CollectionName, in.ItemId, in.Offset, linesThisRun))
					return in.Counts, workflow.NewContinueAsNewError(ctx, PeriodicIngestWorkflow, in)
				}
			}

			in.Offset = findOutput.BytesRead
			findInput.Offset = findOutput.BytesRead

			if findOutput.EOF {
				l.Info(fmt.Sprintf("item %q complete for coll %q: %#v",
					in.ItemId, in.CollectionName, in.Counts))
				in.Offset = 0
				break
			}
		}
	}

	return in.Counts, nil
}

type ListCollectionInput struct {
	CollectionName string
}

type ListCollectionOutput struct {
	ItemIds []string
}

func ListCollectionActivity(ctx context.Context, in ListCollectionInput) (ListCollectionOutput, error) {
	out := ListCollectionOutput{
		ItemIds: []string{},
	}
	client := &http.Client{Timeout: 120 * time.Second}
	page := 1

	for {
		ids, hasMore, err := ia.SearchCollection(
			ctx, client, in.CollectionName, page, itemsPerPage)
		if err != nil {
			return out, fmt.Errorf("failed to search '%s': %w", in.CollectionName, err)
		}

		for _, id := range ids {
			if !strings.HasSuffix(id, "-CRL") {
				out.ItemIds = append(out.ItemIds, id)
			}
		}

		if !hasMore {
			break
		}

		page += 1
	}

	return out, nil
}

// activity: for an item, write just its pdf cdx lines to s3
// use find line batch to get X lines
// start processing lines, using CAN every Y lines
// in-memory worker cache for shas since i'm seeing shas duplicate across URLs
//

type CacheItemPdfActivityInput struct {
	CollectionName string
	ItemId         string
}

func CacheItemPdfActivity(ctx context.Context, in CacheItemPdfActivityInput) (string, error) {
	client := &http.Client{Timeout: 10 * time.Minute}
	//
	// pull petabox item file list, get its CDX file, filter the CDX down to pdfs.
	//
	activity.RecordHeartbeat(ctx, "metadata")
	files, err := ia.ItemFiles(ctx, client, in.ItemId)
	if err != nil {
		return "", fmt.Errorf("metadata for %s: %w", in.ItemId, err)
	}
	rollup, err := ia.FindRollupCDX(files)
	if err != nil {
		return "", fmt.Errorf("find rollup CDX for %s: %w", in.ItemId, err)
	}

	activity.RecordHeartbeat(ctx, "download-cdx")
	rdr, err := ia.OpenFile(ctx, client, in.ItemId, rollup)
	if err != nil {
		return "", fmt.Errorf("open rollup CDX %s/%s: %w", in.ItemId, rollup, err)
	}
	defer rdr.Close()

	activity.RecordHeartbeat(ctx, "parse-cdx")
	pdfLines, err := cdxfile.Parse(rdr, cdxfile.PDFFilter)
	if err != nil {
		return "", fmt.Errorf("parse rollup CDX %s/%s: %w", in.ItemId, rollup, err)
	}

	jsonl := []byte{}
	for _, line := range pdfLines {
		jl, err := json.Marshal(line)
		if err != nil {
			return "", fmt.Errorf("failed to encode cdx row '%#v': %w", line, err)
		}
		for _, c := range jl {
			jsonl = append(jsonl, c)
		}
		jsonl = append(jsonl, '\n')
	}
	s3key := fmt.Sprintf("sandcrawler/pdfcdx/%s/%s", in.CollectionName, in.ItemId)
	err = s3.PutObject(ctx, s3key, jsonl, "application/x-ndjson")
	if err != nil {
		return "", fmt.Errorf("failed to write pdf cdx to s3 key '%s': %w", s3key, err)
	}

	return s3key, nil
}

type ProcessPdfLineInput struct {
	S3Key          string
	LineStart      int64
	Length         int64
	CollectionName string
	ItemId         string
	SourceLabel    string
}

func ProcessPdfLineActivity(ctx context.Context, in ProcessPdfLineInput) (PeriodicCounts, error) {
	out := PeriodicCounts{}
	l := activity.GetLogger(ctx)
	client := &http.Client{Timeout: 10 * time.Minute}
	jsonline, err := harvesting.GetLine(ctx, in.S3Key, in.LineStart, in.Length)
	if err != nil {
		return out, fmt.Errorf("failed to read line in %q at %d for %d bytes: %w",
			in.S3Key, in.LineStart, in.Length, err)
	}

	var pdfLine cdxfile.Row
	err = json.Unmarshal(jsonline, &pdfLine)
	if err != nil {
		return out, fmt.Errorf("failed to parse %q: %w", string(jsonline), err)
	}

	l.Debug("handling pdf line", "line", pdfLine)
	sha1, err := decodeSha1Base32(pdfLine.Sha1Base32)
	if err != nil {
		l.Warn("skipping row with bad sha1", "sha1_b32", pdfLine.Sha1Base32, "err", err.Error())
		out.PdfsSkipped++
		return out, nil
	}

	l.Debug("decoded sha1", "orig_sha1", pdfLine.Sha1Base32, "decoded_sha1", sha1)

	//
	// the conditions that mean we can avoid petabox read:
	// 1. sha1 is in fatcat file index
	// 2. sha1 is in full text index
	// 3. file record exists already in DB and is connected to a release with an external ID
	//
	// we start here because petabox reads are expensive.
	//
	fileInES, err := indexing.ElasticDocExists(client,
		viper.GetString("indexing.fatcat_file_ix"), "sha1", sha1)
	if err != nil {
		return out, fmt.Errorf("fatcat_file existence check failed: %w", err)
	}

	fulltextInES, err := indexing.ElasticDocExists(client,
		viper.GetString("indexing.fulltext_ix"), "fulltext.file_sha1", sha1)
	if err != nil {
		return out, fmt.Errorf("scholar_fulltext existence check failed: %w", err)
	}

	extantFid, err := fatcat2.LookupSha1(client, sha1)
	if err != nil {
		return out, fmt.Errorf("could not look up sha1 in fc2: %w", err)
	}

	extantFileReleases := []fatcat2.Release{}
	var extantFileHasParent bool

	if extantFid != nil {
		extantFileReleases, err = fatcat2.FileReleases(client, *extantFid)
		if err != nil {
			return out, fmt.Errorf("could not get releases for '%s': %w", extantFid, err)
		}
		if len(extantFileReleases) > 0 {
			extidCount := 0
			for _, rel := range extantFileReleases {
				extidCount += len(rel.ExternalIDs)
			}
			if extidCount > 0 {
				extantFileHasParent = true
			}
		}
	}

	if fileInES && fulltextInES && extantFileHasParent {
		l.Info("skipping known and indexed sha1", "sha1_b32", pdfLine.Sha1Base32, "fid", extantFid)
		out.PdfsSkipped++
		return out, nil
	}

	//
	// get PDF bytes from petabox, run through grobid, parse Grobid XML.
	//
	activity.RecordHeartbeat(ctx, "range-read")
	warcItem, warcFile := pdfLine.WARCItemAndFile()
	if warcItem == "" {
		warcItem = in.ItemId
	}
	raw, err := ia.ReadRange(ctx, client, warcItem, warcFile, pdfLine.WarcOffset, pdfLine.WarcSize)
	if err != nil {
		return out, fmt.Errorf("range read failed: %w", err)
	}

	activity.RecordHeartbeat(ctx, "pdf-extract")
	pdfBs, err := extractWARCPayload(raw)
	if errors.Is(err, ErrTruncatedCapture) {
		// Expected data condition, not a failure: the crawler stored only
		// part of this PDF. Skip and move on. Any other extraction error
		// stays fatal so a real bug still surfaces.
		l.Warn("skipping truncated capture",
			"url", pdfLine.URL, "sha1_b32", pdfLine.Sha1Base32, "reason", err.Error())
		out.PdfsSkipped++
		return out, nil
	}
	if err != nil {
		return out, fmt.Errorf("could not extract pdf bytes from warc: %w", err)
	}

	processor, err := pdf.NewProcessor(func(msg string) {
		activity.RecordHeartbeat(ctx, msg)
	})
	if err != nil {
		return out, fmt.Errorf("could not create pdf processor: %w", err)
	}

	activity.RecordHeartbeat(ctx, "pdf-process")
	pdfContent, err := processor.Process(ctx, pdfBs, sha1)
	if err != nil {
		return out, fmt.Errorf("pdf processing failed: %w", err)
	}

	if len(pdfContent.GrobidXML) == 0 {
		l.Warn("empty grobid XML indicates bad pdf", "sha1", sha1)
		out.PdfsFailed++
		return out, nil
	}

	gdoc, err := tei.ParseDocument(bytes.NewReader(pdfContent.GrobidXML))
	if err != nil {
		l.Warn("failed to parse grobid xml", "sha1", sha1, "error", err.Error(), "tei", pdfContent.GrobidXML)
		// now that we have non-200s from grobid as errors in Process we should
		// consider this a hard failure worthy of human intervention
		return out, fmt.Errorf("bad grobid xml: %w", err)
	}

	extIds := findExtIds(*gdoc)
	if len(extIds) == 0 {
		l.Warn("skipping parsed PDF with no external ID", "sha1_b32", pdfLine.Sha1Base32)
		out.PdfsSkipped++
		return out, nil
	}

	var rid *uuid.UUID
	for _, pair := range extIds {
		idType := pair[0]
		idVal := pair[1]
		rid, err = fatcat2.LookupRelease(client, idType, idVal)
		if err != nil {
			return out, fmt.Errorf("fc2 release lookup failed: %w", err)
		}

		if rid != nil {
			break
		}
	}

	if rid == nil {
		l.Warn("skipping parsed PDF with no matching release", "sha1_b32", pdfLine.Sha1Base32)
		// TODO we might add releases in the future; see grobidToRelease
		out.PdfsSkipped++
		return out, nil
	}

	release, err := fatcat2.GetRelease(client, *rid)
	if err != nil {
		return out, fmt.Errorf("could not get release '%s': %w", rid, err)
	}

	// The fatcat file URL must point at the archived copy, not the live-web
	// URL from the CDX row (pdfLine.URL). Build the wayback replay URL from
	// the capture timestamp + original URL.
	wbURL := toWaybackURL(pdfLine.Timestamp, pdfLine.URL)

	var fid uuid.UUID
	var file *fatcat2.File
	if extantFid != nil {
		fid = *extantFid
		f, err := fatcat2.GetFile(client, fid)
		if err != nil {
			return out, fmt.Errorf("could not get file '%s': %w", fid, err)
		}
		file = &f
		var foundUrl bool
		for _, u := range file.URLs {
			if u.URL == wbURL {
				foundUrl = true
			}
		}
		if !foundUrl {
			_, err = fatcat2.AddFileURL(client, fid, fatcat2.FileURL{
				URL:    wbURL,
				Rel:    "webarchive",
				FileID: fid,
			})
			if err != nil {
				return out, fmt.Errorf("could not update fid '%s' with url '%s': %w", fid, wbURL, err)
			}
			// we want to trigger a re-ingest if we added a url for this file
			fileInES = false
			out.FilesUpdated++
		}
		// `extantFileReleases` contains any releases we found connected to a file
		// record with the found sha1; `release` is a release record we found
		// looking up the external ID we found in the parsed grobid. `release`
		// *should* be in `extantFileReleases` but it's not guaranteed. Ideally,
		// these different releases would be merged but for now we just log about
		// it.
		var foundRelease bool
		for _, r := range extantFileReleases {
			if r.ID == release.ID {
				foundRelease = true
			}
		}
		if !foundRelease && len(extantFileReleases) > 0 {
			l.Warn("found probable dupe releases", "fid", fid,
				"ridBySha1", extantFileReleases[0].ID, "ridByExtId", release.ID)
		}
	} else {
		fid, err = uuid.NewV7()
		if err != nil {
			return out, fmt.Errorf("uuid creation failed: %w", err)
		}
		file = &fatcat2.File{
			ID:       fid,
			Releases: []fatcat2.Release{release},
			Mimetype: "application/pdf",
			Source:   in.SourceLabel,
			URLs: []fatcat2.FileURL{
				{
					Rel:    "webarchive",
					URL:    wbURL,
					FileID: fid,
				},
			},
		}
		if err = file.SetMetadata(pdfBs); err != nil {
			return out, fmt.Errorf("could not compute pdf metadata: %w", err)
		}
		_, err = fatcat2.CreateFile(client, file)
		if err != nil {
			return out, fmt.Errorf("failed to create file '%s': %w", file.Sha1, err)
		}
		l.Info("created file", "fid", file.ID, "rid", release.ID)
		out.PdfsAddedToDB++
	}

	//
	// check on sha1 in the two relevant ES indices. We're particular about
	// this for idempotency.
	//
	if !fileInES {
		fileDoc := indexing.PrepareFatcatFileDoc(*file)
		bs, err := json.Marshal(fileDoc)
		if err != nil {
			return out, fmt.Errorf("failed to marshal file ES doc: %w", err)
		}
		err = indexing.DoElasticIndex(client,
			viper.GetString("indexing.fatcat_file_ix"), fileDoc.LegacyIdent, bs)
		if err != nil {
			return out, fmt.Errorf("failed to index file: %w", err)
		}
		out.PdfsAddedToES++
	}

	activity.RecordHeartbeat(ctx, "file-indexed")

	if !fulltextInES {
		var container *fatcat2.Container
		if release.ContainerID != nil {
			c, err := fatcat2.GetContainer(client, *release.ContainerID)
			if err != nil {
				return out, fmt.Errorf("failed to look up container '%s': %w", release.ContainerID, err)
			}
			container = &c
		}
		tctx := indexing.FulltextTransformCtx{
			HttpClient: client,
			Release:    release,
			Container:  container,
			File:       file,
			GrobidXML:  pdfContent.GrobidXML,
			PdfText:    pdfContent.PdfText,
		}

		esDoc := indexing.PrepareFulltextDoc(tctx)

		bs, err := json.Marshal(esDoc)
		if err != nil {
			return out, fmt.Errorf("es doc serialization failed: %w", err)
		}

		err = indexing.DoElasticIndex(client, viper.GetString("indexing.fulltext_ix"), esDoc.Key, bs)
		if err != nil {
			return out, fmt.Errorf("failed to full text ingest '%s': %w", fid, err)
		}
		out.FulltextAddedToES++
	}

	return out, nil
}

// findExtIds determines which external IDs we care about are defined on a
// grobid document. We return a list of pairs so we can prioritize which ones
// we later look up a release for.
func findExtIds(gdoc tei.GrobidDocument) [][]string {
	out := [][]string{}
	pairs := [][]string{
		[]string{"doi", gdoc.Header.DOI},
		[]string{"pmid", gdoc.Header.PMID},
		[]string{"pmcid", gdoc.Header.PMCID},
		[]string{"arxiv", gdoc.Header.ArxivID},
	}
	for _, pair := range pairs {
		idType := pair[0]
		idVal := pair[1]
		if idVal != "" {
			out = append(out, []string{idType, idVal})
		} else {
			continue
		}
	}
	return out
}

// grobidToRelease converts what metadata we extract from a PDF into a fatcat2
// release. This is currently unused as the quality of this release is stubby
// and at this time we aren't handling release updates in the daily crawl. If
// the daily crawl is modified to notice when a release could benefit from the
// addition of new metadata from an upstream source we can make use of this
// function when ingesting CDX.
func grobidToRelease(client *http.Client, source string, gdoc *tei.GrobidDocument) (fatcat2.Release, error) {
	rid, err := uuid.NewV7()
	if err != nil {
		return fatcat2.Release{}, fmt.Errorf("failed making uuid: %w", err)
	}

	out := fatcat2.Release{
		ID:          rid,
		Contribs:    []fatcat2.ReleaseContrib{},
		ExternalIDs: []fatcat2.ExternalID{},
		Extra:       map[string]any{},
		Language:    cleaning.NormalizeLanguage(gdoc.LanguageCode),
		Publisher:   gdoc.Header.Publisher,
		Volume:      gdoc.Header.Volume,
		Pages:       gdoc.Header.Pages,
		Issue:       gdoc.Header.Issue,
		Title:       gdoc.Header.Title,
		Type:        "article-journal",
		Source:      source,
	}
	if gdoc.Header.DOI != "" {
		out.ExternalIDs = append(out.ExternalIDs, fatcat2.ExternalID{
			Type: "doi", Value: gdoc.Header.DOI,
		})
	}
	if gdoc.Header.PMID != "" {
		out.ExternalIDs = append(out.ExternalIDs, fatcat2.ExternalID{
			Type: "pmid", Value: gdoc.Header.PMID,
		})
	}
	if gdoc.Header.PMCID != "" {
		out.ExternalIDs = append(out.ExternalIDs, fatcat2.ExternalID{
			Type: "PMCID", Value: gdoc.Header.PMCID,
		})
	}
	if gdoc.Header.ArxivID != "" {
		out.ExternalIDs = append(out.ExternalIDs, fatcat2.ExternalID{
			Type: "arxiv", Value: gdoc.Header.ArxivID,
		})
		out.Stage = "submitted"
	}

	if out.Stage == "" {
		out.Stage = "published"
	}

	// TODO should i handle ISSN in header?

	if len(gdoc.Header.Date) == 10 {
		d, err := time.Parse("2006-01-02", gdoc.Header.Date)
		if err == nil {
			rd := fatcat2.ReleaseDate(d)
			out.ReleaseDate = &rd
		}
	} else if len(gdoc.Header.Date) > 4 {
		yearPrefixRe := regexp.MustCompile("^[0-9]{4}-")
		if yearPrefixRe.MatchString(gdoc.Header.Date) {
			year, err := strconv.Atoi(gdoc.Header.Date[0:4])
			if err == nil {
				out.ReleaseYear = year
			}
		}
	}

	out.Extra["raw_date"] = gdoc.Header.Date

	for i, author := range gdoc.Header.Authors {
		var aid *uuid.UUID
		var err error
		if author.ORCID != "" {
			aid, err = fatcat2.LookupOrcid(client, author.ORCID)
			if err != nil {
				return out, fmt.Errorf("failed to look up author '%s': %w", author.ORCID, err)
			}
			if aid == nil {
				creator := fatcat2.Creator{
					DisplayName: author.FullName,
					GivenName:   author.GivenName,
					Surname:     author.Surname,
					Source:      source,
					Orcid:       cleaning.NormalizeOrcid(author.ORCID),
				}
				aid, err = fatcat2.CreateCreator(client, &creator)
				if err != nil {
					return out, fmt.Errorf("could not create creator '%s': %w", creator.Orcid, err)
				}
				creator.ID = *aid
			}
		}
		rawAffiliation := author.Affiliation.Institution
		if author.Affiliation.Department != "" {
			rawAffiliation += " " + author.Affiliation.Department
		}
		contrib := fatcat2.ReleaseContrib{
			ReleaseID:      &out.ID,
			CreatorID:      aid,
			RawName:        author.FullName,
			GivenName:      author.GivenName,
			Surname:        author.Surname,
			Position:       i,
			Role:           "author",
			RawAffiliation: rawAffiliation,
		}
		out.Contribs = append(out.Contribs, contrib)
	}

	if gdoc.Abstract != "" {
		abs := cleaning.CleanString(cleaning.DeTag(gdoc.Abstract))
		if len(abs) > cleaning.MinAbstractLength {
			h := sha1.Sum([]byte(abs))
			out.Abstracts = append(out.Abstracts, fatcat2.Abstract{
				MIMEType: "application/xml+jats",
				Content:  abs,
				Language: cleaning.NormalizeLanguage(gdoc.LanguageCode),
				SHA1:     fmt.Sprintf("%x", h),
			})
		}
	}

	for _, citation := range gdoc.Citations {
		rawRef := fatcat2.RawRef{
			Index:   citation.Index,
			Locator: citation.FirstPage,
			Title:   citation.Title,
			Extra:   map[string]any{},
		}
		if len(citation.Date) >= 4 {
			year, _ := strconv.Atoi(citation.Date[0:4])
			rawRef.Year = year
		}

		rawRef.ContainerName = citation.Journal

		if citation.Unstructured != "" {
			rawRef.Extra["unstructured"] = citation.Unstructured
		}

		authorNames := []string{}
		for _, author := range citation.Authors {
			authorNames = append(authorNames, author.FullName)
		}
		if len(authorNames) > 0 {
			rawRef.Extra["authors"] = authorNames
		}

		if citation.Volume != "" {
			rawRef.Extra["volume"] = citation.Volume
		}

		if citation.Pages != "" {
			rawRef.Extra["pages"] = citation.Pages
		}

		if citation.LastPage != "" {
			rawRef.Extra["last_page"] = citation.LastPage
		}
	}

	out.Extra["grobid_version"] = gdoc.GrobidVersion
	if gdoc.Annex != "" {
		out.Extra["annex"] = gdoc.Annex
	}

	// grobid doesn't seem to try and extract license information :(

	// TODO can anything be done about container?

	return out, nil
}

type GrobidParseError struct {
	Sha1 string
	Err  error
}

func (e *GrobidParseError) Error() string {
	return fmt.Sprintf("grobid parsing failure for '%s': %v", e.Sha1, e.Err)
}

func (e *GrobidParseError) Unwrap() error { return e.Err }

// extractWARCPayload reads one WARC record (gzipped) out of raw bytes and
// returns the HTTP response body bytes. Expects exactly one record per
// input slice (matches the Range-GET shape we use against petabox).
func extractWARCPayload(raw []byte) ([]byte, error) {
	rdr, err := warc.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("warc reader init: %w", err)
	}
	defer rdr.Close()

	rec, err := rdr.ReadRecord()
	if err != nil {
		return nil, fmt.Errorf("warc ReadRecord: %w", err)
	}
	defer rec.Content.Close()

	// A capture the crawler cut short (time/size budget, dropped connection)
	// carries a WARC-Truncated header. Its stored body is shorter than the
	// declared HTTP Content-Length, so reading it below would fail with an
	// opaque "unexpected EOF". Detect it here, from the record header we
	// already have, and signal it distinctly so the caller can skip cleanly.
	if t := rec.Header.Get("WARC-Truncated"); t != "" {
		return nil, fmt.Errorf("%w (%s)", ErrTruncatedCapture, t)
	}

	httpResp, err := http.ReadResponse(bufio.NewReader(rec.Content), nil)
	if err != nil {
		return nil, fmt.Errorf("parse HTTP response inside WARC: %w", err)
	}
	defer httpResp.Body.Close()

	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("read HTTP body: %w", err)
	}
	return body, nil
}

// toWaybackURL builds the raw ("id_") Wayback replay URL for a captured
// resource, e.g.
//
//	https://web.archive.org/web/20250825174138id_/http://host/paper.pdf
//
// cdxTimestamp is the 14-digit CDX capture time (YYYYMMDDhhmmss); originalURL
// is the captured URL from the CDX row. The original URL is appended verbatim
// so its scheme "://" and any query string survive (url.JoinPath would
// path-clean "://" down to ":/"). Same shape the daily crawl stores via
// crawling.PDFCrawler.toWaybackURL.
func toWaybackURL(cdxTimestamp, originalURL string) string {
	endpoint := strings.TrimRight(viper.GetString("wayback.replay_endpoint"), "/")
	return fmt.Sprintf("%s/%sid_/%s", endpoint, cdxTimestamp, originalURL)
}

// decodeSha1Base32 converts the CDX-formatted 32-char base32 sha1 digest to
// fatcat's 40-char lowercase hex form.
func decodeSha1Base32(s string) (string, error) {
	bs, err := base32.StdEncoding.DecodeString(strings.ToUpper(s))
	if err != nil {
		return "", err
	}
	if len(bs) != 20 {
		return "", fmt.Errorf("expected 20 sha1 bytes, got %d", len(bs))
	}
	return hex.EncodeToString(bs), nil
}
