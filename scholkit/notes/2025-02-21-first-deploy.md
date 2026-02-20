# prepare for deploy (1)

oaiscrape generation from a raw concatenated XML dump (about 1.3T uncompressed); process about 14M records/min; complete processing takes about 35min

```shell
$ time zstdcat -T0 2025-01-16-metha-oai.xml.zst | \
    sk-oai-records | \
    sk-oai-dctojsonl | \
    pv -l | \
    zstd -c -T0 > 2025-01-16-metha-oai.ndjson.zst

real    35m33.568s
user    1030m4.713s
sys     38m44.740s
```

The resulting file is 179GB compressed; can iterate at 30M docs/minute.
