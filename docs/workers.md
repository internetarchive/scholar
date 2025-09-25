# Worker Reference

## Random Notes

- see `services.txt` for the output of `systemctl list-unit-files` across all of our VMs. I used that to determine on which machines various workers are running.
- KafkaPusher class is for reading from kafka -> pushing to somewhere like postgres. it's not a producer to kafka though it sounds like one.
- `bezerk_mode` enables clobbering of records when importing from an upstream API
- kafka consumer groups:
  - they group consumers together on a single topic
  - without a group, multiple consumers on the same topic will read the same
    stuff from the same partitions.
  - within a group partitions are assigned to consumers
  - assignment maxes out at 1:1 partition to consumer (ie if 12 consumers and
    10 partitions of a topic, 2 will be idle)
  - the consumers don't see the same stuff because of partition assignments
  - the way our workers are set up, we have some consumer groups that will only
    ever have one consumer. This makes the groups pointless.

## Timer triggered workers

The weird naming stuff here (daily vs weekly, weekly vs quarterly) is not erroneous and reflects reality. I am unsure why the discrepancies existed in the first place.

### sandcrawler-ingest-retry-daily

runs on svc506

```
[Timer]
OnCalendar=*-*-* 13:00:00
RandomizedDelaySec=3600
Persistent=true
```

invocation: `/bin/bash -c "./reingest_weekly.sh"`

- runs sql script `dump_reingest_weekly.sql`
  - finds ingest file results that ended in API errors
  - dumps to `/srv/sandcrawler/tasks/reingest_weekly_current.rows.json`
- runs python script `ingestrequest_row2json.py`
  - converts from sql output json to ingest request json
  - dumps to `/srv/sandcrawler/tasks/reingest_weekly_current.json`
- cats `reingest_weekly_current.json` into `kafkacat`
  - produces to topic `sandcrawler-prod.ingest-file-requests-daily`

### sandcrawler-ingest-retry-weekly

runs on svc506

```
[Timer]
OnCalendar=Sat 08:00:00
RandomizedDelaySec=3600
Persistent=true
```

invocation: `/bin/bash -c "./reingest_quarterly.sh"`

- runs sql script `dump_reingest_quarterly.sql`
  - finds ingest file results that ended in API errors (NB: the error list is slightly different from (`dump_reingest_weekly.sql`)
  - dumps to `/srv/sandcrawler/tasks/reingest_quarterly_current.rows.json`
- runs python script `ingestrequest_row2json.py`
  - converts from sql output json to ingest request json
  - dumps to `/srv/sandcrawler/tasks/reingest_quarterly_current.json`
- cats `reingest_quarterly_current.json` into `kafkacat`
  - produces to topic `sandcrawler-prod.ingest-file-requests-daily`

## Daemonized workers

### scholar-worker-fetch-docs

runs on svc500

invocation: `/bin/bash -c ".venv/bin/python -m scholar.worker fetch-docs-worker"`

- consumes from `fatcat-prod.work-ident-updates` topic
- hits fatcat api to get releases for a work, files for releases
- uses postgrest to get crossref info (doi) for a release ident
- for the files of the release
  - check sandcrawler db for grobid output via postgrest. if not available, check seaweedfs for pdftotext output
  - use postgrest to get any pdf metadata from sandcrawler
- produce results to topic `scholar-prod.update-docs`

### scholar-worker-index-docs

runs on svc500

invocation: `/bin/bash -c ".venv/bin/python -mscholar.worker index-docs-worker"`

- consumes from topic `scholar-prod.update-docs`
- transform payload into elasticsearch document
- does bulk index on elasticsearch of documents

### sandcrawler-grobid-worker

runs on svc098 in theory?

invocation: `/bin/bash -c "pipenv run ./sandcrawler_worker.py --env {{ sandcrawler_kafka_env }} --kafka-hosts {{ sandcrawler_kafka_hosts }} --grobid-host {{ sandcrawler_grobid_uri }} grobid-extract"`

- consumes from topic `sandcrawler-prod.ungrobided-pg`
- calls grobid api and passes a blob stored by sandcrawler
- produces to topic `sancdrawler-prod.grobid-output-pg`

### sandcrawler-ingest-file-bulk-worker

runs nowhere; currently disabled

when i start it somewhere the worker says there is nothing to consume though a massive lag is shown in grafana.

invocation: `/bin/bash -c "pipenv run ./sandcrawler_worker.py --env {{ sandcrawler_kafka_env }} --kafka-hosts {{ sandcrawler_kafka_hosts }} --grobid-host {{ sandcrawler_grobid_uri }} ingest-file --bulk"`

consumer group: `sandcrawler-prod-ingest-file-bulk`

sets `spn_cdx_retry_sec` to 9

however, this worker has `try_spn2` set to false which means it will _not_ attempt to fetch resources via the SPN API at all. it will only look in wayback for an existing file using a wayback/cdx client.

- consumes ingest requests from topic `sandcrawler-prod.ingest-file-requests-bulk`
- looks for an existing ingest result in the sandcrawler database using postgrest, early exiting (with a NotImplemented exception) if so
- starting from initial url, crawls:
  - submits url to SPNv2 API
  - polls on spn status
  - find links, add to frontier
- if a file is found:
  - submit to grobid api and get fulltext
  - extracts pdf thumbnail
- produces to topic `sandcrawler-prod.ingest-file-results`
- produces to topic `sandcrawler-prod.grobid-output-pg` if pdf found and grobid successful
- produces to topic `sandcrawler-prod.pdf-text` if pdf found and poppler successful (local pdf parsing)
- produces to topic `sandcrawler-prod.pdf-thumbnail-180px-jpg`  if pdf found and poppler succesful (local pdf parsing)
- produces to topic `sandcrawler-prod.xml-doc` if xml found
- produces to topic `sandcrawler-prod.html-teixml` if html found

### sandcrawler-ingest-file-priority-worker

runs on svc506

invocation: `14:/bin/bash -c "pipenv run ./sandcrawler_worker.py --env {{ sandcrawler_kafka_env }} --kafka-hosts {{ sandcrawler_kafka_hosts }} --grobid-host {{ sandcrawler_grobid_uri }} ingest-file --priority"`

consumer group: `sandcrawler-prod-ingest-file-priority`

sets `spn_cdx_retry_sec` to 45

- consumes ingest requests from `sandcrawler-prod.ingest-file-requests-priority`
- looks for an existing ingest result in the sandcrawler database using postgrest, early exiting (with a NotImplemented exception) if so
- starting from initial url, crawls:
  - submits url to SPNv2 API
  - polls on spn status
  - find links, add to frontier
- if a file is found:
  - submit to grobid api and get fulltext
  - extracts pdf thumbnail
- produces to topic `sandcrawler-prod.ingest-file-results`
- produces to topic `sandcrawler-prod.grobid-output-pg` if pdf found and grobid successful
- produces to topic `sandcrawler-prod.pdf-text` if pdf found and poppler successful (local pdf parsing)
- produces to topic `sandcrawler-prod.pdf-thumbnail-180px-jpg`  if pdf found and poppler succesful (local pdf parsing)
- produces to topic `sandcrawler-prod.xml-doc` if xml found
- produces to topic `sandcrawler-prod.html-teixml` if html found

### sandcrawler-ingest-file-worker

runs on svc506

invocation: `/bin/bash -c "pipenv run ./sandcrawler_worker.py --env {{ sandcrawler_kafka_env }} --kafka-hosts {{ sandcrawler_kafka_hosts }} --grobid-host {{ sandcrawler_grobid_uri }} ingest-file"`

uses consumer group: `sandcrawler-prod-ingest-file`

sets `spn_cdx_retry_sec` to 1

- consumes ingest requests from `sandcrawler-prod.ingest-file-requests-daily`
- looks for an existing ingest result in the sandcrawler database using postgrest, early exiting (with a NotImplemented exception) if so
- starting from initial url, crawls:
  - submits url to SPNv2 API
  - polls on spn status
  - find links, add to frontier
- if a file is found:
  - submit to grobid api and get fulltext
  - extracts pdf thumbnail
- produces to topic `sandcrawler-prod.ingest-file-results`
- produces to topic `sandcrawler-prod.grobid-output-pg` if pdf found and grobid successful
- produces to topic `sandcrawler-prod.pdf-text` if pdf found and poppler successful (local pdf parsing)
- produces to topic `sandcrawler-prod.pdf-thumbnail-180px-jpg`  if pdf found and poppler succesful (local pdf parsing)
- produces to topic `sandcrawler-prod.xml-doc` if xml found
- produces to topic `sandcrawler-prod.html-teixml` if html found

### sandcrawler-pdftext-worker

runs on nowhere; is disabled

invocation: `/bin/bash -c "pipenv run ./sandcrawler_worker.py --env {{ sandcrawler_kafka_env }} --kafka-hosts {{ sandcrawler_kafka_hosts }} pdf-extract"`

- consumes from topic `sandcrawler-prod.unextracted`
- fetches blob by sha1hex from wayback machine client
- produces to topic `sandcrawler-prod.pdf-text` if pdf found and poppler successful (local pdf parsing)
- produces to topic `sandcrawler-prod.pdf-thumbnail-180px-jpg`  if pdf found and poppler succesful (local pdf parsing)


### sandcrawler-persist-crossref-worker

runs on svc506

invocation: `/bin/bash -c "pipenv run ./sandcrawler_worker.py --env {{ sandcrawler_kafka_env }} --kafka-hosts {{ sandcrawler_kafka_hosts }} --grobid-host {{ sandcrawler_grobid_uri }} persist-crossref --parse-refs"`

- consumes from topic `fatcat-prod.api-crossref`
- parses references using grobid api
- inserts into sandcrawler db crossref table via pyscopg2

### sandcrawler-persist-grobid-worker

runs on svc171, svc314, svc506

```
svc506 sandcrawler_persist_mode="--db-only" 
svc171 sandcrawler_persist_mode="--s3-only" sandcrawler_kafka_group_suffix="-replica171"
svc314 sandcrawler_persist_mode="--s3-only"
```

invocation: `/bin/bash -c "pipenv run ./sandcrawler_worker.py --env {{ sandcrawler_kafka_env }} --kafka-hosts {{ sandcrawler_kafka_hosts }} --s3-bucket {{ sandcrawler_grobid_bucket }} --s3-url {{ sandcrawler_blob_url }} --kafka-group-suffix={{ sandcrawler_kafka_group_suffix }} persist-grobid {{ sandcrawler_persist_mode }}"`

consumer groups `persist-grobid`, `persist-grobid-s3` or with custom suffix like `persist-grobid-replica171`

- consumes from topic `sandcrawler-prod.grobid-output-pg`
- if s3_only, stores xml in seaweedfs
- if db_only, 
 - uses grobid api to get some additional metadata(?)
 - stores in sandcrawler db grobid table

Since svc171 identifies itself as being in a different consumer group than svc314's copy of this worker both workers will read the same messages and both will write them to `localhost:8333` and we're just wasting cycles

### sandcrawler-persist-html-teixml-worker

runs on svc171, svc314

```
svc171 sandcrawler_kafka_group_suffix="-replica171"
```

invocation: `/bin/bash -c "pipenv run ./sandcrawler_worker.py --env {{ sandcrawler_kafka_env }} --kafka-hosts {{ sandcrawler_kafka_hosts }} --s3-bucket {{ sandcrawler_text_bucket }} --s3-url {{ sandcrawler_blob_url }} --kafka-group-suffix={{ sandcrawler_kafka_group_suffix }} persist-html-teixml"`

consumer group `persist-html-teixml` unless on 171 then it's `persist-html-teixml-replica171`

- consumes from topic `sandcrawler-prod.html-teixml`
- saves xml to seaweedfs

Since svc171 identifies itself as being in a different consumer group than svc314's copy of this worker both workers will read the same messages and both will write them to `localhost:8333` and we're just wasting cycles


### sandcrawler-persist-ingest-file-worker

runs on svc506

invocation: `/bin/bash -c "pipenv run ./sandcrawler_worker.py --env {{ sandcrawler_kafka_env }} --kafka-hosts {{ sandcrawler_kafka_hosts }} persist-ingest-file"
`
consumer group `persist-ingest`

- consumes from topic `sandcrawler-prod.ingest-file-results`
- as appropriate:
  - inserts into sandcrawler db table ingest_file_request
  - inserts into sandcrawler db table ingest_file_result
  - inserts into sandcrawler db table cdx
  - inserts into sandcrawler db table file_meta
  - inserts into sandcrawler db table html_meta
  - inserts into sandcrawler db table ingest_fileset_platform

### sandcrawler-persist-pdftext-worker

runs on svc506, svc314, svc171

invocation: `ExecStart=/bin/bash -c "pipenv run ./sandcrawler_worker.py --env {{ sandcrawler_kafka_env }} --kafka-hosts {{ sandcrawler_kafka_hosts }} --s3-bucket {{ sandcrawler_text_bucket }} --s3-url {{ sandcrawler_blob_url }} --kafka-group-suffix={{ sandcrawler_kafka_group_suffix }} persist-pdftext {{ sandcrawler_persist_mode }}"`

in group `persist-pdf-text`

- consumes from topic `sandcrawler-prod.pdf-text`
- if destined for db, inserts into sandcrawler db table pdf_meta
- if destined for db, inserts into sandcrawler db table pdf_meta
- if destined for s3, puts pdftext result into s3

Since svc171 identifies itself as being in a different consumer group than svc314's copy of this worker both workers will read the same messages and both will write them to `localhost:8333` and we're just wasting cycles

### sandcrawler-persist-pdftrio-worker

runs on svc506

invocation: `/bin/bash -c "pipenv run ./sandcrawler_worker.py --env {{ sandcrawler_kafka_env }} --kafka-hosts {{ sandcrawler_kafka_hosts }} persist-pdftrio"`

in group `persist-pdftrio`

- consumes from topic `sandcrawler-prod.pdftrio-output`
- inserts into sandcrawler db table pdftrio
- inserts into sandcrawler db table file_meta

### sandcrawler-persist-thumbnail-worker

runs on svc171, svc314

invocation: `/bin/bash -c "pipenv run ./sandcrawler_worker.py --env {{ sandcrawler_kafka_env }} --kafka-hosts {{ sandcrawler_kafka_hosts }} --s3-bucket {{ sandcrawler_thumbnail_bucket }} --s3-url {{ sandcrawler_blob_url }} --kafka-group-suffix={{ sandcrawler_kafka_group_suffix }} persist-thumbnail"`

in group `persist-pdf-thumbnail`, though 171 uses a unique suffix

- consumes from topic `sandcrawler-prod.pdf-thumbnail-180px-jpg`
- it's reading raw image bytes instead of json
- writes the bytes to seaweedfs

Since svc171 identifies itself as being in a different consumer group than svc314's copy of this worker both workers will read the same messages and both will write them to `localhost:8333` and we're just wasting cycles

### sandcrawler-persist-xml-doc-worker

runs on svc171, svc314

invocation: `/bin/bash -c "pipenv run ./sandcrawler_worker.py --env {{ sandcrawler_kafka_env }} --kafka-hosts {{ sandcrawler_kafka_hosts }} --s3-bucket {{ sandcrawler_text_bucket }} --s3-url {{ sandcrawler_blob_url }} --kafka-group-suffix={{ sandcrawler_kafka_group_suffix }} persist-xml-doc"`

in topic `persist-xml-doc`, though 171 uses a unique suffix

- consumes from topic `sandcrawler-prod.xml-doc`
- writes xml to seaweedfs
 
Since svc171 identifies itself as being in a different consumer group than svc314's copy of this worker both workers will read the same messages and both will write them to `localhost:8333` and we're just wasting cycles

### fatcat-changelog-worker

runs on svc519

invocation: `/bin/bash -c "pipenv run ./fatcat_worker.py --env {{ fatcat_kafka_env }} --kafka-hosts {{ fatcat_kafka_hosts }} --api-host-url https://api.{{ fatcat_domain }}/v0 changelog"`

- forever loop to poll the fatcat changelog api
- produces to topic `fatcat-prod.changelog`, one message per changelog entry

### fatcat-elasticsearch-changelog-worker

runs on svc503

invocation: `/bin/bash -c "pipenv run ./fatcat_worker.py --env {{ fatcat_kafka_env }} --kafka-hosts {{ fatcat_kafka_hosts }} --api-host-url https://api.{{ fatcat_domain }}/v0 elasticsearch-changelog --elasticsearch-backend {{ fatcat_elasticsearch_backend }} --elasticsearch-index {{ fatcat_elasticsearch_changelog_index }}{{ fatcat_elasticsearch_changelog_index_suffix }}"`

in group `elasticsearch-updates3`

- consumes from topic `fatcat-prod.changelog`
- transforms changelog payload using `changelog_to_elasticsearch`
- indexes changelog entry into `fatcat_changelog`

### fatcat-elasticsearch-container-worker

runs on svc503

invocation: `/bin/bash -c "pipenv run ./fatcat_worker.py --env {{ fatcat_kafka_env }} --kafka-hosts {{ fatcat_kafka_hosts }} --api-host-url https://api.{{ fatcat_domain }}/v0 elasticsearch-container --elasticsearch-backend {{ fatcat_elasticsearch_backend }} --elasticsearch-index {{ fatcat_elasticsearch_container_index }}{{ fatcat_elasticsearch_container_index_suffix }}"`

`fatcat_elasticsearch_container_index`: `fatcat_container`

`fatcat_elasticsearch_container_index_suffix`: `_v05_20220110`

in group `elasticsearch-updates3`

- consumes from topic `fatcat-prod.container-updates`
- transforms container entity payload using `container_to_elasticsearch`
- indexes container into `fatcat_container_v05_20220110`

### fatcat-elasticsearch-file-worker

runs on svc503

invocation: `/bin/bash -c "pipenv run ./fatcat_worker.py --env {{ fatcat_kafka_env }} --kafka-hosts {{ fatcat_kafka_hosts }} --api-host-url https://api.{{ fatcat_domain }}/v0 elasticsearch-file --elasticsearch-backend {{ fatcat_elasticsearch_backend }} --elasticsearch-index {{ fatcat_elasticsearch_file_index }}{{ fatcat_elasticsearch_file_index_suffix }}"`

`fatcat_elasticsearch_file_index`: `fatcat_file`

`fatcat_elasticsearch_file_index_suffix`: `_v03c`

in group `elasticsearch-updates3`

- consumes from topic `fatcat-prod.file-updates`
- transforms file entity payload using `file_to_elasticsearch`
- indexes file into `fatcat_file_v03c`

### fatcat-elasticsearch-release-worker

runs on svc503

invocation: `/bin/bash -c "pipenv run ./fatcat_worker.py --env {{ fatcat_kafka_env }} --kafka-hosts {{ fatcat_kafka_hosts }} --api-host-url https://api.{{ fatcat_domain }}/v0 elasticsearch-release --elasticsearch-backend {{ fatcat_elasticsearch_backend }} --elasticsearch-index {{ fatcat_elasticsearch_release_index }}{{ fatcat_elasticsearch_release_index_suffix }}"`

in group `elasticsearch-updates3`

- consumes from topic `fatcat-prod.release-updates-v03`
- transforms release entity payload using `release_to_elasticsearch`
- indexes file into `fatcat_release_v05_20220110`

### fatcat-entity-updates-worker

runs on svc519

invocation: `/bin/bash -c "pipenv run ./fatcat_worker.py --env {{ fatcat_kafka_env }} --kafka-hosts {{ fatcat_kafka_hosts }} --api-host-url https://api.{{ fatcat_domain }}/v0 entity-updates"`

in group `entity-updates`

- consumes from topic `fatcat-prod.changelog`
- looks at an editgroup to determine what entities were edited
- depending on type of entities edited, uses fatcat api to hydrate ident and:
  - produces to topic `fatcat-prod.release-updates-v03`
  - produces to topic `fatcat-prod.file-updates`
  - produces to topic `fatcat-prod.container-updates`
- for any release that has been added (no previous revision for release)
  - produces to topic `sandcrawler-prod.ingest-file-requests-daily`
  - this code does some filtering so as not to try and ingest everything
- for all affected works
  - produces to topic `fatcat-prod.work-ident-updates`


### fatcat-harvest-arxiv-worker

runs on svc263

invocation: `/bin/bash -c "pipenv run ./fatcat_harvest.py --env {{ fatcat_kafka_env }} --kafka-hosts {{ fatcat_kafka_hosts }} --contact-email {{ fatcat_harvest_email }} --continuous arxiv"`

start date defaults to two weeks ago
end date defaults to tomorrow

- queries `https://export.arxiv.org/oai2`
- produces XML to topic `fatcat-prod.oaipmh-arxiv`, one document per hit
- produces to topic `fatcat-prod.oaipmh-arxiv-state` indication that a given date range has been processed via the API
- consumes from `fatcat-prod.oaipmh-arxiv-state` to update internal state with previously seen days (does not update consumer offsets, always wants to reread whole topic)

### fatcat-harvest-crossref-worker

runs on svc263

invocation: `/bin/bash -c "pipenv run ./fatcat_harvest.py --env {{ fatcat_kafka_env }} --kafka-hosts {{ fatcat_kafka_hosts }} --contact-email {{ fatcat_harvest_email }} --continuous crossref"`

- queries `https://api.crossref.org/works`
- produces to topic `fatcat-prod.api-crossref` for each item found in api
- produces to topic `fatcat-prod.api-crossref-state` indication that a given date range has been processed via the API
- consumes from `fatcat-prod.api-crossref-state` to update internal state with previously seen days (does not update consumer offsets, always wants to reread whole topic)

### fatcat-harvest-datacite-worker

runs on svc263

invocation: `/bin/bash -c "pipenv run ./fatcat_harvest.py --env {{ fatcat_kafka_env }} --kafka-hosts {{ fatcat_kafka_hosts }} --contact-email {{ fatcat_harvest_email }} --continuous datacite"`

- queries `https://api.datacite.org/dois`
- produces to topic `fatcat-prod.api-datacite` for each item found in api
- produces to topic `fatcat-prod.api-datacite-state` indication that a given date range has been processed via the API
- consumes from `fatcat-prod.api-datacite-state` to update internal state with previously seen days (does not update consumer offsets, always wants to reread whole topic)

### fatcat-harvest-pubmed-worker

runs on svc263

invocation: `/bin/bash -c "pipenv run ./fatcat_harvest.py --env {{ fatcat_kafka_env }} --kafka-hosts {{ fatcat_kafka_hosts }} --contact-email {{ fatcat_harvest_email }} --continuous pubmed"`

- accesses updates via `ftp://ftp.ncbi.nlm.nih.gov`
- stores gzipped xml on disk
- produces XML blobs with a pubmed id to topic `fatcat-prod.ftp-pubmed`
- produces to topic `fatcat-prod.ftp-pubmed-state` indication that a given date range has been processed via the API
- consumes from `fatcat-prod.ftp-pubmed-state` to update internal state with previously seen days (does not update consumer offsets, always wants to reread whole topic)

### fatcat-import-arxiv-worker

runs on svc519

invocation: `/bin/bash -c "pipenv run ./fatcat_import.py --kafka-env {{ fatcat_kafka_env }} --kafka-hosts {{ fatcat_kafka_hosts }} arxiv - --kafka-mode"`

in group `fatcat-prod-import-arxiv`

the logic for this worker is particularly complicated

- consumes from topic `fatcat-prod.oaipmh-arxiv`
- parses raw arxiv XML with Beautiful Soup
- looks up any existing work that matches release
- creates release

### fatcat-import-crossref-worker

runs on svc519

invocation: `/bin/bash -c "pipenv run ./fatcat_import.py --kafka-env {{ fatcat_kafka_env }} --kafka-hosts {{ fatcat_kafka_hosts }} crossref - /srv/fatcat/datasets/ISSN-to-ISSN-L.txt --kafka-mode"`

in group `fatcat-prod-import-arxiv`

- consumes from topic `fatcat-prod.api-crossref`
- parses json from crossref api
- converts to a ReleaseEntity
- if release doesn't already seem to exist, pushes into batch of releases to add to an `EditGroup`

### fatcat-import-datacite-worker

runs on svc519

invocation: `/bin/bash -c "pipenv run ./fatcat_import.py --kafka-env {{ fatcat_kafka_env }} --kafka-hosts {{ fatcat_kafka_hosts }} datacite - /srv/fatcat/datasets/ISSN-to-ISSN-L.txt --kafka-mode"`

in group `fatcat-prod-import-datacite`

- consumes from topic `fatcat-prod.api-datacite`
- converts from datacite JSON into a ReleaseEntity
- creates a ContainerEntity if appopriate
- if release doesn't already seem to exist, pushes  into batch of releases to add to an `EditGroup`

### fatcat-import-ingest-file-worker

runs on svc519

invocation: `/bin/bash -c "pipenv run ./fatcat_import.py --kafka-env {{ fatcat_kafka_env }} --kafka-hosts {{ fatcat_kafka_hosts }} ingest-file-results - --kafka-mode"`

in group `fatcat-prod-ingest-file-result`

- consumes from topic `fatcat-prod.ingest-file-results`
- looking for pdf, xml
- parses an ingest result
- uses fatcat api to create a `FileEntity` if its sha1 isn't already found

### fatcat-import-ingest-web-worker

runs on `svc519`

invocation: `/bin/bash -c "pipenv run ./fatcat_import.py --kafka-env {{ fatcat_kafka_env }} --kafka-hosts {{ fatcat_kafka_hosts }} ingest-web-results - --kafka-mode"`

in group `fatcat-prod-ingest-web-result`

NB: Only 2 out of the last 2.7 million things in this topic were inserted by this worker

- consumes from topic `fatcat-prod.ingest-file-results`
- looking for html results
- parses ingest result
- uses fatcat api to create a `WebcaptureEntity` if its URL isn't already on a webcapture associated with a release

### fatcat-import-pubmed-worker

runs on svc519

invocation: `/bin/bash -c "pipenv run ./fatcat_import.py --kafka-env {{ fatcat_kafka_env }} --kafka-hosts {{ fatcat_kafka_hosts }} pubmed - /srv/fatcat/datasets/ISSN-to-ISSN-L.txt --kafka-mode --do-updates"` 

in group `fatcat-prod-import-pubmed`

- consumes from topic `fatcat-prod.ftp-pubmed`
- converts beautiful soup parsed xml from pubmed ftp into `ReleaseEntity`
- attempts updating existing release with new attributes if matching release already exists
- otherwise, adds release to insert batch 

### fatcat-import-savepapernow-file-worker

Retired. Processed paper submissions via an unsecured public form.

### fatcat-import-savepapernow-web-worker

Retired. Processed paper submissions via an unsecured public form.
