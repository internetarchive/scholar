package crawling

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"git.archive.org/webgroup/scholar/trawler/cdx"
	"git.archive.org/webgroup/scholar/trawler/spn/spnclient"
	"github.com/PuerkitoBio/goquery"
	"github.com/spf13/viper"
	"golang.org/x/net/html/charset"
	"golang.org/x/text/encoding/htmlindex"
)

type SPNError struct {
	Message string
	JobID   string
	URL     string
}

func (e SPNError) Error() string {
	return fmt.Sprintf("SPN job %s failed for '%s': %s", e.JobID, e.URL, e.Message)
}

type BlockedError struct {
	RawURL    string
	ParsedURL string
	Pattern   string
}

func (e BlockedError) Error() string {
	return fmt.Sprintf("blocked '%s' due to pattern '%s'", e.ParsedURL, e.Pattern)
}

type PDFCrawler struct {
	SPNClient       spnclient.Client
	CDXClient       cdx.CDXClient
	WaybackEndpoint string
	UserAgent       string
	MaxHops         int
	SimpleGets      []string
	Blocklist       []string
}

type CrawlResult struct {
	// TODO
}

// TODO decide on CrawlResult
// TODO implement slog for crawl results

func (c PDFCrawler) fetchLiveReplay(URL string, ts time.Time) (io.Reader, error) {
	u, err := url.Parse(c.WaybackEndpoint)
	if err != nil {
		panic(err)
	}
	// TODO this should live elsewhere; it's been propagating (cdx, spnclient)
	timeFormat := "20060102150405"
	timestamp := ts.Format(timeFormat)
	u = u.JoinPath(timestamp, "id_/", URL)
	req, err := http.NewRequest("GET", u.String(), nil)
	if err != nil {
		panic(err)
	}
	req.Header.Set("User-Agent", c.UserAgent)

	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	attempts := 0
	retries := viper.GetInt("wayback.retries")
	backoff, err := time.ParseDuration(viper.GetString("wayback.backoff"))
	if err != nil {
		panic(err)
	}

	var resp *http.Response
	var wbErr error
	for {
		attempts++
		resp, wbErr = client.Do(req)
		if wbErr == nil || attempts == retries {
			break
		}
		time.Sleep(backoff * time.Duration(attempts))
	}
	if wbErr != nil {
		return nil, fmt.Errorf("wayback request failed after %d attempts: %w", attempts, err)
	}

	// TODO old code had this X-Archive-Src thing:
	/*
	   # defensively check that this is actually correct replay based on headers
	   if "X-Archive-Src" not in resp.headers:
	       # check if this was an error first
	       try:
	           resp.raise_for_status()
	       except Exception as e:
	           raise WaybackError(str(e))
	       # otherwise, a weird case (200/redirect but no Src header
	       raise WaybackError("replay fetch didn't return X-Archive-Src in headers")
	   if datetime not in resp.url:
	       raise WaybackError(
	           "didn't get exact reply (redirect?) datetime:{} got:{}".format(
	               datetime, resp.url
	           )
	       )
	*/

	if resp.StatusCode != http.StatusOK {
		// TODO return reader; only read body here if non-200 status code
		bs, err := io.ReadAll(resp.Body)
		var body string
		if err == nil {
			body = string(bs)
		}

		return nil, fmt.Errorf("got a non 200 from wayback: %d: '%s'", resp.StatusCode, body)
	}

	return resp.Body, nil
}

func (c PDFCrawler) Crawl(startURL string) (CrawlResult, error) {
	out := CrawlResult{}

	chain := []string{startURL}

	for len(chain) < c.MaxHops {
		u := chain[len(chain)-1]
		parsed, err := url.Parse(u)
		if err != nil {
			// TODO in the historical sandcrawler data there are a lot of URLs that
			// fail to parse -- none of them ever led to a hit. Thus, a fail here
			// should just give up on the PDF attempt and not bother with SPN.
			// TODO do not error, just form a fail result
			return out, fmt.Errorf("failed to parse url '%s': %w", startURL, err)
		}
		for _, p := range c.Blocklist {
			if strings.Contains(parsed.String(), p) {
				return out, BlockedError{
					RawURL:    startURL,
					ParsedURL: parsed.String(),
					Pattern:   p,
				}
			}
		}

		u = c.maybeRewrite(u)

		// TODO keep filling in simple get list
		var simpleGet bool
		for _, prefix := range c.SimpleGets {
			if strings.HasPrefix(u, prefix) {
				simpleGet = true
				break
			}
		}

		req := spnclient.SaveRequest{
			URL:                u,
			CaptureAll:         true,
			ForceGet:           simpleGet,
			SkipFirstArchive:   true,
			DelayForJavascript: !simpleGet,
			JavascriptTimeout:  30,
		}

		// poll until we obtain a slot
		var jobID string
		for jobID == "" {
			resp, err := c.SPNClient.Save(req)
			if err != nil {
				// TODO slog
				return out, fmt.Errorf("spn api failure: %w", err)
			}

			if strings.Contains(resp.Message, "reached the limit of active sessions") {
				// TODO should we bail after N attempts?
				time.Sleep(6 * time.Second)
				continue
			}

			if strings.Contains(resp.Message, "The same snapshot had been made") {
				// TODO attempt to look up via cdx? or error?
			}

			if resp.JobID == "" {
				// TODO slog
				return out, SPNError{
					Message: resp.Message,
					URL:     resp.URL,
				}
			}

			jobID = resp.JobID
		}

		// poll until job completes
		var spnJobResult spnclient.JobStatus
		for {
			spnJobResult, err = c.SPNClient.StatusJob(jobID)
			if err != nil {
				// TODO slog
				return out, fmt.Errorf("could not get spn job status: %w", err)
			}

			if spnJobResult.Status == "pending" {
				continue
			}

			break
		}

		if spnJobResult.Status != "success" {
			// TODO slog
			return out, SPNError{
				Message: spnJobResult.Message,
				URL:     spnJobResult.OriginalURL,
				JobID:   spnJobResult.JobID,
			}
		}

		// TODO  original code had a check for 'original_url' starting with /; did
		// not see that coming up in the last month+ of sc logs so i'm leaving it
		// out. There was an additional check seeing if :// was not in a url; this
		// used a status called spn2-success-partial-url but I didn't see any cases
		// of that in the sc db

		// TODO evaluate this elsevier hack:
		/*
		   if "://pdf.sciencedirectassets.com/" in spn_result.request_url:
		   elsevier_pdf_cdx = wayback_client.cdx_client.lookup_best(
		       spn_result.request_url,
		       best_mimetype="application/pdf",
		   )
		   if elsevier_pdf_cdx and elsevier_pdf_cdx.mimetype == "application/pdf":
		       print("  Trying pdf.sciencedirectassets.com hack!", file=sys.stderr)
		       cdx_row = elsevier_pdf_cdx
		   else:
		       print("  Failed pdf.sciencedirectassets.com hack!", file=sys.stderr)
		       # print(elsevier_pdf_cdx, file=sys.stderr)
		*/

		// TODO cdx lookup by OriginalURL and Timestamp (old client's fetch)
		// TODO investigate high number of spn2-cdx-lookup-failure. Grabbing a
		// recent one, it's something we tried to get today and indeed have found
		// in the wayback machine now. The current delay for a CDX lookup is 9
		// seconds so we should up that; going to start with 30 seconds.

		// TODO we have code around ftp:// resources but have only ever processed
		// 2500 of those and not since 2002 so dropping for now

		rows, err := c.CDXClient.Query(cdx.CDXParams{
			From: spnJobResult.Time,
			To:   spnJobResult.Time,
			URL:  spnJobResult.OriginalURL,
			Filters: []string{
				"statuscode:2..",
			},
			Limit: 1,
		})
		if err != nil {
			return out, fmt.Errorf("failed to find successful SPN job in CDX: %w", err)
		}

		if len(rows) == 0 {
			return out, fmt.Errorf("no error from CDX but 0 rows in result for %s",
				spnJobResult.OriginalURL)
		}

		cdxRow := rows[0]

		content, err := c.fetchLiveReplay(cdxRow.URL, cdxRow.Datetime)
		if err != nil {
			return out, fmt.Errorf("could not find cdx row '%s' %v in live wayback: %w", cdxRow.URL, cdxRow.Datetime, err)
		}

		if cdxRow.Mimetype == "application/pdf" {
			// TODO handle -- pipe content bytes to blobproc
			// TODO setup result
			return out, nil
		}

		// TODO i question the wisdom of proceeding from this point with XML; xhtml, sure.
		if !c.isHTMLishMimetype(cdxRow.Mimetype) {
			// TODO set up result for un-proceedable mimetype
			return out, nil
		}

		pdfLink, err := c.findPDFLink(cdxRow.URL, content)
		if err != nil {
			return out, fmt.Errorf("pdf link heuristics failure: %w", err)
		}

		if pdfLink == nil {
			// TODO setup failure result
			return out, nil
		}

		// TODO slog about pdfLink.Technique

		chain = append(chain, pdfLink.URL)
	}

	// TODO this should be unreachable but not a big deal
	return out, nil
}

func detectContentCharset(body io.Reader) string {
	r := bufio.NewReader(body)
	if data, err := r.Peek(1024); err == nil {
		if _, name, ok := charset.DetermineEncoding(data, ""); ok {
			return name
		}
	}
	return "utf-8"
}

func decodeHTMLBody(body io.Reader, charset string) (io.Reader, error) {
	if charset == "" {
		charset = detectContentCharset(body)
	}
	e, err := htmlindex.Get(charset)
	if err != nil {
		return nil, err
	}
	if name, _ := htmlindex.Name(e); name != "utf-8" {
		body = e.NewDecoder().Reader(body)
	}
	return body, nil
}

type FulltextPattern struct {
	Label string
	InURL string
}

type PDFLinkResult struct {
	URL       string
	Technique string
}

// currently known to work on revistas.unam.mx
var jsPDFRe = regexp.MustCompile(`pdfUrl = "(.*)";`)

func (c PDFCrawler) findPDFLink(URL string, content io.Reader) (*PDFLinkResult, error) {
	decodedContent, err := decodeHTMLBody(content, "")
	if err != nil {
		return nil, fmt.Errorf("could not decode content for %s: %w", URL, err)
	}

	bs, err := io.ReadAll(decodedContent)
	if err != nil {
		return nil, fmt.Errorf("could not read html content: %w", err)
	}

	rawHTML := string(bs)

	// https://www.revistas.unam.mx/index.php/rep/article/view/35503/32336
	// https://www.revistas.unam.mx/index.php/rep/article/download/35503/32336/85134
	if strings.Contains(URL, "/article/view/") {
		matches := jsPDFRe.FindAllStringSubmatch(rawHTML, 1)
		if len(matches) > 0 {
			u := strings.ReplaceAll(matches[0][1], `\`, "")
			return &PDFLinkResult{u, "jspdfurl"}, nil
		}
	}

	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(bs))
	if err != nil {
		return nil, fmt.Errorf("could not parse HTML for %s: %w", URL, err)
	}

	// https://elifesciences.org/articles/59841
	// <a href="https://elifesciences.org/download/aHR0cHM6Ly9jZG4uZWxpZmVzY2llbmNlcy5vcmcvYXJ0aWNsZXMvNTk4NDEvZWxpZmUtNTk4NDEtdjEucGRmP2Nhbm9uaWNhbFVyaT1odHRwczovL2VsaWZlc2NpZW5jZXMub3JnL2FydGljbGVzLzU5ODQx/elife-59841-v1.pdf?_hash=%2BEZ2CH%2FifGiXeDp5cSOT92ExFSGAjdYcDH%2FlRlOLLE0%3D" class="article-download-links-list__link" data-behaviour-initialised="true">Article PDF</a>
	if strings.Contains(URL, "://elifesciences.org/articles") {
		href, ok := doc.Find("a.article-download-links-list__link").Attr("href")
		if ok {
			return &PDFLinkResult{href, "elifesciences"}, nil
		}
	}

	// TODO

	meta, ok := doc.Find("meta[name='citation_pdf_url']").Attr("content")
	if ok {
		return &PDFLinkResult{meta, "citation_pdf_url"}, nil
	}

	meta, ok = doc.Find("meta[name='bepress_citation_pdf_url']").Attr("content")
	if ok {
		return &PDFLinkResult{meta, "bepress_citation_pdf_url"}, nil
	}

	// the original code first tried to use selectolax+css selectors then an older approach which is a mix of beautiful soup and regexes over raw HTML.
	// Ominously, the older code has comments like "[this function] is partially
	// deprecated" and "note: most of these have migrated to the html_biblio code
	// path". I ran through all of the old hacks and could not find exact matches
	// for any of them in the newer code. From the logs of the past couple days,
	// 6% of the pdf link detection attempts fell into the old code path. Of that
	// 6%, the techniques that were applied:
	//
	// 42% osf-by-url -- all of these fail
	// 52% elsevier-linkinghub -- most go to sciencedirect which fail. not all do
	// 2% ojs-galley-href -- some success
	// 1% ahajournals-url -- all of these succeed
	// 1% google-drive -- many succeed
	// <1% sciencedirect-munge-json -- led to pdf but got captcha'd

	// of the 94% that were html_biblio, 6% of those were "self pointing" which
	// is a situation I can't justify ever allowing and it's a mystery why Bryan
	// did. The point of this work is to find a next hop that might go to a PDF
	// -- a self link is not going to do that. The answer might lie on the "fuzzy
	// equals" function for urls; no, that is just stripping www. and :80 and
	// trailing / for an equals.

	// I'd like more data but this is all journalctl knows about. still, I have some takeaways:
	// - some of these are just url rewrties. I can move them into maybeRewrite.
	// - some of these should just go to blocklist (already did sciencedirect)
	// - some of these are purely regex based

	return nil, nil
}

func (c PDFCrawler) isHTMLishMimetype(mimetype string) bool {
	substrs := []string{"/html", "/xhtml", "application/xml", "text/xml"}
	for _, s := range substrs {
		if strings.Contains(mimetype, s) {
			return true
		}
	}
	return false
}

// maybeRewrite looks for known patterns we can rewrite into direct PDF access.
// This would ideally be captured in the config file perhaps as sets of regular
// expressions with capture groups but is for now in a function for expediency.
func (c PDFCrawler) maybeRewrite(u string) string {
	// TODO ensure that these are captured in simple get list
	if strings.HasPrefix(u, "https://arxiv.org/pdf/") && strings.HasSuffix(u, ".pdf") {
		return u[:len(u)-4]
	}
	if strings.HasPrefix(u, "https://onlinelibrary.wiley.com/doi/") {
		return strings.Replace(u, "doi", "doi/pdf", 1)
	}
	// TODO explore viewcontent.cgi rewrite opportunities
	// ie, if a doi.org link ends up as a viewcontent.cgi and there is a
	// consistent ID between the two we can cut out the doi.org hit and go
	// straight to viewcontent.

	// https://journals.sagepub.com/doi/10.1177/2309499019888836
	// https://journals.sagepub.com/doi/10.1177/2309499019888836?download=true
	if strings.HasPrefix(u, "https://journals.sagepub.com/doi/10.") {
		return u + "?download=true"
	}

	// https://journals.sagepub.com/doi/pdf/10.1177/2309499019888836
	// https://journals.sagepub.com/doi/pdf/10.1177/2309499019888836?download=true
	if strings.HasPrefix(u, "https://journals.sagepub.com/doi/pdf/10.") {
		return u + "?download=true"
	}

	// https://pubs.acs.org/doi/10.1021/acs.estlett.9b00379
	// https://pubs.acs.org/doi/pdf/10.1021/acs.estlett.9b00379?ref=article_openPDF
	if strings.HasPrefix(u, "https://pubs.acs.org/doi/10") {
		u = strings.Replace(u, "/doi/", "/doi/pdf/", 1)
		u = strings.TrimSuffix(u, "#")
		return u + "?ref=article_openPDF"
	}

	return u
}
