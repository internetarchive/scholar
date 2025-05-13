# quick and dirty scholar reporting

## sandcrawler stats 

we get stats about our SPN crawling via postgrest; we use it to call functions that run basic queries.

The needed functions:

```sql
create function stat_got_pdf() RETURNS integer AS $$ SELECT count(*) FROM ingest_file_result WHERE ingest_type = 'pdf' AND hit = true AND updated > NOW() - INTERVAL '24 hours' $$ language sql immutable;

create function stat_failed_pdf() RETURNS integer AS $$ SELECT count(*) FROM ingest_file_result WHERE ingest_type = 'pdf' AND hit = false AND updated > NOW() - INTERVAL '24 hours' $$ language sql immutable;

create function stat_error_counts() returns table (status text, count int) as $$ select status, count(*) AS count from ingest_file_result where ingest_type = 'pdf' AND hit = false and updated > NOW() - INTERVAL '24 hours' group by status order by count desc; $$ language sql;
```

which are then accessible from within the cluster like so:

```bash
curl wbgrp-svc506.us.archive.org:3030/rpc/stat_got_pdf
curl wbgrp-svc506.us.archive.org:3030/rpc/stat_failed_pdf
curl wbgrp-svc506.us.archive.org:3030/rpc/stat_error_counts
```

## fatcat changelog

we want to know how many entities have been added to fatcat in the past 24 hours sorted by entity type. for that, we'll use:

`https://scholar.archive.org/_fc/v0/changelog?limit=1000`

if we exhaust these and haven't seen 24 hours of updates, we'll use something like:

`https://scholar.archive.org/_fc/v0/changelog/7611632`

to get an update at a time until we hit the 24 hour mark.

I'm using `_fc` to save some time and also to collect stats on internal usage vs external.

## fulltext holdings

```
curl -X GET "https://scholar.archive.org/_es/scholar_fulltext/_search" \
                         -H "Content-Type: application/json" \
                         -d '{
                       "size": 0,
                       "aggs": {
                         "distinct_values": {
                           "terms": {
                             "field": "fulltext.access_type",
                             "size": 1000
                           }
                         }
                       }
                     }'
```

yielded:

```json
{
  "took": 2060,
  "timed_out": false,
  "_shards": {
    "total": 12,
    "successful": 12,
    "skipped": 0,
    "failed": 0
  },
  "hits": {
    "total": {
      "value": 10000,
      "relation": "gte"
    },
    "max_score": null,
    "hits": []
  },
  "aggregations": {
    "distinct_values": {
      "doc_count_error_upper_bound": 0,
      "sum_other_doc_count": 0,
      "buckets": [
        {
          "key": "ia_sim",
          "doc_count": 41771332
        },
        {
          "key": "wayback",
          "doc_count": 40663110
        },
        {
          "key": "ia_file",
          "doc_count": 2163072
        },
        {
          "key": "web",
          "doc_count": 294
        },
        {
          "key": "repository",
          "doc_count": 9
        }
      ]
    }
  }
}
```

```
nsmith@nsmith-dev ~> curl -X GET "https://scholar.archive.org/_es/scholar_fulltext/_count" \
                         -H "Content-Type: application/json" \
                         -d '{
                       "query": {
                        "term": { "access.mimetype":"application/pdf"}
                       }
                     }'
{"count":41421674,"_shards":{"total":12,"successful":12,"skipped":0,"failed":0}}⏎   
```

## fatcat totals

`https://scholar.archive.org/fatcat/stats.json`

i'm going to start with just this instead of the changelog stuff; we can compute deltas as desired.
