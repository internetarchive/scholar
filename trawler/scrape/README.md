# scrape: URL list -> Wayback

A deliberately small Temporal workflow: read a list of URLs, crawl each one with
`crawling.PDFCrawler`, and let SPN put what it finds in the Wayback Machine.

It is not the daily crawl. There is no upstream metadata API, no fatcat records,
no GROBID, no search indexing. It exists for piles of URLs we only want
captured -- for example the crawl-only half of an ETD ingest split, where a
landing page or even a single chapter PDF reaching Wayback is the whole point.

## Running one

The list is a newline-delimited file of URLs in S3 (blank lines ignored), keyed
`bucket/path`:

    trawler scrape one-off --list scholar/etd/wayback-seeds.txt --concurrency 6

and somewhere with internet access:

    trawler scrape start-worker

The workflow ID is derived from the list key, so a second dispatch of the same
list while one is running is rejected rather than doubling the load on SPN. Once
a run has closed the same list may be re-run; a re-run is cheap for URLs already
captured, because the crawler serves them from CDX when a capture is newer than
`crawling.cdx_freshness_cutoff` and only spends SPN time on the rest.

## Throttles

Three, in increasing order of bluntness:

1. **`--concurrency` / `scrape.concurrency`** -- how many crawls the workflow
   keeps in flight. This is the knob to reach for, and it can be changed on a
   running scrape:

       trawler scrape set-concurrency --list scholar/etd/wayback-seeds.txt --concurrency 2

   The signal is clamped to `scrape.max_concurrency`; out-of-range values are
   logged and ignored.

2. **`scrape.worker_activity_slots`** -- how many crawl activities a single
   worker will accept. With several workers on the queue this, not the
   workflow's concurrency, is what bounds total simultaneous load.

3. Stopping the workers.

Worth knowing: SPN allows a limited number of concurrent sessions per account,
and the daily crawl draws on the same pool. `PDFCrawler` handles exhaustion by
polling for a slot, so over-large concurrency does not fail -- it just parks
activities in a polling loop, competing with the daily crawl for slots. Prefer
raising concurrency gradually.

## What counts as success

`PDFFound` means the crawl ended on a PDF. `NoPDF` means it did not -- but in
most of those cases SPN still captured a page, which for this workflow is a
perfectly good outcome; the URL is in Wayback either way. `Skipped` is the
subset that never touched the network at all (blocklisted or unparsable URLs),
and is the only category where nothing was archived. `FailReasons` breaks the
last two down by `CrawlResult.FailReason`, which is the useful thing to look at
when a whole repository is coming back empty.

## Failure handling

A single URL's crawl failing never fails the run. A list of millions will always
contain dead hosts, and one of them must not strand the rest: after its retries
are exhausted the URL is counted in `Errored` and the scrape moves on.

`scrape.max_consecutive_errors` is the exception. An unbroken streak of failures
is not bad URLs, it is something systemic -- wrong SPN credentials, SPN down --
and burning the rest of the list against it helps nobody, so the run aborts.
Any single success resets the streak.

## Resuming

The workflow hands off via ContinueAsNew every `scrape.urls_per_can` URLs to
keep any one run's history bounded. Before it does, it drains the in-flight
window, so the byte offset it carries forward is the end of a URL that has
actually finished -- no re-crawls, no skips.
