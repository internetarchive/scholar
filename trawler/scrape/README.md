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
and the daily crawl draws on the same pool. Over-large concurrency does not
fail, it just means more URLs waiting on SPN -- but see below for where that
waiting happens. Prefer raising concurrency gradually.

## When SPN has no session for us

A crawl that needs a fresh capture cannot start until SPN has a session free.
The scrape workflow does not wait for one in place: waiting would hold a worker
activity slot while doing nothing, and that slot is what a URL already in the
Wayback Machine needs in order to finish in the second or two it actually
takes. Instead the crawl hands the URL straight back, and Temporal redelivers
it after `crawling.spn_slot_poll_interval`. The waiting still happens, it just
stops costing a slot.

Two things keep that from turning into a stampede on SPN:

- Before spending a POST to find out, a crawl checks `/status/user` for the
  account's free-session count. The reading is cached for
  `crawling.spn_slot_status_ttl` and shared by every crawl in the worker, and
  it is spent down as crawls claim slots, so one query covers a burst. A crawl
  that SPN does turn away records that, and the crawls behind it defer without
  asking.
- Only the first few attempts defer (`spnDeferAttempts` in `scrape.go`). The
  last ones fall back to waiting in place, so an SPN outage that outlasts the
  deferrals cannot abandon URLs wholesale and trip `max_consecutive_errors`.

A deferred URL is not a failure and gets no outcome-log row: SPN never looked
at it, so there is nothing to record. Deferrals are also filtered out of Sentry
reporting, since at crawl concurrency they are routine.

The daily crawl is unchanged -- it still waits in place, because it walks a
release's URLs in one pass and has no way to come back to one later.

## Blocking a host mid-run

The crawl activity re-reads `crawling.url_blocklist` from the config file on
every execution, so adding a pattern -- in ansible, or straight into
`/etc/trawler/config` on the host when something needs stopping now -- takes
effect on the next crawl. No worker restart, no touching the running workflow.
A URL that matches is never fetched and lands in the outcome log as
`fail` / `blocklist`.

Two things to know. A crawl already in flight does not notice; the list is read
when the activity starts, so a retry picks it up but the current attempt runs to
completion. And emptying the list entirely is treated as a bad edit and ignored
(the previous list stays in force, with a warning) -- it holds hosts that asked
us not to crawl them, so silently dropping all of them is not a mistake worth
making cheap. Removing individual patterns works normally.

Note the asymmetry: this re-read is the scrape workflow's. The daily crawl still
reads the same list once at startup.

## The outcome log

Every seed URL gets one row in a TSV on the worker that crawled it, under
`scrape.log_dir`, in a file named for the workflow id:

    seed_url	status	wayback_url	reason	crawled_at
    https://repo.example/thesis/1	ok	https://web.archive.org/web/20260904…id_/https://repo.example/1.pdf
    https://repo.example/thesis/2	fail		dead-end	2026-09-04T17:31:02Z
    https://repo.example/thesis/3	error		spn api failure: …	2026-09-04T17:33:20Z

- `ok` -- we got a PDF into Wayback; `wayback_url` is its replay URL.
- `fail` -- the crawl finished without reaching a PDF. `reason` is the crawler's
  fail reason. In most of these SPN still captured a page, so the URL is in
  Wayback even though no PDF came of it.
- `error` -- the crawl never reached a verdict; it errored and we gave up.

`wayback_url` is only ever populated on `ok`, so the PDFs we captured are
`awk -F'\t' '$2=="ok" {print $1, $3}'` and everything else is
`grep -v -P '\tok\t'`.

Rows are written at the moment a crawl reaches its verdict, from inside the
crawl activity -- so a retried attempt does not leave a row behind for each try.
The one outcome the activity cannot record is a URL whose retries ran out, since
it never returned; the workflow logs those itself.

With several workers on the queue each host holds the rows for the URLs it
happened to take, so `cat` them together for the whole run. Rows append across
ContinueAsNew, so one file covers a scrape however many runs it took.

A failure to write a row fails the run rather than being swallowed: the log is a
deliverable here, so an unwritable `log_dir` should surface immediately instead
of quietly losing the record of what happened.

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
