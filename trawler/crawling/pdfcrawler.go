package crawling

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	cdx "git.archive.org/webgroup/scholar/trawler/cdx/cdxclient"
	spn "git.archive.org/webgroup/scholar/trawler/spn/spnclient"
	"github.com/PuerkitoBio/goquery"
	"github.com/google/uuid"
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

// TODO create a newCrawler constructor...

type PDFCrawler struct {
	SPNClient       spn.Client
	CDXClient       cdx.Client
	WaybackEndpoint string
	UserAgent       string
	MaxHops         int
	SimpleGets      []string
	Blocklist       []string
	Logger          *slog.Logger
	crawlTrace      uuid.UUID
}

type CrawlResult struct {
	Chain       []string
	Success     bool
	FailReason  string
	Techniques  []string
	Content     io.Reader
	SnapshotUrl string
	Mimetype    string
	// TODO
}

func (c PDFCrawler) fetchWaybackRedirect(URL string, ts time.Time) (*http.Response, error) {
	client := &http.Client{}
	return c.fetchWayback(client, URL, ts)
}

func (c PDFCrawler) fetchWaybackNoRedirect(URL string, ts time.Time) (*http.Response, error) {
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return c.fetchWayback(client, URL, ts)
}

func (c PDFCrawler) toWaybackURL(URL string, ts time.Time) string {
	u, err := url.Parse(c.WaybackEndpoint)
	if err != nil {
		panic(err)
	}
	// TODO this should live elsewhere; it's been propagating (cdx, spnclient)
	timeFormat := "20060102150405"
	timestamp := ts.Format(timeFormat)
	return u.JoinPath(timestamp+"id_/", URL).String()
}

func (c PDFCrawler) fetchWayback(client *http.Client, URL string, ts time.Time) (*http.Response, error) {
	wbUrl := c.toWaybackURL(URL, ts)

	req, err := http.NewRequest("GET", wbUrl, nil)
	if err != nil {
		panic(err)
	}
	req.Header.Set("User-Agent", c.UserAgent)

	fmt.Printf("DBG %#v\n", wbUrl)

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

	/*
		if resp.StatusCode != http.StatusOK {
			// TODO return reader; only read body here if non-200 status code
			bs, err := io.ReadAll(resp.Body)
			var body string
			if err == nil {
				body = string(bs)
			}

			return nil, fmt.Errorf("got a non 200 from wayback: %d: '%s'", resp.StatusCode, body)
		}
	*/

	return resp, nil
}

func (c PDFCrawler) slogInfo(msg string, args ...any) {
	args = append(args, "trace")
	args = append(args, c.crawlTrace)
	c.Logger.Info(msg, args...)
}

func (c PDFCrawler) Crawl(startURL string) (CrawlResult, error) {
	out := &CrawlResult{}

	freshnessCutoff := viper.GetDuration("crawling.cdx_freshness_cutoff")

	trace, err := uuid.NewV7()
	if err != nil {
		return *out, fmt.Errorf("could not generate trace ID: %w", err)
	}

	c.crawlTrace = trace

	c.slogInfo("starting crawl", "starturl", startURL)

	out.Chain = []string{startURL}

	// TODO if we end at a url to which we were redirected i want that in the chain
	defer c.slogInfo("ending crawl", "result", out)

	for len(out.Chain) < c.MaxHops {
		u := out.Chain[len(out.Chain)-1]
		parsed, err := url.Parse(u)
		if err != nil {
			// in the historical sandcrawler data there are a lot of URLs that
			// fail to parse -- none of them ever led to a hit. Thus, a fail here
			// should just give up on the PDF attempt and not bother with SPN.
			c.slogInfo("unparsable URL", "url", u, "err", err.Error())
			out.FailReason = "bad-url"
			return *out, nil
		}

		for _, p := range c.Blocklist {
			if strings.Contains(parsed.String(), p) {
				c.slogInfo("blocked URL", "url", parsed.String(), "pattern", p)
				out.FailReason = "blocklist"
				return *out, nil
			}
		}

		var simpleGet bool

		ru := c.maybeRewrite(u)

		if ru != u {
			c.slogInfo("rewrote URL", "from", u, "to", ru)
			simpleGet = true
		} else {
			for _, pattern := range c.SimpleGets {
				split := strings.Split(pattern, "|")
				substr := split[0]
				suffix := ""
				if len(split) > 1 {
					suffix = split[1]
				}
				if strings.Contains(u, substr) && strings.HasSuffix(u, suffix) {
					c.slogInfo("simple GET list match", "url", ru, "pattern", pattern)
					simpleGet = true
					break
				}
			}
		}
		u = ru

		// TODO the old code fetched N rows then tried to find the most recent 200
		// response. I'm choosing to only look at the single most recent row for now.
		var row *cdx.CDXRow
		rows, err := c.CDXClient.Query(cdx.QueryParams{
			URL:   u,
			Limit: -1,
		})

		if len(rows) > 0 && time.Since(rows[0].Datetime) < freshnessCutoff {
			row = &rows[0]
		} else {
			if simpleGet {
				row, err = c.spnGetToCdx(u)
			} else {
				row, err = c.spnBrowserToCdx(u)
			}
			if err != nil {
				return *out, err
			}
			// TODO we could and probably should choose to go ahead with old
			// snapshots if spn fails; refactor around this i think
		}

		if row.URL != u {
			out.Chain = append(out.Chain, row.URL)
		}

		c.slogInfo("FOUND A CDX ROW", "row", row)

		if row.StatusCode > 399 {
			c.slogInfo("cdx error", "status", row.StatusCode)
			out.FailReason = fmt.Sprintf("status-%d", row.StatusCode)
			return *out, nil
		}

		var resp *http.Response

		if row.StatusCode == 302 {
			resp, err := c.fetchWaybackNoRedirect(row.URL, row.Datetime)
			if err != nil {
				return *out, fmt.Errorf("could not get replay from wb: %w", err)
			}
			fmt.Printf("DBG %#v\n", resp.Header)
			loc := resp.Header.Get("Location")
			if loc == "" {
				// TODO i don't think this is an error case, just a crawl ender? unless
				// we think it represents a transiet wbm issue...
				return *out, fmt.Errorf("empty location header in redirect for '%s'", u)
			}
			c.slogInfo("found redirect", "from", row.URL, "to", loc)
			if strings.HasPrefix(loc, "https://web.archive.org/web/") {
				split := strings.Split(loc, "/")
				loc = strings.Join(split[5:], "/")
				c.slogInfo("trimmed wayback prefix for redirect", "result", loc)
			} else if strings.HasPrefix(loc, "/web/") {
				split := strings.Split(loc, "/")
				loc = strings.Join(split[3:], "/")
				c.slogInfo("trimmed wayback prefix for redirect", "result", loc)
			}
			if loc == row.URL {
				c.slogInfo("detected infinite redirect", "url", loc)
				out.FailReason = "infinite-redirect"
				return *out, nil
			}
			out.Chain = append(out.Chain, loc)
			continue
		} else if row.StatusCode == 301 || (row.StatusCode >= 200 && row.StatusCode <= 299) {
			resp, err = c.fetchWaybackRedirect(row.URL, row.Datetime)
			if err != nil {
				return *out, fmt.Errorf("could not get replay from wb: %w", err)
			}
		} else if row.Mimetype == "warc/revisit" {
			// TODO i don't like this...
			resp, err = c.fetchWaybackNoRedirect(row.URL, row.Datetime)
			if err != nil {
				return *out, fmt.Errorf("could not get replay from wb: %w", err)
			}
			if resp.StatusCode == 301 || resp.StatusCode == 302 {
				// TODO unfortunate copypasta
				loc := resp.Header.Get("Location")
				if loc == "" {
					// TODO i don't think this is an error case, just a crawl ender? unless
					// we think it represents a transiet wbm issue...
					return *out, fmt.Errorf("empty location header in redirect for '%s'", u)
				}
				c.slogInfo("found redirect", "from", row.URL, "to", loc)
				if strings.HasPrefix(loc, "https://web.archive.org/web/") {
					split := strings.Split(loc, "/")
					loc = strings.Join(split[5:], "/")
					c.slogInfo("trimmed wayback prefix for redirect", "result", loc)
				} else if strings.HasPrefix(loc, "/web/") {
					split := strings.Split(loc, "/")
					loc = strings.Join(split[3:], "/")
					c.slogInfo("trimmed wayback prefix for redirect", "result", loc)
				}
				if loc == row.URL {
					c.slogInfo("detected infinite redirect", "url", loc)
					out.FailReason = "infinite-redirect"
					return *out, nil
				}
				out.Chain = append(out.Chain, loc)
				continue
			} else if resp.StatusCode > 302 {
				c.slogInfo("sad replay code", "status", resp.StatusCode)
				out.FailReason = fmt.Sprintf("status-%d", resp.StatusCode)
				return *out, nil
			} // else handle resp below
		} else {
			return *out, fmt.Errorf("surprising status code %d", row.StatusCode)
		}

		content := resp.Body
		mimetype := resp.Header.Get("Content-Type")

		fmt.Printf("DBG %#v\n", mimetype)

		if strings.Contains(mimetype, "application/pdf") {
			out.Success = true
			out.Content = content
			out.SnapshotUrl = c.toWaybackURL(row.URL, row.Datetime)
			out.Mimetype = mimetype
			return *out, nil
		}

		// TODO i question the wisdom of proceeding from this point with XML; xhtml, sure.
		if !c.isHTMLishMimetype(mimetype) {
			// TODO set up result for un-proceedable mimetype
			out.FailReason = "unknown-mimetype"
			c.slogInfo("un-processable mimetype", "mimetype", mimetype)
			return *out, nil
		}

		nextLink, err := c.findNextLink(row.URL, content)
		if err != nil {
			return *out, fmt.Errorf("pdf link heuristics failure: %w", err)
		}

		if nextLink == nil {
			out.FailReason = "dead-end"
			return *out, nil
		}

		out.Techniques = append(out.Techniques, nextLink.Technique)
		out.Chain = append(out.Chain, nextLink.URL)
	}

	// should be unreachable
	return *out, nil
}

func (c PDFCrawler) spnToCdx(u string, simpleGet bool) (*cdx.CDXRow, error) {
	var out *cdx.CDXRow

	req := spn.SaveRequest{
		URL:                u,
		CaptureAll:         true,
		ForceGet:           simpleGet,
		SkipFirstArchive:   true,
		DelayForJavascript: !simpleGet,
		JavascriptTimeout:  30,
		// NB this will usually not come up since we're (ideally) crawling new
		// stuff every day. however, when debugging these workflows, we send a lot
		// of repeated things to SPN. thus, a not archived within setting which
		// will return the most recent capture as a successful job completion.
		IfNotArchivedWithinSecs: 1209600, // two weeks
	}

	// poll until we obtain a slot
	var jobID string
	attempts := 0
	spnSlotPollInterval := viper.GetDuration("crawling.spn_slot_poll_interval")

	for jobID == "" {
		resp, err := c.SPNClient.Save(req)
		if err != nil {
			// TODO slog? what to do here? if transient we want temporal to poll;
			// if related to input, crawl should end; otherwise..? I *this* an
			// error like this (instead of a message in the payload) implies
			// transient failure
			return out, fmt.Errorf("spn api failure: %w", err)
		}

		if strings.Contains(resp.Message, "reached the limit of active sessions") {
			c.slogInfo("SPN slots full, polling", "attempt", attempts, "poll", spnSlotPollInterval)
			// TODO should we bail after N attempts?
			time.Sleep(spnSlotPollInterval)
			continue
		}

		/*
			TODO used to do this check when this code was in the outer loop. given that
			we check cdx right before invoking this, it feels like a far too edgy edge
			case to worry about
			if strings.Contains(resp.Message, "The same snapshot had been made") {
				// just continue, here, without updating the chain -- it'll execute the
				// CDX lookup again.  for this to even happen it means that a request
				// for this url *finished* between our initial CDX check and then the
				// subsequent SPN req. I could see that race condition happening if
				// this job is flapping for some reason..?
				// I suspect this is a rare case and it makes the code kind of ass
				c.slogInfo("SPN claimed existing snapshot, going back to CDX lookup...", "url", u)
				continue
			}
		*/

		if resp.JobID == "" {
			c.slogInfo("spn response lacked job id", "resp", resp)
			return out, SPNError{
				Message: resp.Message,
				URL:     resp.URL,
			}
		}

		jobID = resp.JobID
	}

	var err error

	// poll until job completes
	var spnJobResult spn.JobStatus
	for {
		spnJobResult, err = c.SPNClient.StatusJob(jobID)
		if err != nil {
			c.slogInfo("spn job status failure", "err", err.Error())
			return out, fmt.Errorf("could not get spn job status: %w", err)
		}

		if spnJobResult.Status == "pending" {
			c.slogInfo("sleeping while spn pending")
			time.Sleep(viper.GetDuration("crawling.spn_job_poll_interval"))
			continue
		}

		break
	}

	if spnJobResult.Status != "success" {
		c.slogInfo("spn failure", "result", spnJobResult)
		return out, SPNError{
			Message: spnJobResult.Message,
			URL:     spnJobResult.OriginalURL,
			JobID:   spnJobResult.JobID,
		}
	}
	// TODO this shouldn't be necessary as we have sleeping in the cdx client;
	// however, i hit a case where CDX's output couldn't be json parsed. I didn't
	// have debug on so i don't know what exactly it returned. I'm going to
	// gomment this out but put debug on in the hopes that I get that case again
	// c.slogInfo("sleeping before CDX lookup")
	//time.Sleep(5 * time.Second)
	rows, err := c.CDXClient.Query(cdx.QueryParams{
		From:  spnJobResult.Time,
		To:    spnJobResult.Time,
		URL:   spnJobResult.OriginalURL,
		Limit: 1,
	})
	// TODO hitting 504 here
	if err != nil {
		c.slogInfo("spn succeeded but did not find in CDX", "err", err.Error())
		return out, fmt.Errorf("failed to find successful SPN job in CDX: %w", err)
	}
	if len(rows) == 0 {
		return out, fmt.Errorf("no error from CDX but 0 rows in result for %s",
			spnJobResult.OriginalURL)
	}

	return &rows[0], nil

	// TODO  original code had a check for 'original_url' starting with /; did
	// not see that coming up in the last month+ of sc logs so i'm leaving it
	// out. There was an additional check seeing if :// was not in a url; this
	// used a status called spn2-success-partial-url but I didn't see any cases
	// of that in the sc db

	// TODO investigate high number of spn2-cdx-lookup-failure. Grabbing a
	// recent one, it's something we tried to get today and indeed have found
	// in the wayback machine now. The current delay for a CDX lookup is 9
	// seconds so we should up that; going to start with 30 seconds.

	// TODO we have code around ftp:// resources but have only ever processed
	// 2500 of those and not since 2020 so dropping for now

}

func (c PDFCrawler) spnGetToCdx(u string) (*cdx.CDXRow, error) {
	return c.spnToCdx(u, true)
}

func (c PDFCrawler) spnBrowserToCdx(u string) (*cdx.CDXRow, error) {
	return c.spnToCdx(u, false)
}

func decodeHTMLBody(body io.Reader, cset string) (io.Reader, error) {
	r := bufio.NewReader(body)
	if cset == "" {
		if data, err := r.Peek(1024); err == nil {
			if _, name, ok := charset.DetermineEncoding(data, ""); ok {
				cset = name
			}
		}
		cset = "utf-8"
	}
	e, err := htmlindex.Get(cset)
	if err != nil {
		return nil, err
	}
	if name, _ := htmlindex.Name(e); name != "utf-8" {
		return e.NewDecoder().Reader(r), nil
	}
	return r, nil
}

type FulltextPattern struct {
	Label string
	InURL string
}

type FindLinkResult struct {
	// URL is either a hoped-for PDF link or a hop towards one
	URL string
	// Technique is a unique string describing how we found the link. Its text
	// isn't important; it's just used to track success metrics.
	Technique string
	// Hop indicates whether we think this link gets us one page closer to a
	// diret PDF link. If false, it means we think the link points directly to a
	// PDF (ie, making it suitable for a simple wget)
	Hop bool
}

// absolutize takes two urls with the expectation that the first one has a
// domain and the second one *might* have a domain. the domain from the first
// argument is prepended to the path/query of the second argument if the second
// argument is lacking a domain.
func absolutize(pageURL, pdfURL string) (string, error) {
	if !strings.HasPrefix(pdfURL, "http://") && !strings.HasPrefix(pdfURL, "https://") && !strings.HasPrefix(pdfURL, "..") {
		pdfURL = "https://" + pdfURL
	}

	parsedPage, err := url.Parse(pageURL)
	if err != nil {
		return "", fmt.Errorf("could not parse page url '%s': %w", pageURL, err)
	}

	parsedPDF, err := url.Parse(pdfURL)
	if err != nil {
		return "", fmt.Errorf("could not parse pdf url '%s': %w", pdfURL, err)
	}

	if strings.HasPrefix(pdfURL, "..") {
		parsedPage = parsedPage.ResolveReference(parsedPDF)
		return parsedPage.String(), nil
	}

	if parsedPDF.Host != "" {
		return parsedPDF.String(), nil
	}

	parsedPDF.Host = parsedPage.Host
	parsedPDF.Scheme = parsedPage.Scheme

	return parsedPDF.String(), nil
}

func newHopResult(pageURL, hopURL, technique string) *FindLinkResult {
	r := newPDFLinkResult(pageURL, hopURL, technique)
	r.Hop = true
	return r
}

func newPDFLinkResult(pageURL, pdfURL, technique string) *FindLinkResult {
	u, err := absolutize(pageURL, pdfURL)
	if err == nil {
		pdfURL = u
	}

	return &FindLinkResult{
		URL:       pdfURL,
		Technique: technique,
	}
}

// currently known to work on revistas.unam.mx
var jsPDFRe = regexp.MustCompile(`pdfUrl = "(.*)";`)
var ieeeRe = regexp.MustCompile(`"pdfPath":"(/.*?\.pdf)"`)

func (c PDFCrawler) findNextLink(URL string, content io.Reader) (*FindLinkResult, error) {
	decodedContent, err := decodeHTMLBody(content, "")
	if err != nil {
		return nil, fmt.Errorf("could not decode content for %s: %w", URL, err)
	}

	bs, err := io.ReadAll(decodedContent)
	if err != nil {
		return nil, fmt.Errorf("could not read html content: %w", err)
	}

	// TODO .Hop not needed if the simple get list is up to date -- refactor.

	rawHTML := string(bs)

	// https://www.revistas.unam.mx/index.php/rep/article/view/35503/32336
	// https://www.revistas.unam.mx/index.php/rep/article/download/35503/32336/85134
	if strings.Contains(URL, "/article/view/") {
		matches := jsPDFRe.FindAllStringSubmatch(rawHTML, 1)
		if len(matches) > 0 {
			u := strings.ReplaceAll(matches[0][1], `\`, "")
			return newPDFLinkResult(URL, u, "jspdfurl"), nil
		}
	}

	// https://ieeexplore.ieee.org/document/8730316
	// https://ieeexplore.ieee.org/iel7/6287639/8600701/08730316.pdf
	if strings.Contains(URL, "ieeexplore.ieee.org/document/") {
		matches := ieeeRe.FindAllStringSubmatch(rawHTML, 1)
		if len(matches) > 0 {
			return newHopResult(URL, matches[0][1], "ieeejs"), nil
		}
	}

	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(bs))
	if err != nil {
		return nil, fmt.Errorf("could not parse HTML for %s: %w", URL, err)
	}

	// https://elifesciences.org/articles/59841
	// <a href="https://elifesciences.org/download/aHR0cHM6Ly9jZG4uZWxpZmVzY2llbmNlcy5vcmcvYXJ0aWNsZXMvNTk4NDEvZWxpZmUtNTk4NDEtdjEucGRmP2Nhbm9uaWNhbFVyaT1odHRwczovL2VsaWZlc2NpZW5jZXMub3JnL2FydGljbGVzLzU5ODQx/elife-59841-v1.pdf?_hash=%2BEZ2CH%2FifGiXeDp5cSOT92ExFSGAjdYcDH%2FlRlOLLE0%3D" class="article-download-links-list__link" data-behaviour-initialised="true">Article PDF</a>
	if strings.Contains(URL, "elifesciences.org/articles") {
		href, ok := doc.Find("a.article-download-links-list__link").Attr("href")
		if ok {
			return newPDFLinkResult(URL, href, "elifesciences"), nil
		}
	}

	/*
		If we can get the landing page to load, this *does* work fine. but as of
		11/2025 their site fails to load landing pages on the first try and I gave up
		trying to coax it into working. I suspect some accumulation of cookies is
		needed for it to work.

		For now, I've put this domain on the blocklist.

		// https://journals.tsu.ru/informatics/en/&journal_page=archive&id=2582&article_id=52764
		// <a class='file pdf' href='https://journals.tsu.ru/engine/download.php?id=302574&area=files'>Download file</a>
		if strings.Contains(URL, "journals.tsu.ru") && strings.Contains(URL, "article_id=") {
		  href, ok := doc.Find("a.file.pdf").Attr("href")
		  if ok {
		    return &PDFLinkResult{href, "journals.tsu.ru-download.php"}, nil
		  }
		}
	*/

	// https://www.eurosurveillance.org/content/10.2807/1560-7917.ES.2025.30.43.2500793
	// <a href="/deliver/fulltext/eurosurveillance/30/43/eurosurv-30-43-3.pdf?itemId=%2Fcontent%2F10.2807%2F1560-7917.ES.2025.30.43.2500793&mimeType=pdf&containerItemId=content/eurosurveillance" class="pdf " title="Download" rel="http://instance.metastore.ingenta.com/content/10.2807/1560-7917.ES.2025.30.43.2500793" target="/content/10.2807/1560-7917.ES.2025.30.43.2500793-pdf" >
	if strings.Contains(URL, "/content/10.") {
		attr, ok := doc.Find("a.pdf[title='Download']").Attr("href")
		if ok {
			return newPDFLinkResult(URL, attr, "a.pdf_link"), nil
		}
	}

	if strings.Contains(URL, "research.tue.nl") {
		attr, ok := doc.Find("meta[name='citation_pdf_url']").Attr("content")
		if ok {
			// they wrap pdf links in cloudflare but this rewrite seems to fix things...
			// https://research.tue.nl/files/1950518/Metis209517.pdf
			// https://pure.tue.nl/ws/portalfiles/portal/1950518/Metis209517.pdf
			u := strings.Replace(attr, "research.tue.nl", "pure.tue.nl", 1)
			u = strings.Replace(u, "/files/", "/ws/portalfiles/portal/", 1)
			return newPDFLinkResult(URL, u, "research.tue.nl"), nil
		}
	}

	if strings.Contains(URL, "hal.science") {
		attr, ok := doc.Find("div.widget-files a").Attr("href")
		if ok && strings.Contains(attr, "pdf") {
			return newPDFLinkResult(URL, attr, "hal"), nil
		}
	}

	if strings.Contains(URL, "/record/") {
		attr, ok := doc.Find("#detailedrecordminipanelfile a").Attr("href")
		if ok {
			return newPDFLinkResult(URL, attr, "invenio-record"), nil
		}
	}

	if strings.Contains(URL, "repositorio.unicamp.br") {
		attr, ok := doc.Find("span.titulo a").Attr("href")
		if ok {
			return newPDFLinkResult(URL, attr, "unicamp"), nil
		}
	}

	if strings.Contains(URL, "ingentaconnect.com/content/") {
		attr, ok := doc.Find("a.pdf[data-popup]").Attr("data-popup")
		if ok {
			return newPDFLinkResult(URL, attr, "ingenta"), nil
		}
	}

	if strings.Contains(URL, "/dlibra/") {
		attr, ok := doc.Find("iframe#js-main-frame").Attr("src")
		if ok {
			return newPDFLinkResult(URL, attr, "dlibra-iframe"), nil
		}
	}

	if strings.Contains(URL, "/available/") {
		attr, ok := doc.Find("table.file-table a").Attr("href")
		if ok {
			return newPDFLinkResult(URL, attr, "unipi.it"), nil
		}
	}

	if strings.Contains(URL, "/islandora/") {
		attr, ok := doc.Find("a.islandora-pdf-link").Attr("href")
		if ok {
			return newPDFLinkResult(URL, attr, "islandora"), nil
		}
	}

	if strings.Contains(URL, "/receive/") {
		// wacky workaround for noscript
		// see https://github.com/PuerkitoBio/goquery/issues/139
		root := doc.Selection
		var subdoc *goquery.Document
		var err error
		root.Find(`noscript`).Each(func(i int, selection *goquery.Selection) {
			if i != 1 {
				return
			}
			subdoc, err = goquery.NewDocumentFromReader(
				strings.NewReader(selection.Text()))
		})
		if err == nil {
			attr, ok := subdoc.Find("a").Attr("href")
			if ok {
				return newPDFLinkResult(URL, attr, "mycore-receive"), nil
			}
		}
	}

	if strings.Contains(URL, "/registro.do") {
		attr, ok := doc.Find(".resumen_bib a[data-analytics=open-mediagroup]").Attr("href")
		if ok {
			return newPDFLinkResult(URL, attr, "digibis-media-link"), nil
		}
	}

	if strings.Contains(URL, "repository.dri.ie/") {
		attr, ok := doc.Find("a#download_surrogate").Attr("href")
		if ok {
			return newPDFLinkResult(URL, attr, "dri.ie-download-link"), nil
		}
	}

	if strings.Contains(URL, "/view/") {
		attr, ok := doc.Find("a.download").Attr("href")
		if ok {
			return newPDFLinkResult(URL, attr, "ojs-pdf-download"), nil
		}
	}

	if strings.Contains(URL, "/view/") {
		attr, ok := doc.Find("a.pdf").Attr("href")
		if ok && strings.Contains(attr, "/article/") {
			return newPDFLinkResult(URL, attr, "ojs-pdf-embed"), nil
		}
	}

	if strings.Contains(URL, "scitemed.com/article/") {
		attr, ok := doc.Find("li.tab_pdf_btn a").Attr("href")
		if ok {
			return newPDFLinkResult(URL, attr, "scitemed"), nil
		}
	}

	if strings.Contains(URL, "doaj.org/article/") {
		attr, ok := doc.Find("section.col-md-8 a[target='_blank'].button--primary").Attr("href")
		if ok {
			return newHopResult(URL, attr, "doaj-access-link"), nil
		}
	}

	if strings.Contains(URL, "/article/view") {
		// eg https://www.mediterranea-comunicacion.org
		attr, ok := doc.Find("a.obj_galley_link.file").Attr("href")
		if ok {
			return newHopResult(URL, attr, "ojs-remote-pdf"), nil
		}
	}

	if strings.Contains(URL, "dlib.si/details/") {
		attr, ok := doc.Find("body #FilesBox a").Attr("href")
		if ok {
			return newPDFLinkResult(URL, attr, "dlib.si"), nil
		}
	}

	if strings.Contains(URL, "filclass.ru") {
		attr, ok := doc.Find("main .pdf-article a.pdficon").Attr("href")
		if ok {
			return newPDFLinkResult(URL, attr, "filclass.ru"), nil
		}
	}

	if strings.Contains(URL, "linkinghub.elsevier.com/retrieve/pii") {
		attr, ok := doc.Find("#redirectURL").Attr("value")
		if ok {
			u, err := url.PathUnescape(attr)
			if err == nil {
				parsed, err := url.Parse(u)
				if err == nil {
					// preserving behavior from sandcrawler
					if strings.Contains(u, "?via") {
						parsed.RawQuery = ""
					}
					return newHopResult(URL, parsed.String(), "linkinghub"), nil
				}
			}
		}
	}

	// TODO i need to do a freshness check on cdx lookups. for this site, for eg,
	// i'm pulling a snapshot from 2024 which is not possible to scrape properly.
	// i think it's okay to do something like "within 6 months" or "within four weeks"
	// TODO test for this
	if strings.Contains(URL, "sciengine.com/") {
		issn, _ := doc.Find("meta[name='citation_issn']").Attr("content")
		articleId, _ := doc.Find("meta[name='citation_id']").Attr("content")
		if issn != "" && articleId != "" {
			newUrl := fmt.Sprintf("https://www.sciengine.com/cfs/files/pdfs/view/%s/%s.pdf", issn, articleId)
			return newPDFLinkResult(URL, newUrl, "sciengine"), nil
		}
	}

	if strings.Contains(URL, "ieeexplore.ieee.org/stamp/stamp.jsp?arnumber") {
		attr, ok := doc.Find("iframe").Attr("src")
		if ok {
			return newPDFLinkResult(URL, attr, "ieee-iframe"), nil
		}
	}

	// eg, https://unsworks.unsw.edu.au/entities/publication/fd08fc25-48dc-40bc-b673-deb232f31faa
	attr, ok := doc.Find("meta[name='citation_pdf_url']").Attr("content")
	if ok {
		return newPDFLinkResult(URL, attr, "citation_pdf_url"), nil
	}

	// eg, https://aisel.aisnet.org/sjis/vol25/iss2/1/
	attr, ok = doc.Find("meta[name='bepress_citation_pdf_url']").Attr("content")
	if ok {
		return newPDFLinkResult(URL, attr, "bepress_citation_pdf_url"), nil
	}

	attr, ok = doc.Find("meta[name='eprints.document_url']").Attr("content")
	if ok {
		return newPDFLinkResult(URL, attr, "eprints-document_url"), nil
	}

	attr, ok = doc.Find("embed[type='application/pdf']").Attr("src")
	if ok {
		return newPDFLinkResult(URL, attr, "pdf-embed-type"), nil
	}

	attr, ok = doc.Find("embed[alt='pdf']").Attr("src")
	if ok {
		return newPDFLinkResult(URL, attr, "pdf-embed-alt"), nil
	}

	// NB the sample page bryan had for this now features citation_pdf_url so
	// this is unlikely to be triggered. I've left it here because it doesn't
	// hurt and tweaked the degruyter.html sample html to trigger the fallback.
	attr, ok = doc.Find("a.downloadPdf").Attr("href")
	if ok {
		return newPDFLinkResult(URL, attr, "downloadPdf"), nil
	}

	attr, ok = doc.Find(".download-article a").Attr("href")
	if ok && strings.Contains(attr, "pdf") {
		return newPDFLinkResult(URL, attr, "download-article"), nil
	}

	attr, ok = doc.Find("a.downloadPdf").Attr("href")
	if ok {
		return newPDFLinkResult(URL, attr, "downloadPdf"), nil
	}

	attr, ok = doc.Find("a.download-pdf").Attr("href")
	if ok {
		return newPDFLinkResult(URL, attr, "download-pdf"), nil
	}

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
	if strings.Contains(u, "arxiv.org/abs/") {
		return strings.Replace(u, "/abs/", "/pdf/", 1)
	}
	if strings.HasPrefix(u, "https://onlinelibrary.wiley.com/doi/") {
		return strings.Replace(u, "doi", "doi/pdf", 1)
	}
	if strings.Contains(u, "protocols.io/view/") && !strings.HasSuffix(u, ".pdf") {
		return u + ".pdf"
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

	// https://www.jcancer.org/v16p1684.html
	// https://www.jcancer.org/v16p1684.pdf
	if strings.Contains(u, "jcancer.org/") && strings.HasSuffix(u, ".html") {
		return strings.Replace(u, ".html", ".pdf", 1)
	}

	// https://www.jcancer.org/v16p1684.htm
	// https://www.jcancer.org/v16p1684.pdf
	if strings.Contains(u, "jcancer.org/") && strings.HasSuffix(u, ".htm") {
		return strings.Replace(u, ".htm", ".pdf", 1)
	}

	// https://www.tandfonline.com/doi/full/10.1080/19491247.2019.1682234
	// https://www.tandfonline.com/doi/pdf/10.1080/19491247.2019.1682234
	if strings.Contains(u, "tandfonline.com/doi/full/10.") {
		return strings.Replace(u, "/doi/full/", "/doi/pdf/", 1)
	}

	// https://www.isca-archive.org/interspeech_2025/pu25_interspeech.html
	// https://www.isca-archive.org/interspeech_2025/pu25_interspeech.pdf
	if strings.Contains(u, "isca-archive.org") && strings.HasSuffix(u, ".html") {
		return strings.Replace(u, ".html", ".pdf", 1)
	}

	// https://www.journals.uchicago.edu/doi/10.14318/hau1.1.008
	// https://www.journals.uchicago.edu/doi/epdf/10.14318/hau1.1.008
	if strings.Contains(u, "journals.uchicago.edu/doi/10") {
		return strings.Replace(u, "/doi/", "/doi/epdf/", 1)
	}

	// https://integrityresjournals.org/journal/JBBD/article-abstract/291855622
	// https://integrityresjournals.org/journal/JBBD/article-full-text-pdf/291855622
	if strings.Contains(u, "integrityresjournals.org/journal/JBBD/article-abstract/") {
		return strings.Replace(u, "/article-abstract/", "/article-full-text-pdf/", 1)
	}

	// https://cdnsciencepub.com/doi/10.1139/AS-2022-0011
	// https://cdnsciencepub.com/doi/pdf/10.1139/AS-2022-0011
	if strings.Contains(u, "cdnsciencepub.com/doi/10") {
		return strings.Replace(u, "/doi/", "/doi/pdf/", 1)
	}

	// https://www.worldscientific.com/doi/abs/10.1142/S0116110521500098
	// https://www.worldscientific.com/doi/pdf/10.1142/S0116110521500098?download=true
	if strings.Contains(u, "worldscientific.com/doi/abs/") {
		return strings.Replace(u, "/doi/abs/", "/doi/pdf/", 1) + "?download=true"
	}

	// https://www.ahajournals.org/doi/10.1161/circ.110.19.2977
	// https://www.ahajournals.org/doi/pdf/10.1161/circ.110.19.2977?download=true
	if strings.Contains(u, "ahajournals.org/doi/") && !strings.Contains(u, "/doi/pdf") {
		return strings.Replace(u, "/doi/", "/doi/pdf/", 1) + "?download=true"
	}

	// https://ehp.niehs.nih.gov/doi/full/10.1289/EHP4709
	// https://ehp.niehs.nih.gov/doi/pdf/10.1289/EHP4709?download=true
	if strings.Contains(u, "ehp.niehs.nih.gov/doi/full") && !strings.Contains(u, "doi/pdf") {
		return strings.Replace(u, "/doi/full/", "/doi/pdf/", 1) + "?download=true"
	}

	// https://ehp.niehs.nih.gov/doi/10.1289/ehp.113-a51
	// https://ehp.niehs.nih.gov/doi/pdf/10.1289/ehp.113-a51?download=true
	if strings.Contains(u, "ehp.niehs.nih.gov/doi/10") && !strings.Contains(u, "doi/pdf") {
		return strings.Replace(u, "/doi/", "/doi/pdf/", 1) + "?download=true"
	}

	// https://publications.rwth-aachen.de/record/986268/
	// https://publications.rwth-aachen.de/record/986268/files/986268.pdf
	if strings.Contains(u, "publications.rwth-aachen.de/record/") && !strings.HasSuffix(u, ".pdf") {
		u := strings.TrimSuffix(u, "/")
		split := strings.Split(u, "/")
		return u + "/files/" + split[len(split)-1] + ".pdf"
	}

	// https://mhealth.jmir.org/2020/7/e17891/
	// https://mhealth.jmir.org/2020/7/e17891/PDF
	if strings.Contains(u, ".jmir.org") && !strings.Contains(u, "/pdf") {
		return strings.TrimSuffix(u, "/") + "/PDF"
	}

	// https://array.aami.org/doi/10.2345/0899-8205-54.s3.31
	// https://array.aami.org/doi/pdf/10.2345/0899-8205-54.s3.31
	if strings.Contains(u, "array.aami.org/doi/10.") {
		return strings.Replace(u, "/doi/", "/doi/pdf/", 1) + "?download=true"
	}

	// ported without much context or meaning
	if strings.Contains(u, "drive.google.com/file/d/") && strings.Contains(u, "/view") {
		split := strings.Split(u, "/")
		if len(split) > 5 && len(split[5]) > 10 {
			return fmt.Sprintf("https://drive.google.com/uc?export=download&id=%s", split[5])
		}
	}

	return u
}
