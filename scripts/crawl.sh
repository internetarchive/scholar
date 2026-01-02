#!/bin/bash

# look so I avoided this kind of script for almost two years because, well,
# first of all, nothing about these services was documented and i had to arrive
# at this list through reverse engineering and research; second of all i want
# to sunset literally all of these workers. but winter has been hard on the
# archive and we've had so many outages recently I finally got sick of doing
# all this manually.

set -e

ssctl="sudo systemctl"

run() {
  host=$1
  worker=$2
  printf "%s %s\n" $host $worker
  ssh -n wbgrp-svc$host.us.archive.org $ssctl start $worker
  ssh -n wbgrp-svc$host.us.archive.org $ssctl status $worker | grep Active
}

run 263 fatcat-harvest-arxiv-worker
run 263 fatcat-harvest-crossref-worker
run 263 fatcat-harvest-datacite-worker
run 263 fatcat-harvest-pubmed-worker

run 314 sandcrawler-persist-thumbnail-worker@1
run 314 sandcrawler-persist-thumbnail-worker@2
run 314 sandcrawler-persist-html-teixml-worker@1

run 314 sandcrawler-persist-pdftext-worker@1
run 314 sandcrawler-persist-pdftext-worker@2
run 314 sandcrawler-persist-xml-doc-worker@1

run 500 scholar-worker-fetch-docs@1
run 500 scholar-worker-fetch-docs@2
run 500 scholar-worker-index-docs@1
run 500 scholar-worker-index-docs@2

run 503 fatcat-elasticsearch-changelog-worker
run 503 fatcat-elasticsearch-container-worker@1
run 503 fatcat-elasticsearch-file-worker@1
run 503 fatcat-elasticsearch-release-worker@1
run 503 fatcat-elasticsearch-release-worker@2

for i in {1..20}; do
  run 506 sandcrawler-ingest-file-worker@$i
done
run 506 sandcrawler-grobid-worker@1
run 506 sandcrawler-grobid-worker@2
run 506 sandcrawler-persist-crossref-worker@1
run 506 sandcrawler-persist-crossref-worker@2
run 506 sandcrawler-persist-grobid-worker@1
run 506 sandcrawler-persist-grobid-worker@2
run 506 sandcrawler-persist-ingest-file-worker@1
run 506 sandcrawler-persist-ingest-file-worker@2
run 506 sandcrawler-persist-pdftext-worker@1
run 506 sandcrawler-persist-pdftext-worker@2

run 519 fatcat-changelog-worker
run 519 fatcat-elasticsearch-changelog-worker
run 519 fatcat-entity-updates-worker
run 519 fatcat-import-arxiv-worker
run 519 fatcat-import-crossref-worker
run 519 fatcat-import-datacite-worker
run 519 fatcat-import-pubmed-worker
run 519 fatcat-import-ingest-file-worker
run 519 fatcat-import-ingest-web-worker
