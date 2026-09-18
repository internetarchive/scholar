# daily: one day of an upstream -> fatcat, Wayback, scholar

Scrape one day out of one upstream metadata API (crossref, arxiv, doaj,
datacite, pubmed) into an ndjson in S3, then walk it: each record becomes a
fatcat release, and a release worth having gets its fulltext crawled through
SPN, run through GROBID, and indexed into `fatcat_file` and `scholar_fulltext`.

It is the same shape as `scrape` and `periodic`: the object is cut into byte
ranges and one activity works a whole range with a pool of its own, so Temporal
sees a handful of history events per few hundred records rather than per
record.

## Running one

    trawler daily one-off --upstream crossref --day 2026-05-20 --concurrency 8

and two workers, because the work splits across two networks:

    trawler daily start-external-worker --upstream crossref   # reaches the upstream API
    trawler daily start-internal-worker --upstream crossref   # everything else

The external worker runs only `ScholkitScrapeActivity`, which shells out to
scholkit and leaves the day's ndjson in S3. Everything after that -- planning,
crawling, GROBID, fatcat, ES -- is the internal worker's.

The workflow ID is `<upstream>_daily_<yyyymmdd>`, so a second dispatch of the
same day while one is running is rejected rather than doubling the load. Once a
run has closed the same day may be re-run, which is worth doing after a crawler
improvement: a re-run is cheap for everything already done, because every step
checks whether its output is already there.

Scheduled runs are separate (`trawler daily start-scheduler --upstream X`) and
work a day back — DOAJ a month and a day back, because their API will not serve
non-paying callers anything newer.

## Throttles

1. **`--concurrency` / `daily.concurrency`** -- how many releases the run works
   at once, across all of its batches. This is the knob to reach for, and it
   can be changed on a running crawl:

       trawler daily set-concurrency --upstream crossref --day 2026-05-20 --concurrency 2

   A retune reaches the batches dispatched after it, not the ones already
   running, so it takes a batch to bite. The value is clamped to
   `daily.max_concurrency`; out-of-range values are logged and ignored.

   A scheduled run's workflow ID carries the scheduled time, so `--day` cannot
   derive it; pass `--workflow-id` from the Temporal UI instead.

2. **`daily.worker_activity_slots`** -- how many *batches* a single worker will
   accept. What a worker puts on SPN and GROBID is this times a batch's
   parallelism, so keep it small and let concurrency do the tuning.

3. Stopping the internal workers.

SPN allows a limited number of concurrent sessions per account and the scrape
workflow draws on the same pool, so raise concurrency gradually. A crawl with
no session free waits in place: it holds one goroutine of its batch's pool
rather than a Temporal slot, and the records behind it keep moving.

## Blocking a host or a publisher mid-run

`crawling.url_blocklist` and `crawling.doi_prefix_blocklist` are re-read from
the config file every `daily.blocklist_reload_interval`, so adding a pattern --
in ansible, or straight into `/etc/trawler/config` when something needs
stopping now -- takes effect within the minute, on batches already running. No
worker restart, no touching the workflow.

Emptying either list entirely is treated as a bad edit and ignored (the
previous list stays in force, with a warning); removing individual patterns
works normally.

## The outcome log

Every record gets one row in a TSV on the worker that handled it, under
`daily.log_dir`, in a file named for the workflow id:

    upstream  ext_id  release_id  seed_url  status  detail  file_id  wayback_url  crawled_at

- `ok` -- we ended with a PDF in fatcat and/or an index document. `detail` says
  what was written (`created file, indexed fulltext`), or `already current` if
  everything was there before we looked.
- `skip` -- we passed on the record before crawling: no release came of it, it
  is not paperlike, its DOI prefix is blocked, it has no fulltext URLs, or the
  release already has files. This is the common case and not a problem.
- `fail` -- we tried and came away with nothing: no URL for the release reached
  a PDF (`no pdf: <crawler reason>`), or GROBID made nothing of the one we got.
- `error` -- the record never reached a verdict; it errored, exhausted its
  retries, and we abandoned it.

This exists because batching took the per-record view away. When every line was
its own activity, "why did this DOI never get a PDF" was answerable from the
Temporal UI; a batch of hundreds of records is one history event.

With several workers on the queue each host holds the rows for the records it
happened to take, so `cat` them together for the whole day. Rows append across
ContinueAsNew, so one file covers a day however many runs it took. A record can
appear twice — a batch resumed from its last checkpoint redoes whatever it had
finished past that point — so treat a repeated `ext_id` as one record.

A failure to write a row fails the batch rather than being swallowed: an
unwritable `log_dir` should surface immediately instead of quietly losing the
record of what happened.

## Failure handling

A single record failing never fails the run. A day of crossref will always
contain records fatcat chokes on and PDFs GROBID will not answer for, and one
of them must not strand the rest: after `daily.line_attempts` the record is
counted in `Errored` and the day moves on.

A crawl that finds no PDF is not even that -- it is an ordinary verdict, logged
as `fail` and counted, because most of the open web does not hand us fulltext.
So the circuit breaker below is deliberately deaf to crawling: what trips it is
fatcat, GROBID or ES failing, which is the kind of thing a human should hear
about.

`daily.max_consecutive_errors` is that breaker. An unbroken streak of errors is
not bad records, it is something systemic, and burning a whole day against it
helps nobody, so the run aborts and says where to resume. Any single verdict
resets the streak.

## Resuming

The workflow hands off via ContinueAsNew every `daily.lines_per_can` records to
keep any one run's history bounded. Before it does, it drains the in-flight
window, so the byte offset it carries forward is the end of a batch that has
actually finished -- no redone records, no skips. The scraped S3 key and the
source label are pinned on the first run and carried, so a day never re-scrapes
and all of its records share one `source`.

Inside a batch the same idea, one level down: the activity heartbeats a
checkpoint covering the contiguous prefix it has finished, and a worker that
dies mid-batch has the batch redelivered to another one, which picks up there.
