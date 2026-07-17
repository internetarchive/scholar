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
	"encoding/base32"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"git.archive.org/webgroup/scholar/trawler/cdx/cdxfile"
	"git.archive.org/webgroup/scholar/trawler/cleaning"
	"git.archive.org/webgroup/scholar/trawler/fatcat2"
	"git.archive.org/webgroup/scholar/trawler/ia"
	"git.archive.org/webgroup/scholar/trawler/pdf"
	"github.com/google/uuid"
	warc "github.com/internetarchive/gowarc"
	"github.com/miku/grobidclient/tei"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/workflow"
)

const (
	// TODO config?
	itemsPerPage = 100
	taskQueue    = "periodic_ingest"
)

type PeriodicCounts struct {
	PdfLines     int
	PdfsWanted   int
	PdfsAcquired int
}

type PeriodicIngestInput struct {
	// petabox collection of warcs/cdx
	CollectionName string
	// limit how many items are looked at; for debugging
	Limit int
}

func PeriodicIngestWorkflow(ctx workflow.Context, in PeriodicIngestInput) (PeriodicCounts, error) {
	out := PeriodicCounts{}
	// l := workflow.GetLogger(ctx)

	ao := workflow.ActivityOptions{
		StartToCloseTimeout: 20 * time.Minute,
		TaskQueue:           taskQueue,
	}
	var listOut ListCollectionOutput
	err := workflow.ExecuteActivity(
		workflow.WithActivityOptions(ctx, ao),
		ListCollectionActivity,
		ListCollectionInput{
			CollectionName: in.CollectionName,
			Limit:          in.Limit}).Get(ctx, &listOut)
	if err != nil {
		return out, err
	}

	ao = workflow.ActivityOptions{
		StartToCloseTimeout: 4 * time.Hour, // TODO may want to tweak later
		TaskQueue:           taskQueue,
	}
	ctx = workflow.WithActivityOptions(ctx, ao)
	var processOut PeriodicCounts
	for _, itemId := range listOut.ItemIds {

		err := workflow.ExecuteActivity(
			ctx, ProcessCrawlItemActivity,
			ProcessCrawlItemInput{ItemId: itemId}).Get(ctx, &processOut)
		if err != nil {
			return out, err
		}
		out.PdfLines += processOut.PdfLines
		out.PdfsWanted += processOut.PdfsWanted
		out.PdfsAcquired += processOut.PdfsAcquired
	}

	return out, nil
}

type ListCollectionInput struct {
	CollectionName string
	Limit          int
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

	for true {
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

		if len(ids) >= in.Limit {
			break
		}

		if !hasMore {
			break
		}

		page += 1
	}

	return out, nil
}

type ProcessCrawlItemInput struct {
	ItemId string
}

func ProcessCrawlItemActivity(ctx context.Context, in ProcessCrawlItemInput) (PeriodicCounts, error) {
	out := PeriodicCounts{}
	client := &http.Client{Timeout: 10 * time.Minute}
	l := activity.GetLogger(ctx)

	activity.RecordHeartbeat(ctx, "metadata")
	files, err := ia.ItemFiles(ctx, client, in.ItemId)
	if err != nil {
		return out, fmt.Errorf("metadata for %s: %w", in.ItemId, err)
	}
	rollup, err := ia.FindRollupCDX(files)
	if err != nil {
		return out, fmt.Errorf("find rollup CDX for %s: %w", in.ItemId, err)
	}

	activity.RecordHeartbeat(ctx, "download-cdx")
	rdr, err := ia.OpenFile(ctx, client, in.ItemId, rollup)
	if err != nil {
		return out, fmt.Errorf("open rollup CDX %s/%s: %w", in.ItemId, rollup, err)
	}
	defer rdr.Close()

	activity.RecordHeartbeat(ctx, "parse-cdx")
	pdfLines, err := cdxfile.Parse(rdr, cdxfile.PDFFilter)
	if err != nil {
		return out, fmt.Errorf("parse rollup CDX %s/%s: %w", in.ItemId, rollup, err)
	}

	out.PdfLines = len(pdfLines)

	processor, err := pdf.NewProcessor(func(msg string) {
		activity.RecordHeartbeat(ctx, msg)
	})

	for _, pdfLine := range pdfLines {
		warcItem, warcFile := pdfLine.WARCItemAndFile()
		if warcItem == "" {
			warcItem = in.ItemId
		}
		sha1, err := decodeSha1Base32(pdfLine.Sha1Base32)
		if err != nil {
			l.Warn("skipping row with bad sha1", "sha1_b32", pdfLine.Sha1Base32, "err", err.Error())
			continue
		}

		activity.RecordHeartbeat(ctx, "sha1-lookup")
		extantFid, err := fatcat2.LookupSha1(client, sha1)
		if err != nil {
			return out, fmt.Errorf("could not look up sha1 in fc2: %w", err)
		}

		if extantFid != nil {
			activity.RecordHeartbeat(ctx, "file-lookup")
			file, err := fatcat2.GetFile(client, *extantFid)
			if err != nil {
				return out, fmt.Errorf("could not get file '%s' from fc2: '%w'", extantFid, err)
			}
			if len(file.URLs) > 0 {
				l.Info("skipping known file", "sha1", sha1, "fid", extantFid)
			}
		}

		out.PdfsWanted++

		activity.RecordHeartbeat(ctx, "range-read")
		raw, err := ia.ReadRange(ctx, client,
			warcItem, warcFile, pdfLine.WarcOffset, pdfLine.WarcSize)
		if err != nil {
			return out, fmt.Errorf("range read failed: %w", err)
		}

		activity.RecordHeartbeat(ctx, "pdf-extract")
		pdfBs, err := extractWARCPayload(raw)
		if err != nil {
			return out, fmt.Errorf("could not extract pdf bytes from warc: %w", err)
		}

		activity.RecordHeartbeat(ctx, "pdf-process")
		pdfContent, err := processor.Process(ctx, pdfBs, sha1)
		if err != nil {
			return out, fmt.Errorf("pdf processing failed: %w", err)
		}

		gdoc, err := tei.ParseDocument(bytes.NewReader(pdfContent.GrobidXML))
		if err != nil {
			return out, fmt.Errorf("grobid parsing failed: %w", err)
		}

		pairs := [][]string{
			[]string{"doi", gdoc.Header.DOI},
			[]string{"pmid", gdoc.Header.PMID},
			[]string{"pmcid", gdoc.Header.PMCID},
			[]string{"arxiv", gdoc.Header.ArxivID},
		}

		// TODO if all of the ext ids are empty we should probably just log and
		// move on. it might not be an academic PDF or we might make a release/file
		// combo that ends up orphaned from a later, DOI'ed release.

		var rid *uuid.UUID
		var idType string
		err = nil

		for _, pair := range pairs {
			idType = pair[0]
			rid, err = fatcat2.LookupRelease(client, idType, pair[1])
			if err != nil {
				return out, fmt.Errorf("fc2 release lookup failed: %w", err)
			}

			if rid != nil {
				break
			}
		}

		var release *fatcat2.Release

		if rid != nil {
			// have a release we can add a file to
			r, err := fatcat2.GetRelease(client, *rid)
			if err != nil {
				return out, fmt.Errorf("failed to get release '%s': %w", rid, err)
			}
			release = &r
		} else {
			// TODO run a basic heuristic on whether or not this seems like a paper:
			// does it have at least one author, an external ID, an abstract, a title, and a
			// citation?
			r, err := grobidToRelease(client, gdoc)
			if err != nil {
				return out, fmt.Errorf("failed to convert grobid to new release: %w", err)
			}
			release = &r
			rid, err := fatcat2.CreateRelease(client, *release)
			if err != nil {
				return out, fmt.Errorf("could not create release: %w", err)
			}
			release.ID = *rid
		}

		fid, err := uuid.NewV7()
		if err != nil {
			return out, fmt.Errorf("uuid creation failed: %w", err)
		}

		file := fatcat2.File{
			ID:       fid,
			Releases: []fatcat2.Release{*release},
			Mimetype: "application/pdf",
			Source:   release.Source,
			URLs: []fatcat2.FileURL{
				{
					Rel:    "wayback",
					URL:    pdfLine.URL,
					FileID: fid,
				},
			},
		}

		_, err = fatcat2.CreateFile(client, &file)

		// TODO create fc2 entities
		// TODO ES ingest
	}

	return out, nil
}

func grobidToRelease(client *http.Client, gdoc *tei.GrobidDocument) (fatcat2.Release, error) {
	out := fatcat2.Release{
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

	for _, author := range gdoc.Header.Authors {
		// TODO
		fmt.Println(author)
		//if author.ORCID != "" {
		//	aid, err := fatcat2.LookupOrcid(client, author.ORCID)
		//	if err != nil {
		//		return out, fmt.Errorf("failed to look up author '%s': %w", err)
		//	}
		//}
		//// TODO create authors as needed
		//// TODO append to contribs array on release
		//contrib := fatcat2.ReleaseContrib{
		//	RawName:   author.FullName,
		//	GivenName: author.GivenName,
		//	Surname:   author.Surname,
		//}
	}

	// TODO stage
	// TODO references
	// TODO contribs
	// TODO extra
	// TODO abstract
	// TODO grobid doesn't seem to try and extract license information

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

// processResult is a stub for downstream handling of blobproc results.
// TODO: implement real downstream handling (indexing into ES, attaching the
// file to fatcat, etc).
func processResult(ctx context.Context, sha1hex string, content pdf.Content) error {
	gdoc, err := tei.ParseDocument(bytes.NewReader(content.GrobidXML))
	if err != nil {
		return &GrobidParseError{Sha1: sha1hex, Err: err}
	}

	// TODO support non-DOI ext ids
	if gdoc.Header.DOI == "" {
		return nil
	}

	fmt.Printf("DBG %#v\n", gdoc)
	slog.Info("processed blobproc result",
		"sha1", sha1hex,
		"grobid_xml_len", len(content.GrobidXML),
		"pdf_text_len", len(content.PdfText))
	return nil
}

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
