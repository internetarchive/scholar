# scholkit

```
   ,   ,
  /////|
 ///// |
|~~~|  |
|===|  |
|j  |  |
| g |  |
|  s| /
|===|/
'---'
```

Scratch project, assorted utilities around scholarly metadata formats and
tasks; mostly data acquisition for now.

status: wip, api and cli not stable yet; some tools are fragments

## Try

```
$ git clone https://git.archive.org/webgroup/scholar
$ cd scholar/scholkit
$ make
```

This builds a couple of executables:

* sk-feed (download raw data, mostly with external tools)
* sk-convert (data format conversions)
* sk-cat (stream data from many urls)
* sk-cdx (ad-hoc cdx api lookup)
* sk-norm (quick string normalization)

Example dataset to work with, e.g. convert arxiv to fatcat release:

```sh
$ curl -sL https://archive.org/download/arxiv-2024-02-15/arxiv-2024-02-15.xml.zst | \
    zstd -dc | \
    sk-convert -f arxiv
```

## Tools

### Conversions

We want conversions from various formats to one single format (e.g. release
entities). Source formats include:

* [x] crossref
* [x] datacite
* [x] pubmed
* [x] arxiv
* [x] oaiscrape
* [x] openalex
* [x] dblp

Target:

* [ ] fatcat entities (release, work, container, file, contrib, abstract)

Note: For each format, try to find the smallest conversion unit, e.g. one record.
Then add convenience layers on top, e.g. for streams.

No bulk conversion should take longer than an 1 hour, roughly (slowest
currently is openalex - 250M records - which takes about 45 min).

### Clustering

TODO: Create a "works" view from releases.

### Misc

The `sk-cat` utility streams content from multiple URLs to stdout. Can help
to create single file versions of larger datasets like pubmed, openalex, etc.

```
$ curl -s "https://www.gutenberg.org/browse/scores/top" | \
    grep -Eo "/ebooks/[0-9]+" | \
    awk '{print "https://gutenberg.org"$0".txt.utf-8"}' > top100.txt

$ sk-cat < top100.txt > top100books.txt
```

## Notes

* [upstream id comparison](notes/2024-02-09-upstream-ids.md)
* [stub releases](notes/2024-03-15-first-stub-releases.md)
* [url list](notes/2024-03-27-url-list.md)

## TODO

* [ ] implement schema conversions and tests
* [ ] add layer for daily harvests and capturing data on disk
* [ ] cli to interact with the current files on dist
* [ ] cli for basic stats
* [ ] some simplistic index/query structure, e.g. to quickly find a record by id or the like
* [ ] pubmed resilience
* [ ] datacite fix (use monthly dumps, not api)

More:

* [x] map basic fields to fatcat release entities
* [ ] map all fields to fatcat release entities
* [ ] basic clustering algorithm

