# BLOBPROC

> Queues like it's 1995!

![](static/00741.png)

## Build

Build binaries:

```shell
$ make
```

Create a debian package:

```shell
$ make deb
```

Release a new version:

```shell
$ make update-version V=1.2.3                    # updates VERSION and version.go
$ git commit -am "v1.2.3" && git tag v1.2.3      # tag needs this format for CI!
$ git push origin main && git push origin --tags # CI build deb, uploads it to nexus
```

## Background

BLOBPROC is a less kafkaesque version of PDF postprocessing found in
sandcrawler, which is part of [IA Scholar](https://scholar.archive.org) infra.
Specifically it is designed to process and persist documents with minimum
number of external components and little to no state.

The goal is to have artifacts (fulltext, thumbnails, metadata, ...)  derived
from millions of PDF files available in a storage system (e.g. S3). In the best
case, the artifacts can be kept up to date in an unattended way.

BLOBPROC currently ships with two cli programs:

* **blobprocd** exposes an HTTP server that can receive binary data and stores
  it in a
  [spool](https://refspecs.linuxfoundation.org/FHS_3.0/fhs/ch05s14.html) folder (maybe a better name would be `blob-spoold`)
* **blobproc** is a process that scans the spool folder and executes post
  processing tasks on each PDF, and removes the file from spool, if a
  best-effort-style processing of the file is done (periodically called by a
  systemd timer) (this is a one off command, not a server)

In our case pdf data may come from:

* Heritrix crawl, via a [ScriptedProcessor](https://github.com/miku/blobproc/blob/8e9f091ea83c46b024b0c74ee7900b1fb84c4174/extra/heritrix/fetch-processor-snippet.xml#L30-L137)
* (wip) a WARC file, a crawl collection or similar
* in general, by any process that can deposit a file in the spool folder or send an HTTP request to blobprocd

In our case blobproc will execute the following tasks:

* send PDF to [GROBID](https://github.com/kermitt2/grobid) and store the result in **S3**, using [grobidclient](https://github.com/miku/grobidclient) Go library
* generate text from PDF via [go-fitz](https://github.com/gen2brain/go-fitz) (MuPDF, in-process) and store the result in S3 ([seaweedfs](https://github.com/seaweedfs/seaweedfs))
* generate a thumbnail from PDF via [go-fitz](https://github.com/gen2brain/go-fitz) (MuPDF, in-process) and store the result in S3 ([seaweedfs](https://github.com/seaweedfs/seaweedfs))
* find all weblinks in the PDF text and send them to a crawl API (wip)

More tasks can be added by extending blobproc itself. A focus remains on simple
deployment via an OS distribution package. By pushing various parts into library
functions (or external packages like [grobidclient](https://miku/grobidclient)), the main processing routine shrinks to about [100 lines of
code](https://github.com/miku/blobproc/blob/37f9cd7873f1e08400f46e98640e2b24bd37a088/walker.go#L64-L166)
(as of 08/2024). Currently both blobproc and blobprocd run on a dual-core [2nd
gen
XEON](https://ark.intel.com/content/www/us/en/ark/products/193394/intel-xeon-silver-4216-processor-22m-cache-2-10-ghz.html) with 24GB of RAM;
blobprocd received up to 100 rps and wrote pdfs to rotational disk.

## Bulk, back-of-the-envelope, reprocessing

Currently, about 5 pdfs/s. GROBID may be able to handle up to 10 pdfs/s. To
reprocess, say 200M pdfs in less than a month, we would need about 10 GROBID
instances.

## Mode of operation

* receive blob over HTTP, may be heritrix, curl, some backfill process
* regularly scan spool dir and process found files

## Usage

Server component.

```
$ blobproc serve --help
Start an HTTP server that receives binary PDF data via POST or PUT
requests and stores them in the spool folder for later processing.

The server provides the following endpoints:
  POST/PUT /spool    - Upload a PDF blob
  GET /spool         - List spool contents
  GET /spool/{id}    - Get status of a specific spool item

Usage:
  blobproc serve [flags]

Flags:
      --access-log string         access log file (empty = discard)
      --addr string               server listen address (default "0.0.0.0:8000")
  -h, --help                      help for serve
      --server-timeout duration   server read/write timeout (default 15s)
      --urlmap-file string        URL map database file (empty = disabled)
      --urlmap-header string      HTTP header for URL mapping (default "X-Original-URL")

Global Flags:
      --config string              config file (searches: ./blobproc.yaml, /home/tir/.config/blobproc/blobproc.yaml, /etc/blobproc/blobproc.yaml)
      --debug                      enable debug logging
      --grobid-host string         GROBID host URL (default "http://localhost:8070")
      --grobid-max-filesize int    max file size for GROBID in bytes (default 268435456)
      --grobid-timeout duration    GROBID request timeout (default 30s)
      --log-file string            log file path (empty = stderr)
      --s3-access-key string       S3 access key (default "minioadmin")
      --s3-default-bucket string   S3 default bucket (default "sandcrawler")
      --s3-endpoint string         S3 endpoint (default "localhost:9000")
      --s3-secret-key string       S3 secret key (default "minioadmin")
      --s3-use-ssl                 use SSL for S3 connections
      --spool-dir string           spool directory path (default "/home/tir/.local/share/blobproc/spool")
      --timeout duration           subprocess timeout (default 5m0s)
```

Processing command line tool.

```
$ blobproc run --help
Process all PDF files in the spool directory, generating
derivatives and storing them in S3. This is the main processing mode.

Usage:
  blobproc run [flags]

Flags:
  -h, --help          help for run
  -k, --keep          keep files in spool after processing
  -w, --workers int   number of parallel workers (1=sequential, >1=parallel) (default 4)

Global Flags:
      --config string              config file (searches: ./blobproc.yaml, /home/tir/.config/blobproc/blobproc.yaml, /etc/blobproc/blobproc.yaml)
      --debug                      enable debug logging
      --grobid-host string         GROBID host URL (default "http://localhost:8070")
      --grobid-max-filesize int    max file size for GROBID in bytes (default 268435456)
      --grobid-timeout duration    GROBID request timeout (default 30s)
      --log-file string            log file path (empty = stderr)
      --s3-access-key string       S3 access key (default "minioadmin")
      --s3-default-bucket string   S3 default bucket (default "sandcrawler")
      --s3-endpoint string         S3 endpoint (default "localhost:9000")
      --s3-secret-key string       S3 secret key (default "minioadmin")
      --s3-use-ssl                 use SSL for S3 connections
      --spool-dir string           spool directory path (default "/home/tir/.local/share/blobproc/spool")
      --timeout duration           subprocess timeout (default 5m0s)
```

## Performance data points

The initial, unoptimized version would process about 25 pdfs/minute or 36K
pdfs/day. We were able to crawl much faster than that, e.g. we reached 63G
captured data (not all pdf) after about 4 hours. GROBID should be able to
handle up to 10 pdfs/s.

A parallel walker could process about 300 pdfs/minute, and would match the
inflow generated by one heritrix crawl node.

## Scaling

* [x] tasks will run in parallel, e.g. text, thumbnail generation and grobid all run in parallel, but we process one file by one for now
* [ ] we should be able to configure a pool of grobid hosts to send requests to

## Backfill

* [ ] point to CDX file, crawl collection or similar and have all PDF files sent to BLOBPROC, even if this may take days or weeks

## TODO

* [ ] for each file placed into spool, try to record the URL-SHA1 pair somewhere
* [ ] pluggable write backend for testing, e.g. just log what would happen
* [ ] log performance measures
* [ ] grafana

## ASCII

```
                      PDF SOURCES
                          │
          ┌───────────────┼───────────────┐
          │               │               │
      Heritrix      WARC Files        Manual/
      Crawler         │               curl/etc
          │         blobfetch              │
          │           │                    │
          │           ├─────┐              │
          │           │     │              │
          │           v     v              v
          │      ┌─────────────────────────┐
          └─────>│   blobproc serve        │
                 │  (HTTP endpoint)        │
                 │  :8000/upload           │
                 └──────────┬──────────────┘
                            │
                            v
                 ┌──────────────────────┐
                 │   SPOOL DIRECTORY    │
                 │  ~/.local/share/...  │
                 │   (file queue)       │
                 └──────────┬───────────┘
                            │
                            v
                 ┌──────────────────────┐
                 │   blobproc run       │<─── systemd timer
                 │  (batch processor)   │     (periodic)
                 └──────────┬───────────┘
                            │
              ┌─────────────┼─────────────┐
              │             │             │
              v             v             v
        ┌─────────┐   ┌─────────┐   ┌─────────┐
        │ GROBID  │   │ go-fitz │   │ go-fitz │
        │ (XML)   │   │ (text)  │   │ (thumb) │
        └────┬────┘   └────┬────┘   └────┬────┘
             │             │             │
             └─────────────┼─────────────┘
                           │ (parallel)
                           v
                     ┌───────────┐
                     │ S3 Store  │
                     │(seaweedfs)│
                     └───────────┘
                           │
                           v
                      [Artifacts]
                    (fulltext.txt)
                    (metadata.xml)
                    (thumbnail.png)
```

## Notes

This tool should cover most of the following areas from sandcrawler:

* `run_grobid_extract`
* `run_pdf_extract`
* `run_persist_grobid`
* `run_persist_pdftext`
* `run_persist_thumbnail`

Including references workers.

Performance: Processing 1605 pdfs, 1515 successful, 2.23 docs/s, when processed
in parallel, via `fd ... -x` - or about 200K docs per day.

```
real    11m0.767s
user    73m57.763s
sys     5m55.393s
```

