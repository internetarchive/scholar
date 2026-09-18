package daily

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"git.archive.org/webgroup/scholar/trawler/arxiv"
	"git.archive.org/webgroup/scholar/trawler/counts"
	"git.archive.org/webgroup/scholar/trawler/crawling"
	"git.archive.org/webgroup/scholar/trawler/crossref"
	"git.archive.org/webgroup/scholar/trawler/datacite"
	"git.archive.org/webgroup/scholar/trawler/doaj"
	"git.archive.org/webgroup/scholar/trawler/fatcat2"
	"git.archive.org/webgroup/scholar/trawler/indexing"
	"git.archive.org/webgroup/scholar/trawler/pdf"
	"git.archive.org/webgroup/scholar/trawler/pubmed"
	"github.com/google/uuid"
	"github.com/spf13/viper"
	"go.temporal.io/sdk/log"
)

// One ndjson line's worth of work: turn the upstream record into a fatcat
// release, decide whether its fulltext is worth crawling, crawl it through
// SPN, run what comes back through GROBID, and attach the result to fatcat and
// the two ES indices.
//
// This is the same sequence the old per-line activity ran. What changed is
// that it is now a function called from a pool inside a batch rather than an
// activity, so the things that used to be built per line -- the HTTP client,
// the SPN and CDX clients, the PDF processor, the denylists -- are built once
// per batch and handed in.

// upstreamFn is the per-record transform each upstream package provides. It
// turns one line of that upstream's ndjson into a fatcat release (creating it
// if fatcat does not have it yet), or into nil when the record is not worth
// one.
type upstreamFn func(context.Context, *http.Client, string, []byte) (counts.Counts, *fatcat2.Release, error)

// upstreamProcessor picks the transform for an upstream. It is resolved once
// per batch rather than per line, and the error is what a typo in --upstream
// looks like by the time it reaches a worker.
func upstreamProcessor(upstream string) (upstreamFn, error) {
	switch upstream {
	case "arxiv":
		return arxiv.ProcessLine, nil
	case "crossref":
		return crossref.ProcessLine, nil
	case "datacite":
		return datacite.ProcessLine, nil
	case "doaj":
		return doaj.ProcessLine, nil
	case "pubmed":
		return pubmed.ProcessLine, nil
	}
	return nil, fmt.Errorf("daily: unknown upstream %q", upstream)
}

// crawlFn crawls one URL. CrawlBatchActivity supplies the real crawler; tests
// supply their own.
type crawlFn func(ctx context.Context, url string) (crawling.CrawlResult, error)

// lineDeps is everything one line's work needs that is worth building once per
// batch rather than once per line.
type lineDeps struct {
	HTTP      *http.Client
	Processor *pdf.Processor
	// Crawl runs one of a release's fulltext URLs through the batch's crawler.
	Crawl crawlFn
	// Process is the upstream transform; see upstreamFn.
	Process upstreamFn
	// Source is the provenance label stamped on records this run creates.
	Source string
	// Upstream is which API the line came from.
	Upstream string
	// Rules reads the denylists, freshly enough that an edit lands on a running
	// batch. See crawlRulesReloader.
	Rules func() (crawlRules, error)
	Beat  func(string)
	Log   log.Logger
}

// processLine runs one ndjson line to a verdict.
//
// It returns an error only for something that might work on a retry: a lookup
// that failed, an index that would not take the document, GROBID refusing to
// answer. Everything else -- a record we deliberately pass on, a release whose
// fulltext we could not reach -- is a verdict, and comes back as counts plus a
// log row.
func processLine(ctx context.Context, d lineDeps, jsonLine string) (DailyCounts, Outcome, error) {
	out := DailyCounts{Lines: 1}
	oc := Outcome{Upstream: d.Upstream}

	skip := func(detail string) (DailyCounts, Outcome, error) {
		oc.Status = StatusSkip
		oc.Detail = detail
		return out, oc, nil
	}
	fail := func(detail string) (DailyCounts, Outcome, error) {
		oc.Status = StatusFail
		oc.Detail = detail
		return out, oc, nil
	}

	d.Beat("upstream-transform")
	c, release, err := d.Process(ctx, d.HTTP, d.Source, []byte(jsonLine))
	out.Counts = out.Counts.Add(c)
	if err != nil {
		return out, oc, fmt.Errorf("%s processing failed: %w", d.Upstream, err)
	}

	if release == nil {
		// The upstream transform passed on the record -- not paperlike, no
		// usable identifier, already known and unchanged. It has already said
		// so in the counts it returned.
		return skip("no release from upstream record")
	}

	oc.ReleaseID = release.ID.String()
	oc.ExtID = extIDLabel(release)

	rules, err := d.Rules()
	if err != nil {
		d.Log.Warn("falling back to crawl rules loaded at startup", "err", err.Error())
	}

	if ok, reason := shouldCrawlRelease(release, rules.DOIPrefixBlocklist); !ok {
		d.Log.Info(fmt.Sprintf("skipping crawl for %q: %s", release.ID, reason))
		return skip(reason)
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

	existingFiles, err := fatcat2.ReleaseFiles(d.HTTP, release.ID)
	if err != nil {
		return out, oc, fmt.Errorf("failed to check files for release %q: %w", release.ID, err)
	}
	if len(existingFiles) > 0 {
		d.Log.Info(fmt.Sprintf("skipping crawl for %q, already has %d files", release.ID, len(existingFiles)))
		return skip(fmt.Sprintf("release already has %d files", len(existingFiles)))
	}

	out.Releases.CrawlWanted++

	res, seed, reason := crawlFulltext(ctx, d, release, urls)
	oc.SeedURL = seed
	if !res.Success {
		// Not reaching a PDF is the ordinary outcome for most of the open web,
		// so it is a verdict rather than an error: retrying the line would
		// re-run the whole upstream transform to reach the same dead end, and
		// a publisher that blocks us will still block us in thirty seconds.
		return fail("no pdf: " + reason)
	}
	oc.WaybackURL = res.SnapshotUrl

	mimetype, _, _ := strings.Cut(res.Mimetype, ";")

	fid, err := uuid.NewV7()
	if err != nil {
		return out, oc, fmt.Errorf("uuid creation failed: %w", err)
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
	res.Content.Close()
	if err != nil {
		return out, oc, fmt.Errorf("could not read pdf bytes: %w", err)
	}

	if err = file.SetMetadata(pdfBs); err != nil {
		return out, oc, err
	}

	// TODO check if file exists. This can happen if we're re-running this
	// workflow. NB--in that case, we've pulled all of the stuff from a previous
	// crawl from CDX and don't need to worry about wasting SPN time assuming
	// stuff is fresh enough which reminds me if we ever want to care about cdx
	// freshness...

	fileID, err := fatcat2.LookupSha256(d.HTTP, file.Sha256)
	if err != nil {
		return out, oc, fmt.Errorf("sha256 lookup failed: %w", err)
	}

	d.Beat("file-lookup")

	// TODO we could verify that the existing file is attached to the release ID
	// we're working with...

	var did []string
	if fileID == nil {
		// CreateFile may reassign the file's ID to an existing legacy ident, so
		// the ID it hands back -- not the one we generated above -- is the one
		// worth logging.
		fileID, err = fatcat2.CreateFile(d.HTTP, &file)
		if err != nil {
			return out, oc, fmt.Errorf("file creation failed: %w", err)
		}
		out.Releases.Acquired++
		did = append(did, "created file")
	}
	if fileID != nil {
		oc.FileID = fileID.String()
	}

	d.Beat("file-created")

	fileInES, err := indexing.ElasticDocExists(d.HTTP,
		viper.GetString("indexing.fatcat_file_ix"), "sha1", file.Sha1)
	if err != nil {
		return out, oc, fmt.Errorf("fatcat_file existence check failed: %w", err)
	}
	if !fileInES {
		fileDoc := indexing.PrepareFatcatFileDoc(file)
		bs, err := json.Marshal(fileDoc)
		if err != nil {
			return out, oc, fmt.Errorf("failed to marshal file ES doc: %w", err)
		}
		err = indexing.DoElasticIndex(d.HTTP,
			viper.GetString("indexing.fatcat_file_ix"), fileDoc.LegacyIdent, bs)
		if err != nil {
			return out, oc, fmt.Errorf("failed to index file: %w", err)
		}
		did = append(did, "indexed file")
	}

	d.Beat("file-indexed")

	fulltextInES, err := indexing.ElasticDocExists(d.HTTP,
		viper.GetString("indexing.fulltext_ix"), "fulltext.file_sha1", file.Sha1)
	if err != nil {
		return out, oc, fmt.Errorf("scholar_fulltext existence check failed: %w", err)
	}
	if fulltextInES {
		oc.Status = StatusOK
		oc.Detail = detailOr(did, "already current")
		return out, oc, nil
	}

	d.Beat("pdf-process")
	pdfContent, err := d.Processor.Process(ctx, pdfBs, file.Sha1)
	if err != nil {
		return out, oc, fmt.Errorf("pdf processing failed: %w", err)
	}
	if len(pdfContent.GrobidXML) == 0 {
		d.Log.Warn("grobid could not parse pdf", "sha1", file.Sha1, "rid", release.ID)
		out.Pdfs.Failed++
		// CacheableVerdict only names the outcomes that are facts about the
		// bytes; the rest -- a GROBID timeout, a PDF over grobid.max_filesize
		// we chose not to send -- come back blank. Both mean no fulltext, but
		// the log must not read as though GROBID rejected the paper when it
		// never saw it.
		reason, ok := pdfContent.CacheableVerdict()
		if !ok {
			reason = "no tei: " + string(pdfContent.GrobidOutcome)
		}
		return fail(reason)
	}
	d.Beat("post-pdf-process")

	var container *fatcat2.Container
	if release.ContainerID != nil {
		c, err := fatcat2.GetContainer(d.HTTP, *release.ContainerID)
		if err != nil {
			return out, oc, fmt.Errorf("could not fetch container: %w", err)
		}
		container = &c
	}
	d.Beat("container-fetched")

	d.Log.Info("preparing full text es doc",
		"rid", release.ID, "xmlLen", len(pdfContent.GrobidXML), "pdfTextLen", len(pdfContent.PdfText))

	esDoc := indexing.PrepareFulltextDoc(indexing.FulltextTransformCtx{
		HttpClient: d.HTTP,
		Release:    *release,
		File:       &file,
		PdfText:    pdfContent.PdfText,
		GrobidXML:  pdfContent.GrobidXML,
		Container:  container,
	})

	ftbs, err := json.Marshal(esDoc)
	if err != nil {
		return out, oc, fmt.Errorf("marshaling fulltext doc failed: %w", err)
	}

	err = indexing.DoElasticIndex(d.HTTP,
		viper.GetString("indexing.fulltext_ix"), esDoc.Key, ftbs)
	if err != nil {
		return out, oc, fmt.Errorf("indexing fulltext failed: %w", err)
	}
	d.Beat("fulltext-indexed")

	out.Releases.Ingested++
	did = append(did, "indexed fulltext")

	oc.Status = StatusOK
	oc.Detail = detailOr(did, "already current")
	return out, oc, nil
}

// crawlFulltext walks a release's fulltext URLs until one of them lands a PDF
// in Wayback. It reports the result, the URL it ended on, and why it stopped.
//
// A crawl that errors is not the same as a crawl that came up empty, but
// neither is fatal here: a day of upstream metadata will always contain
// publishers who block us and links that 404, and the next URL for the same
// release may still work. Everything that goes wrong is folded into the reason
// string so the outcome log says which of the release's URLs was tried and
// what happened, which is the view the Temporal UI used to give for free.
func crawlFulltext(ctx context.Context, d lineDeps,
	release *fatcat2.Release, urls []string) (crawling.CrawlResult, string, string) {
	var (
		res    crawling.CrawlResult
		seed   string
		reason = "no fulltext urls"
	)

	for _, u := range urls {
		d.Beat("crawl " + u)
		seed = u

		var err error
		res, err = d.Crawl(ctx, u)
		if res.Success {
			return res, seed, ""
		}
		// Crawl only hands back a body on success, but leave nothing to chance:
		// at this volume leaking a connection per URL would exhaust the worker.
		if res.Content != nil {
			io.Copy(io.Discard, res.Content)
			res.Content.Close()
			res.Content = nil
		}
		if err != nil {
			d.Log.Info(fmt.Sprintf("crawl failed for %q, %q: %s", release.ID, u, err))
			reason = "crawl error: " + err.Error()
			continue
		}
		reason = res.FailReason
		if reason == "" {
			reason = "unknown"
		}
	}

	return crawling.CrawlResult{}, seed, reason
}

// shouldCrawlRelease returns false with a reason string when we should skip
// attempting to crawl a release's fulltext. It encapsulates the pure
// decision logic so it can be unit tested independently of processLine's I/O.
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

// extIDLabel is the handle the outcome log hangs a row on: the release's DOI
// where it has one, and otherwise whichever external ID it does have. A row
// with neither is still worth writing -- it has a release ID -- but there is
// nothing to join it to upstream on.
func extIDLabel(release *fatcat2.Release) string {
	if doi := release.DOI(); doi != "" {
		return "doi:" + doi
	}
	if len(release.ExternalIDs) > 0 {
		return release.ExternalIDs[0].Type + ":" + release.ExternalIDs[0].Value
	}
	return ""
}

// detailOr joins what a line actually did, falling back when it did nothing --
// everything the line would have written was already there. That is not a
// skip: we paid for the crawl and the lookups to find out.
func detailOr(did []string, fallback string) string {
	if len(did) == 0 {
		return fallback
	}
	return strings.Join(did, ", ")
}

// rulesReloadInterval is how stale the denylists may get inside a running
// batch.
func rulesReloadInterval() time.Duration {
	if d := viper.GetDuration("daily.blocklist_reload_interval"); d > 0 {
		return d
	}
	return defaultRulesReloadFreq
}
