# intermediate report

> 2024-06-25, 2024-06-27

goal: batch versus real-time; trying to reduce dependency on kafka

Steps, exploration towards that goal:

### upstream data to release format ("mdconv")

A go tool to convert one metadata format to another: "mdconv", like "iconv", "uconv"

> arxiv, crossref, datacite, dblp, doaj, oaiscrape, oai, openalex, pubmed,

Example:

```
$ curl -sL https://archive.org/download/crossref-2024-05-01/crossref-2024-05-01.ndj.zst | \
    zstd -T0 -dc | ./mdconv -f crossref | zstd -c -T0 > crossref-2024-05-01.release.json.zst
```

status:

```go
var availableSourceFormats = []string{
	"arxiv",
	"crossref",
	"datacite",
	"dblp",
	"doaj",
	"oaiscrape",
	"oai",
	"openalex",
	"pubmed",
	"fatcat-release", // for downstream tasks
}
```

Example merge (on k9; about 100G compressed), 800M docs; takes 8 min to iterate;
nvme/raid; can read 150MB/s compressed data off disk, when fed into [cw](https://github.com/Freaky/cw)

```
$ zstdcat -T0 /var/data/tmp/fatcat.ndj.zst | pv -l > /dev/null
 812M 0:07:50 [1.73M/s]
```

### work clustering locally

* prototype to cluster release based on ID and slug title (crude, effective, efficient)
* tool: "clowder"

not decided on the "id" handing; if we give out a "work" or "cluster" id, how
do we rederive that? we need to keep that around in a database and on the next
iteration, need to compare existing cluster result with the previous database

Could use a simple database:

(clusterid, releaseid)

Then redirect the cluster standalone, then for each found cluster:

* choose a representative "releaseid"
* associated newly found "releaseid" set with existing "clusterid" (so members of a cluster may change)

In general, search should work, but dependence on the cluster id would be more brittle.


### chocula

* tried to port chocula to Go quickly ("jmag"), but lots of string manipulation, better handled with python
* tried to move chocula to "luigi", in "lipa", can demo; separate business logic from weaving together tasks
* updated existing sources in may (+8K journals); added openalex in june (+3K journals)
* more sources, updates available

Would like to make this more automated.

Todo:

* hathifiles have been updated
* openapc


### file entity

* hackery around crawl logs
* targeted crawls:

```        T
2024  764  NM
2023  561  M
2022  559  BM
2021  935  BM
2020 9837  BM
2019 3665  B
2018 3057  B
2017 3116  B
```

total of 46 crawls, including test crawls; about 6 p.a.

For 38, we seem to have the full crawl logs (a crawl may have more than one
crawl log file):

```shell
$ ia search 'identifier:*full_crawl_logs'
{"identifier": "ARXIV-CRAWL-2019-10-full_crawl_logs"}
{"identifier": "ARXIV-PUBMEDCENTRAL-CRAWL-2020-04-full_crawl_logs"}
{"identifier": "CORE-UPSTREAM-CRAWL-2018-11-full_crawl_logs"}
{"identifier": "DATACITE-DOI-CRAWL-2020-01-full_crawl_logs"}
{"identifier": "DATASET-CRAWL-2022-01-full_crawl_logs"}
{"identifier": "DIRECT-OA-CRAWL-2019-full_crawl_logs"}
{"identifier": "DOAJ-CRAWL-2020-11-full_crawl_logs"}
{"identifier": "DOI-CRAWL-2022-02-full_crawl_logs"}
{"identifier": "DOI-LANDING-CRAWL-2018-06-full_crawl_logs"}
{"identifier": "JOURNAL-HOMEPAGE-CRAWL-2022-03-full_crawl_logs"}
{"identifier": "JOURNALS-PATCH-CRAWL-2022-01-full_crawl_logs"}
{"identifier": "MAG-PDF-CRAWL-2020-03-full_crawl_logs"}
{"identifier": "MAG-PDF-CRAWL-2020-07-full_crawl_logs"}
{"identifier": "MAG-PDF-CRAWL-2021-08-full_crawl_logs"}
{"identifier": "OA-DOI-CRAWL-2020-02-full_crawl_logs"}
{"identifier": "OA-DOI-CRAWL-2020-12-full_crawl_logs"}
{"identifier": "OA-JOURNAL-CRAWL-2019-08-full_crawl_logs"}
{"identifier": "OA-JOURNAL-CRAWL-2020-07-full_crawl_logs"}
{"identifier": "OAI-PMH-CRAWL-2020-06-full_crawl_logs"}
{"identifier": "OAI-PMH-CRAWL-2022-10-full_crawl_logs"}
{"identifier": "OAI-PMH-CRAWL-2023-08-full_crawl_logs"}
{"identifier": "OAI-PMH-PATCH-CRAWL-2021-12-full_crawl_logs"}
{"identifier": "OMICS-DOI-LANDING-CRAWL-2019-04-full_crawl_logs"}
{"identifier": "PUBMEDCENTRAL-CRAWL-2019-10-full_crawl_logs"}
{"identifier": "PUBMEDCENTRAL-CRAWL-2020-02-full_crawl_logs"}
{"identifier": "SCIELO-CRAWL-2020-07-full_crawl_logs"}
{"identifier": "TARGETED-ARTICLE-CRAWL-2022-03-full_crawl_logs"}
{"identifier": "TARGETED-ARTICLE-CRAWL-2022-04-full_crawl_logs"}
{"identifier": "TARGETED-ARTICLE-CRAWL-2022-07-full_crawl_logs"}
{"identifier": "UNPAYWALL-PDF-CRAWL-2018-07-PATCH-full_crawl_logs"}
{"identifier": "UNPAYWALL-PDF-CRAWL-2018-07-full_crawl_logs"}
{"identifier": "UNPAYWALL-PDF-CRAWL-2019-04-full_crawl_logs"}
{"identifier": "UNPAYWALL-PDF-CRAWL-2020-03-full_crawl_logs"}
{"identifier": "UNPAYWALL-PDF-CRAWL-2020-05-full_crawl_logs"}
{"identifier": "UNPAYWALL-PDF-CRAWL-2020-11-full_crawl_logs"}
{"identifier": "UNPAYWALL-PDF-CRAWL-2021-05-full_crawl_logs"}
{"identifier": "UNPAYWALL-PDF-CRAWL-2021-07-full_crawl_logs"}
{"identifier": "UNPAYWALL-PDF-CRAWL-2022-04-full_crawl_logs"}
```

For each crawl log file, we can generate an sqlite3 db with (cf.
[makta](https://github.com/miku/makta),
[sqlite-utils](https://sqlite-utils.datasette.io/en/stable/)):

```
URL, previous URL
```

which then forms a trace, e.g. for some PDF, we can get the associated seed back.

* works for landing page crawls, DOI crawls
* does not work for "journal homepage" crawls, which do broader crawl

However, the association between `ingest_request` and `FileEntity` could be
made shorter via crawl logs.

What is missing from the `FileEntity` still?

We get `(url, ftsurl)` from crawl log; and the starting URL we have in the
metadata as well.

How many PDF to seed associations can we get? On my laptop, it takes a while to
turn 100GB compressed logs into sqlite3 databases:

```shell
$ time find full_crawl_logs/ -name "*crawl.log.gz" | \
    grep -v "OAI-PMH-CRAWL-2020-06" | \
    parallel --lb -I {} -j 8 './clogdb -C 200000 {}'
```

Started `2024/06/26 10:36:09` + 15min

But not longer than a few hours.

First, how many HTTP 200 PDFs are in there at all? From some CDX analysis in
april, we found that we have 117M PDF captures. But since some logs are
missing, we expect fewer here.



## outlook

* [ ] consolidate
* [ ] reduce
* [ ] rewrite
* [ ] toolize (better with an exact spec)

