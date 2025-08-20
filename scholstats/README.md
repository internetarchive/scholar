# lightweight scholar reporting

This python application:

- collects cumulative totals for various data points in scholar
- can generate an HTML file with totals and graphs of change over time

Stats are stored in a `.jsonl` file and stat collection is intended to be done once daily.

The stat gathering code needs to run on a `scholar-web` host since it counts the files in the scholar sitemap on disk. Other data sources:

- sandcrawler's postgrest API
- elasticsearch
- fatcat's `stats.json` route

## prereqs

The following functions must exist in sandcrawler's postgresql:

```sql
CREATE FUNCTION stat_pdf_totals() RETURNS table (reqs int, hits int, misses int) as $$
  SELECT 
    count(*) as reqs,
    count(*) FILTER (WHERE hit = true) as hits,
    count(*) FILTER (WHERE hit = false) as misses
  FROM ingest_file_result
  WHERE ingest_type = 'pdf';
$$ language sql;

CREATE FUNCTION stat_pdf_error_totals() RETURNS table (status text, count int) as $$
  SELECT status, count(*) AS count
  FROM ingest_file_result
  WHERE ingest_type = 'pdf' AND hit = false
  GROUP BY status ORDER BY count DESC;
$$ language sql;
```

These are callable as so:

```bash
curl wbgrp-svc506.us.archive.org:3030/rpc/stat_pdf_totals
curl wbgrp-svc506.us.archive.org:3030/rpc/stat_pdf_error_totals
```

Otherwise, this needs to be run from a `scholar-web` node in order for the sitemap query to work. The other calls just require cluster access.

## running

- `uv run main.py gather` appends to `./stats.jsonl`
- `uv run main.py report` write an HTML report to STDOUT.
- `uv run main.py report nsmith@archive.org scholar@archive.org` writes HTML report but with an email header making the output suitable for `sendmail`

## author

nate <nsmith@archive.org>
