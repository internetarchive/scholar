# oaiscrape Q1 2025

We find about 477,368,047 end or record tags. The uncompressed XML file
contains 1.25TiB of metadata.

Conversion steps:

Tool: `dumprecords.go` to go from invalid concatenaded XML to a valid XML file
(albeit large), the `oaijson.go` to go from XML to JSON lines.

During `oaijson.go` we encountered a problem with conversion, some unexpected
"dump" element. So while we find 477M end of record markers, we only have
128,863,625 lines in the ndj file.

We retry with a byte counter to identify the data issue.

```
$ time zstdcat -T0 2025-01-16-metha-oai-dump-record.xml.zst | pv | ./oaijson | \
    zstd -c -T0 > 2025-01-16-metha-oai-dump-record-update-1.ndjson.zst
```

30G in, 4.5G out and 10M lines out. Estimating to be 400GB read (of 1.25TB)
when the issue appears.

Another option would be to split the output of `dumprecords.go` into groups of
1M records and then work on them individually.

## Debugging

* discrepancy in start and end record tags:

start: 477370502
end:   477368052

```
$ time justrecords < 2025-01-16-metha-oai.xml | pv > xxxx
1.25TiB 0:45:27 [ 481MiB/s] [

real    45m28.411s
user    28m16.767s
sys     43m41.496s
```

## Rough numbers

* 477M json records
* it takes about 20min to iterate over the 1.3T, 477M json lines
* extracted ids, then found unique: 196204847

```
$ time zstdcat -T0 2025-01-16-metha-oai-sep-1e-oai-identifier.tsv.zst | \
    LC_ALL=C sort -S80% -u | wc -l
196204847
```

