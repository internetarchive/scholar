# scholar sitemap generator

Generates the large sitemap of our works that is consumed by Google Scholar (and others, I assume).

Intended to be run at least twice a year (November, May).

It scans the `scholar_fulltext` Elasticsearch index once and writes, into `-outdir`:

- `sitemap-works-NNNNN.txt`  — one `/work/{ident}` detail-page URL per matched work
- `sitemap-access-NNNNN.txt` — one `/work/{ident}/access/...` redirect URL per work
- `sitemap-index-works.xml`  — sitemap index listing the works files
- `sitemap-index-access.xml` — sitemap index listing the access files

Files are split at 20,000 URLs (Google Scholar's stated limit).

## Build & run

```sh
# build it
go build

# pre-check to make sure things look sane
./sitemap -count-only

# sample run
./sitemap -limit 50000 -outdir /tmp/smap

# the real thing — long-running, see "Performance" below
./sitemap -outdir ./out
```

It uses https://scholar.archive.org/_es which works as long as you are on the VPN.

## Flags

| flag | default | meaning |
|------|---------|---------|
| `-es-url` | `https://scholar.archive.org/_es` | Elasticsearch base URL |
| `-index` | `scholar_fulltext` | index name (an alias) |
| `-base-url` | `https://scholar.archive.org` | prefix for emitted URLs and `<loc>` entries |
| `-outdir` | `.` | output directory |
| `-per-file` | `20000` | max URLs per sitemap file |
| `-page-size` | `10000` | scroll batch size (hard-capped at the index `max_result_window` of 10000) |
| `-pd-year` | `0` | public-domain cutoff year; `0` computes it from the current year |
| `-slices` | `1` | parallel sliced-scroll workers; see Performance |
| `-keep-alive` | `5m` | scroll context keep-alive |
| `-limit` | `0` | stop after N docs (0 = unlimited); for smoke tests |
| `-count-only` | `false` | print the matching count and exit |

## how works are included

this is based on Bryan Newbold's original work in the `legacy/` subdir.

```
doc_type:work
(fulltext.access_type:ia_file OR fulltext.access_type:wayback)   # access we can redirect to
(NOT biblio.arxiv_id:*)                                          # arXiv has a canonical home
(NOT biblio.pmcid:*)                                             # PMC has a canonical home
((NOT biblio.publisher_type:big5)                                # avoid paywalled big-5 content,
   OR biblio.release_year:<=<pd-year> OR tags:oa)                #   unless public-domain or OA
```

The public-domain cutoff is computed as `currentYear - 96` (US 95-year term plus
the Jan-1 entry convention), so it advances automatically each year. Override
with `-pd-year`.

## notable difference from legacy scripts

The legacy pipeline dropped a work from **both** sitemaps if its primary access
URL was a 12-digit-timestamp Wayback URL (which the access-redirect endpoint
can't serve). This tool keeps the work's `/work/{ident}` detail URL (a valid,
indexable page) and skips only its `access` line. Such skips are counted as
`access_skipped` in the final summary.

TODO understand why access won't serve those timestamps

## performance

Each `scholar_fulltext` doc embeds the full article text, so loading `_source`
dominates the scan cost (the work URL itself is taken from the doc `_id`, which
is free; only `access_url`, mapped `doc_values:false`, forces a `_source` read).

- **`-slices 1` (default):** one scroll request in flight at a time, ~700 docs/s,
  so **~12–13h for the full ~32M docs**.
- **`-slices N`:** N sliced-scroll workers in parallel

The scroll uses `sort: ["_doc"]` and `_source` filtering, and retries on
429/5xx with backoff (see `doPost`).

## deploying the sitemap

this part is amazingly low tech. Consult `ait-ansible` for what hosts are in the `scholar-sitemap` group. get the output of this command in place at `/srv/scholar/sitemap` (or whatever the sitemap nginx in ansible is configured to serve). Move the previous sitemap dir to `sitemap-old` after cleaning up any lingering previous sitemap.

If you wanted to, future human, you could try to automate all of this.
