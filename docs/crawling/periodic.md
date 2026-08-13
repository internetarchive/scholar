# Scholar Periodic Crawling

We use the term "periodic" to refer to crawls that we run across large sets of URLs to fill gaps in our holdings. These might be URLs that the daily crawl tried and failed to capture, a set of academic journal homepages, a bulk set of metadata from another service, or something else.

These crawls result in a large pile of warcs. We have a special temporal workflow that can be pointed at a warc collection on archive.org to extract PDFs found during the crawl and associate them with records in Fatcat.

You can see what workflows are running in temporal in the `scholar_trawler` namespace; look for the `periodic_ingest` prefix in the workflow ID.

These are initiated via the `trawler` command from anywhere that can access the Temporal API:

```
trawler ingest-warcs <collection url or ID>
```

for the kind of shape it expects, see [this catchup crawl](https://archive.org/details/CATCHUP-CRAWL-2025-06).

## Health

TODO

## Systems

TODO, should move the list from Daily into a general trawler.md doc
