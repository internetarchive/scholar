package periodic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"git.archive.org/webgroup/scholar/trawler/cdx/cdxfile"
	"git.archive.org/webgroup/scholar/trawler/fatcat2"
	"git.archive.org/webgroup/scholar/trawler/ia"
	"git.archive.org/webgroup/scholar/trawler/indexing"
	"git.archive.org/webgroup/scholar/trawler/pdf"
	"github.com/google/uuid"
	warc "github.com/internetarchive/gowarc"
	"github.com/miku/grobidclient/tei"
	"github.com/spf13/viper"
	"go.temporal.io/sdk/log"
)

// One PDF CDX row's worth of work: decide whether the PDF behind it is worth
// fetching, fetch it from petabox, run it through GROBID, and attach whatever
// comes out to fatcat and the two ES indices.
//
// This is the same sequence the old per-line activity ran. What changed is that
// it is now a function called from a pool inside a batch rather than an
// activity, so the things that used to be built per line -- the HTTP client,
// the PDF processor, the denylists -- are built once per batch and handed in.

// ErrTruncatedCapture marks a WARC record the crawler flagged as truncated
// (WARC-Truncated header): the stored payload is incomplete, so callers skip
// the row rather than treating the short HTTP body as a hard error.
var ErrTruncatedCapture = errors.New("warc capture truncated at crawl time")

// lineDeps is everything one line's ingest needs that is worth building once
// per batch rather than once per line.
type lineDeps struct {
	HTTP      *http.Client
	Processor *pdf.Processor
	// Source is the provenance label stamped on records this run creates.
	Source string
	// ItemID is the item the batch's rows came from. It is the fallback for
	// rows whose WARC path carries no item of its own.
	ItemID string
	// SkipRules reads the denylists, freshly enough that an edit lands on a
	// running batch. See skipRulesReloader.
	SkipRules func() (pdfSkipRules, error)
	// BadPDFs remembers sha1s an earlier pass already found unusable, so a
	// replay does not pay for petabox and GROBID to rediscover it.
	BadPDFs badPDFIndex
	Beat    func(string)
	Log     log.Logger
}

// ingestLine runs one PDF CDX row to a verdict.
//
// It returns an error only for something that might work on a retry: a lookup
// that failed, a read that failed, an index that would not take the document.
// Everything else -- a row we deliberately pass on, a PDF GROBID made nothing
// of -- is a verdict, and comes back as counts plus a log row.
func ingestLine(ctx context.Context, d lineDeps, jsonLine string) (PeriodicCounts, Outcome, error) {
	out := PeriodicCounts{Lines: 1}
	oc := Outcome{ItemID: d.ItemID}

	skip := func(detail string) (PeriodicCounts, Outcome, error) {
		out.PdfsSkipped++
		oc.Status = StatusSkip
		oc.Detail = detail
		return out, oc, nil
	}

	var row cdxfile.Row
	if err := json.Unmarshal([]byte(jsonLine), &row); err != nil {
		// We staged this file ourselves, so a row that will not parse is
		// corruption rather than a transient failure. Retrying it would spend
		// the line's whole attempt budget reaching the same conclusion.
		d.Log.Warn("skipping unparsable cdx row", "line", jsonLine, "err", err.Error())
		return skip("unparsable cdx row")
	}

	oc.Sha1B32 = row.Sha1Base32
	oc.URL = row.URL

	sha1, err := decodeSha1Base32(row.Sha1Base32)
	if err != nil {
		d.Log.Warn("skipping row with bad sha1", "sha1_b32", row.Sha1Base32, "err", err.Error())
		return skip("bad sha1: " + err.Error())
	}

	skipRules, err := d.SkipRules()
	if err != nil {
		d.Log.Warn("falling back to skip rules loaded at startup", "err", err.Error())
	}

	if slices.Contains(skipRules.CursedShas, row.Sha1Base32) {
		d.Log.Warn("skipping a cursed sha", "sha1", row.Sha1Base32)
		return skip("cursed sha1")
	}

	for _, substr := range skipRules.URLIgnorePatterns {
		if strings.Contains(row.URL, substr) {
			d.Log.Warn("skipping an ignored url",
				"sha1", row.Sha1Base32, "url", row.URL, "pattern", substr)
			return skip("ignored url pattern: " + substr)
		}
	}

	// Ask the cheapest question first. A sha1 an earlier pass already found
	// unusable is not in fatcat or either index -- that is what made it
	// unusable -- so the lookups below would all miss and we would go on to
	// spend a petabox read and a GROBID run reaching last week's conclusion.
	if bad, err := d.BadPDFs.has(ctx, sha1); err != nil {
		// Advisory in both directions: if we cannot tell, process the PDF.
		d.Log.Warn("bad-pdf cache lookup failed, processing anyway",
			"sha1", sha1, "err", err.Error())
	} else if bad {
		d.Log.Info("skipping known-bad pdf", "sha1_b32", row.Sha1Base32)
		out.PdfsKnownBad++
		oc.Status = StatusSkip
		oc.Detail = "known-bad pdf, cached"
		return out, oc, nil
	}

	//
	// the conditions that mean we can avoid a petabox read:
	// 1. sha1 is in fatcat file index
	// 2. sha1 is in full text index
	// 3. file record exists already in DB and is connected to a release with an
	//    external ID
	//
	// we start here because petabox reads are expensive.
	//
	fileInES, err := indexing.ElasticDocExists(d.HTTP,
		viper.GetString("indexing.fatcat_file_ix"), "sha1", sha1)
	if err != nil {
		return out, oc, fmt.Errorf("fatcat_file existence check failed: %w", err)
	}

	fulltextInES, err := indexing.ElasticDocExists(d.HTTP,
		viper.GetString("indexing.fulltext_ix"), "fulltext.file_sha1", sha1)
	if err != nil {
		return out, oc, fmt.Errorf("scholar_fulltext existence check failed: %w", err)
	}

	extantFid, err := fatcat2.LookupSha1(d.HTTP, sha1)
	if err != nil {
		return out, oc, fmt.Errorf("could not look up sha1 in fc2: %w", err)
	}

	extantFileReleases := []fatcat2.Release{}
	var extantFileHasParent bool

	if extantFid != nil {
		oc.FileID = extantFid.String()
		extantFileReleases, err = fatcat2.FileReleases(d.HTTP, *extantFid)
		if err != nil {
			return out, oc, fmt.Errorf("could not get releases for '%s': %w", extantFid, err)
		}
		extidCount := 0
		for _, rel := range extantFileReleases {
			extidCount += len(rel.ExternalIDs)
		}
		extantFileHasParent = extidCount > 0
	}

	if fileInES && fulltextInES && extantFileHasParent {
		d.Log.Info("skipping known and indexed sha1", "sha1_b32", row.Sha1Base32, "fid", extantFid)
		return skip("already in fatcat and both indices")
	}

	//
	// get PDF bytes from petabox, run through grobid, parse Grobid XML.
	//
	d.Beat("range-read " + sha1)
	warcItem, warcFile := row.WARCItemAndFile()
	if warcItem == "" {
		warcItem = d.ItemID
	}
	raw, err := ia.ReadRange(ctx, d.HTTP, warcItem, warcFile, row.WarcOffset, row.WarcSize)
	if err != nil {
		return out, oc, fmt.Errorf("range read failed: %w", err)
	}

	d.Beat("pdf-extract " + sha1)
	pdfBs, err := extractWARCPayload(raw)
	if errors.Is(err, ErrTruncatedCapture) {
		// Expected data condition, not a failure: the crawler stored only
		// part of this PDF. Skip and move on. Any other extraction error
		// stays fatal so a real bug still surfaces.
		d.Log.Warn("skipping truncated capture",
			"url", row.URL, "sha1_b32", row.Sha1Base32, "reason", err.Error())
		return skip("truncated capture")
	}
	if err != nil {
		if strings.Contains(err.Error(), "malformed MIME header line") || strings.Contains(err.Error(), "unexpected EOF") {
			d.Log.Warn("corrupt warc",
				"url", row.URL, "sha1_b32", row.Sha1Base32, "error", err.Error())
			return skip("corrupt warc record")
		}
		return out, oc, fmt.Errorf("could not extract %q pdf bytes from warc: %w", row.Sha1Base32, err)
	}

	// Breadcrumb before crossing into cgo. Process runs pdfextract, which renders
	// page 0 through MuPDF, and a malformed PDF can segfault in there -- killing
	// the worker outright rather than returning an error. Nothing after this line
	// runs when that happens, so this is the only record that the PDF was ever
	// opened.
	//
	// The byte count is here because it is the one field a Go stack trace from
	// such a crash also carries, which is what lets you pick the culprit out of
	// however many PDFs the batch had in flight.
	d.Log.Info("processing pdf",
		"sha1", sha1, "sha1_b32", row.Sha1Base32, "url", row.URL, "bytes", len(pdfBs))

	d.Beat("pdf-process " + sha1)
	pdfContent, err := d.Processor.Process(ctx, pdfBs, sha1)
	if err != nil {
		return out, oc, fmt.Errorf("pdf processing failed: %w", err)
	}

	if len(pdfContent.GrobidXML) == 0 {
		// Nothing usable came out. Whether that is worth remembering is the pdf
		// package's call, not ours: a rejection is a fact about the bytes, while
		// a timeout or a size skip is a fact about GROBID's afternoon or our own
		// config, and caching either would blame the PDF for someone else's
		// problem.
		reason, cacheable := pdfContent.CacheableVerdict()
		if cacheable {
			d.markBad(ctx, sha1, reason)
		} else {
			d.Log.Warn("no tei, but not a verdict on the pdf; not caching",
				"sha1", sha1, "outcome", string(pdfContent.GrobidOutcome))
			reason = "no tei: " + string(pdfContent.GrobidOutcome)
		}
		out.PdfsFailed++
		oc.Status = StatusFail
		oc.Detail = reason
		return out, oc, nil
	}

	gdoc, err := tei.ParseDocument(bytes.NewReader(pdfContent.GrobidXML))
	if err != nil {
		// Non-200s from GROBID are already errors out of Process, so XML that
		// will not parse means GROBID told us something we do not understand.
		// It stays an error: the line is retried and then abandoned, and a run
		// of them trips the batch's circuit breaker, which is the signal that
		// this wants a human.
		d.Log.Warn("failed to parse grobid xml",
			"sha1", sha1, "error", err.Error(), "tei", pdfContent.GrobidXML)
		return out, oc, &GrobidParseError{Sha1: sha1, Err: err}
	}

	extIds := findExtIds(*gdoc)
	if len(extIds) == 0 {
		// GROBID read this PDF and found no DOI, PMID, PMCID or arXiv ID in it.
		// On a wide crawl that is the single most common way a row dies, and it
		// is a property of the bytes and of GROBID -- so it is exactly what the
		// cache is for, and exactly what bumping the generation un-decides when
		// GROBID gets better at finding IDs.
		d.Log.Warn("skipping parsed PDF with no external ID", "sha1_b32", row.Sha1Base32)
		d.markBad(ctx, sha1, "no external id in grobid output")
		return skip("no external id in grobid output")
	}

	var rid *uuid.UUID
	for _, pair := range extIds {
		rid, err = fatcat2.LookupRelease(d.HTTP, pair[0], pair[1])
		if err != nil {
			return out, oc, fmt.Errorf("fc2 release lookup failed: %w", err)
		}
		if rid != nil {
			break
		}
	}

	if rid == nil {
		// Deliberately not cached. Unlike the dead ends above, this one is not
		// about the PDF: the IDs were there, fatcat just has no release for them
		// yet. That changes on its own as fatcat grows, so a cached "no" would
		// strand this paper for good on the day before its release landed.
		d.Log.Warn("skipping parsed PDF with no matching release", "sha1_b32", row.Sha1Base32)
		// TODO we might add releases in the future; see grobidToRelease
		return skip("no release matches the grobid external ids")
	}

	release, err := fatcat2.GetRelease(d.HTTP, *rid)
	if err != nil {
		return out, oc, fmt.Errorf("could not get release '%s': %w", rid, err)
	}
	oc.ReleaseID = release.ID.String()

	// The fatcat file URL must point at the archived copy, not the live-web
	// URL from the CDX row. Build the wayback replay URL from the capture
	// timestamp + original URL.
	wbURL := toWaybackURL(row.Timestamp, row.URL)

	var fid uuid.UUID
	var file *fatcat2.File
	var did []string

	if extantFid != nil {
		fid = *extantFid
		f, err := fatcat2.GetFile(d.HTTP, fid)
		if err != nil {
			return out, oc, fmt.Errorf("could not get file '%s': %w", fid, err)
		}
		file = &f

		var foundUrl bool
		for _, u := range file.URLs {
			if u.URL == wbURL {
				foundUrl = true
			}
		}
		if !foundUrl {
			newUrl := fatcat2.FileURL{URL: wbURL, Rel: "webarchive", FileID: fid}
			added, err := fatcat2.AddFileURL(d.HTTP, fid, newUrl)
			if err != nil {
				return out, oc, fmt.Errorf("could not update fid '%s' with url '%s': %w", fid, wbURL, err)
			}
			if added {
				// we want to trigger a re-ingest if we added a url for this file
				file.URLs = append(file.URLs, newUrl)
				fileInES = false
				out.FilesUpdated++
				did = append(did, "added url")
			}
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
			d.Log.Warn("found probable dupe releases", "fid", fid,
				"ridBySha1", extantFileReleases[0].ID, "ridByExtId", release.ID)
		}
	} else {
		fid, err = uuid.NewV7()
		if err != nil {
			return out, oc, fmt.Errorf("uuid creation failed: %w", err)
		}
		file = &fatcat2.File{
			ID:       fid,
			Releases: []fatcat2.Release{release},
			Mimetype: "application/pdf",
			Source:   d.Source,
			URLs: []fatcat2.FileURL{
				{Rel: "webarchive", URL: wbURL, FileID: fid},
			},
		}
		if err = file.SetMetadata(pdfBs); err != nil {
			return out, oc, fmt.Errorf("could not compute pdf metadata: %w", err)
		}
		if _, err = fatcat2.CreateFile(d.HTTP, file); err != nil {
			return out, oc, fmt.Errorf("failed to create file '%s': %w", file.Sha1, err)
		}
		d.Log.Info("created file", "fid", file.ID, "rid", release.ID)
		out.PdfsAddedToDB++
		did = append(did, "created file")
	}
	oc.FileID = fid.String()

	//
	// check on sha1 in the two relevant ES indices. We're particular about
	// this for idempotency.
	//
	if !fileInES {
		fileDoc := indexing.PrepareFatcatFileDoc(*file)
		bs, err := json.Marshal(fileDoc)
		if err != nil {
			return out, oc, fmt.Errorf("failed to marshal file ES doc: %w", err)
		}
		if err := indexing.DoElasticIndex(d.HTTP,
			viper.GetString("indexing.fatcat_file_ix"), fileDoc.LegacyIdent, bs); err != nil {
			return out, oc, fmt.Errorf("failed to index file: %w", err)
		}
		out.PdfsAddedToES++
		did = append(did, "indexed file")
	}

	d.Beat("file-indexed " + sha1)

	if !fulltextInES {
		var container *fatcat2.Container
		if release.ContainerID != nil {
			c, err := fatcat2.GetContainer(d.HTTP, *release.ContainerID)
			if err != nil {
				return out, oc, fmt.Errorf("failed to look up container '%s': %w", release.ContainerID, err)
			}
			container = &c
		}

		esDoc := indexing.PrepareFulltextDoc(indexing.FulltextTransformCtx{
			HttpClient: d.HTTP,
			Release:    release,
			Container:  container,
			File:       file,
			GrobidXML:  pdfContent.GrobidXML,
			PdfText:    pdfContent.PdfText,
		})

		bs, err := json.Marshal(esDoc)
		if err != nil {
			return out, oc, fmt.Errorf("es doc serialization failed: %w", err)
		}
		if err := indexing.DoElasticIndex(d.HTTP,
			viper.GetString("indexing.fulltext_ix"), esDoc.Key, bs); err != nil {
			return out, oc, fmt.Errorf("failed to full text ingest '%s': %w", fid, err)
		}
		out.FulltextAddedToES++
		did = append(did, "indexed fulltext")
	}

	oc.Status = StatusOK
	if len(did) == 0 {
		// Everything this line would have written was already there. It is not a
		// skip -- we paid for the PDF to find out -- but nothing changed.
		oc.Detail = "already current"
	} else {
		oc.Detail = strings.Join(did, ", ")
	}
	return out, oc, nil
}

// markBad records a verdict in the negative cache. A cache write that fails is
// logged and forgotten: the row still has its verdict, its counts and its log
// line, and all that is lost is the shortcut on some future pass.
func (d lineDeps) markBad(ctx context.Context, sha1hex, reason string) {
	if err := d.BadPDFs.mark(ctx, sha1hex, reason, d.Source); err != nil {
		d.Log.Warn("could not record known-bad pdf",
			"sha1", sha1hex, "reason", reason, "err", err.Error())
	}
}

const (
	cursedShasKey        = "periodic.cursed_shas"
	urlIgnorePatternsKey = "periodic.url_ignore_patterns"
)

// pdfSkipRules are the denylists consulted before doing any expensive work on a
// PDF line: sha1s (base32) of PDFs that wedge the pipeline, and URL substrings
// for PDFs we do not want (eg publisher supplements linked from landing pages).
type pdfSkipRules struct {
	CursedShas        []string
	URLIgnorePatterns []string
}

// loadPdfSkipRules reads the denylists fresh from the config file so they can be
// edited on a running worker and picked up without a restart. It parses the file
// into a throwaway viper instead of re-reading the process global, which only
// loads the file at startup and would race with the other activities calling
// viper.Get. On a read error the values loaded at startup are returned along
// with the error.
func loadPdfSkipRules() (pdfSkipRules, error) {
	fallback := pdfSkipRules{
		CursedShas:        viper.GetStringSlice(cursedShasKey),
		URLIgnorePatterns: viper.GetStringSlice(urlIgnorePatternsKey),
	}

	path := viper.ConfigFileUsed()
	if path == "" {
		return fallback, errors.New("no config file was loaded at startup")
	}

	v := viper.New()
	v.SetConfigFile(path)
	if filepath.Ext(path) == "" {
		// the deployed config file is named "config" with no extension, so viper
		// can't sniff a format from it; main.go tells the global the same thing.
		v.SetConfigType("toml")
	}
	if err := v.ReadInConfig(); err != nil {
		return fallback, fmt.Errorf("failed to re-read config %s: %w", path, err)
	}

	return pdfSkipRules{
		CursedShas:        v.GetStringSlice(cursedShasKey),
		URLIgnorePatterns: v.GetStringSlice(urlIgnorePatternsKey),
	}, nil
}

// skipRulesReloader keeps the last denylists read from disk so a batch of
// hundreds of lines re-reads the config file on a TTL rather than once per line.
//
// Re-reading at all is the point -- a cursed sha1 added while a collection is
// being ingested should stop the next PDF, not wait for a deploy -- but "on the
// next line" used to mean "on the next activity", and an activity was one line.
// A batch activity runs for hours, so the property has to be kept by something
// other than the activity boundary. Same shape as scrape's blocklistReloader.
type skipRulesReloader struct {
	mu     sync.Mutex
	rules  pdfSkipRules
	at     time.Time
	loaded bool
}

// skipRules is shared by every batch in the worker; the file it reads is too.
var skipRules = &skipRulesReloader{}

// get returns the denylists, re-reading them when the cached copy is older than
// ttl.
//
// The error is only ever from a read that just happened, so a caller that logs
// it hears about a bad config file once per ttl rather than once per line. The
// rules are still returned alongside it: loadPdfSkipRules falls back to what was
// loaded at startup, and a stale denylist beats none.
func (s *skipRulesReloader) get(ttl time.Duration, now time.Time) (pdfSkipRules, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.loaded && now.Sub(s.at) < ttl {
		return s.rules, nil
	}

	rules, err := loadPdfSkipRules()
	s.rules = rules
	s.at = now
	s.loaded = true
	return rules, err
}

// findExtIds determines which external IDs we care about are defined on a
// grobid document. We return a list of pairs so we can prioritize which ones
// we later look up a release for.
func findExtIds(gdoc tei.GrobidDocument) [][]string {
	out := [][]string{}
	pairs := [][]string{
		{"doi", gdoc.Header.DOI},
		{"pmid", gdoc.Header.PMID},
		{"pmcid", gdoc.Header.PMCID},
		{"arxiv", gdoc.Header.ArxivID},
	}
	for _, pair := range pairs {
		if pair[1] != "" {
			out = append(out, pair)
		}
	}
	return out
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
