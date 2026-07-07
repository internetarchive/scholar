# Scholar sitemap

Our primary source of traffic is Google Scholar. People search over there for
papers they want and we are one of several domains that Google Scholar uses to
redirect to for PDFs.

This documentation explains how to generate a _sitemap_ for Google Scholar to
consume; this is how they index our holdings and know what we can serve on
their behalf.

There are two kinds of sitemap entry:

- **work** URLs (`/work/{ident}`) — the scholar detail page for a work.
- **access** URLs (`/work/{ident}/access/...`) — the redirect endpoint that
  sends a visitor on to the actual PDF (a Wayback capture or an `archive.org`
  download).

We only list works whose fulltext we can actually serve, and we exclude content
that already has a canonical home elsewhere (arXiv, PubMed Central) or that is
paywalled big-5-publisher material that isn't public-domain or open-access. See
the generator's README for the exact filter.

## Frequency

The sitemap should be updated twice a year in January and June.

## Generation

Use the `scripts/sitemap` CLI. It scans the `scholar_fulltext` Elasticsearch
index once and writes all the sitemap files (thousands of `*.txt` files plus two
`*.xml` index files) into an output directory:

```sh
cd scripts/sitemap
GOWORK=off go build -o sitemap .

GOWORK=off go run . -count-only          # sanity check (~32M docs as of 2026)
./sitemap -outdir ./out                  # full run
```

The full run is long — roughly 12–13 hours single-threaded, because every
fulltext doc carries the whole article body in `_source`. Run it under
`tmux`/`screen`. If you need it faster and can accept more load on the shared
cluster, pass `-slices N` for N parallel scroll workers. Full flag and tuning
docs live in [`scripts/sitemap/README.md`](../scripts/sitemap/README.md).

The previous hand-run `fatcat-cli | jq | rg | awk | split` pipeline is kept for
reference under `scripts/sitemap/legacy/`.

## Serving

The generator only writes files; it does not deploy them. The sitemap files are
served from the root of `scholar.archive.org` (the XML index `<loc>` entries
point at `https://scholar.archive.org/sitemap-*.txt`), so copy the generated
`out/` contents to the directory the web servers serve from, and onto the
replica host. Historically this was:

```sh
scp out/*.txt out/*.xml $SCHOLARHOST:/srv/fatcat_scholar/sitemap
# repeat for the replica host
```

Adjust the destination to wherever the current djscholar deployment serves
static sitemap files from.

Google limits a sitemap file to 50k URLs / 10 MB (text) and Google Scholar has
indicated a stricter 20k URL / 5 MB limit, which is why files are split at
20,000 URLs.

## See also

- [`scripts/sitemap/README.md`](../scripts/sitemap/README.md) — the generator CLI.
- [`scripts/sitemap/legacy/`](../scripts/sitemap/legacy/) — the previous pipeline.
- Google sitemap verifier: <https://support.google.com/webmasters/answer/7451001>

