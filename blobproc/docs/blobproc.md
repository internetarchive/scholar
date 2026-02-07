% BLOBPROC(1) | blobproc User Manual

NAME
====

**blobproc** — PDF postprocessing utility for derivative generation

SYNOPSIS
========

| **blobproc** [*global-options*] *command* [*command-options*]
| **blobproc** **run** [**-w** *workers*] [**-k**]
| **blobproc** **serve** [**--addr** *address*]
| **blobproc** **single** *file.pdf*
| **blobproc** **config** [**--show-defaults**]

DESCRIPTION
===========

**blobproc** is a PDF postprocessing utility that generates derivatives like fulltext,
thumbnails, and metadata from PDF files. It processes PDFs through external tools (GROBID,
pdftotext, pdftoppm) and can persist results to S3-compatible storage.

The typical workflow involves running **blobproc serve** to accept incoming PDFs, which are
stored in a spool directory. A separate process or timer runs **blobproc run** to process
spooled files and generate derivatives.

OVERVIEW
========

Overview of data flow, from top to bottom.

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
        │ GROBID  │   │pdftotext│   │pdftoppm │
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

COMMANDS
========

**run**
:   Process all PDF files from the spool directory. Generates GROBID XML, extracted text,
    thumbnails, and metadata, then stores results in S3. Use **-w** to control parallelism
    (default: 4 workers). Use **-k** to keep files in spool after processing.

**serve**
:   Start HTTP server on **--addr** (default: 0.0.0.0:8000) to receive PDF blobs via POST/PUT
    requests. Provides endpoints: **/spool** (upload/list), **/spool/{id}** (status).

**single** *file*
:   Process a single PDF file for testing. Emits JSON with extracted data to stdout without
    persisting to S3.

**config**
:   Display current configuration values. Use **--show-defaults** to see default values,
    **--show-file** to show config file location.

**completion** *shell*
:   Generate shell autocompletion script for bash, zsh, fish, or powershell.

OPTIONS
=======

## Global Options

**--config** *path*
:   Configuration file path. Searches: ./blobproc.yaml, ~/.config/blobproc/blobproc.yaml,
    /etc/blobproc/blobproc.yaml

**--spool-dir** *path*
:   Spool directory path (default: ~/.local/share/blobproc/spool)

**--grobid-host** *url*
:   GROBID server URL (default: http://localhost:8070)

**--grobid-timeout** *duration*
:   GROBID request timeout (default: 30s)

**--grobid-max-filesize** *bytes*
:   Maximum file size for GROBID processing (default: 268435456)

**--s3-endpoint** *host:port*
:   S3-compatible storage endpoint (default: localhost:9000)

**--s3-access-key**, **--s3-secret-key** *string*
:   S3 credentials (default: minioadmin/minioadmin)

**--s3-default-bucket** *name*
:   S3 bucket name (default: sandcrawler)

**--s3-use-ssl**
:   Enable SSL for S3 connections

**--timeout** *duration*
:   Subprocess execution timeout (default: 5m0s)

**--debug**
:   Enable debug logging

**--log-file** *path*
:   Log file path (default: stderr)

**-v**, **--version**
:   Show version information

**-h**, **--help**
:   Show help message

## run Command Options

**-w**, **--workers** *int*
:   Number of parallel workers (default: 4, use 1 for sequential)

**-k**, **--keep**
:   Keep files in spool after processing

## serve Command Options

**--addr** *address*
:   Server listen address (default: 0.0.0.0:8000)

**--server-timeout** *duration*
:   Server read/write timeout (default: 15s)

**--access-log** *path*
:   Access log file path

**--urlmap-file** *path*
:   URL mapping database file (SQLite, optional)

**--urlmap-header** *name*
:   HTTP header for URL mapping (default: X-Original-URL)

ENVIRONMENT
===========

Configuration values can be set via environment variables with **BLOBPROC_** prefix. For
example, **BLOBPROC_GROBID_HOST** sets the GROBID host URL.

FILES
=====

*~/.local/share/blobproc/spool*
:   Default spool directory for PDF files

*./blobproc.yaml*, *~/.config/blobproc/blobproc.yaml*, */etc/blobproc/blobproc.yaml*
:   Configuration file search paths

EXAMPLES
========

Process PDFs with 8 parallel workers:

    $ blobproc run -w 8

Start server on custom port:

    $ blobproc serve --addr :9000

Test single PDF file:

    $ blobproc single document.pdf | jq .

Upload PDF to server:

    $ curl -X POST --data-binary @paper.pdf http://localhost:8000/spool

BUGS
====

Report issues at the project repo (tba).

SEE ALSO
========

**pdftotext**(1), **pdftoppm**(1)

