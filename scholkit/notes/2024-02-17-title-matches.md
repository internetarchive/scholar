# Title matches

Goal: How many titles match between fatcat and openalex? All CrossRef records
have a DOI. The other dataset with much fewer ids would be oaiscrape. So look
at these:

* fatcat-openalex
* fatcat-oaiscrape
* oaiscrape-openalex

How many records?

```
$ time zstdcat -T0 openalex-works-2024-02-09-ids-reduced.ndj.zst | wc -l
248072830

real    0m57.792s
user    1m0.896s
sys     0m21.336s
```

oaiscrape number of URL per record (mostly 1, then none, then more than one):

```
$ time zstdcat -T0 2023-11-01-metha-oai.ndjson.zst | \
    parallel --pipe --block 100M -j 28 "jq -rc '.urls | length'" > ulen.txt

real    10m54.364s
user    77m38.247s
sys     18m50.551s

$ LC_ALL=C sort -S50% ulen.txt | LC_ALL=C uniq -c | sort -nr | head -20
196477094 1
76319859 0
35994820 2
10804393 3
5344579 4
 567886 5
 140139 6
 132324 7
 121539 8
  87719 10
  54010 9
  37569 11
  18816 12
  10771 13
   7212 14
   6519 15
   4659 16
   3943 17
   3033 18
   2795 19
```

For oaiscrape, we need to dissect ids and URL.

## Exact title and matchkey comparisons

* [ ] compute normalized title for each dataset
* [ ] run "comm"

## Preparation

For openalex, we have a "reduced id" version (15G compressed), with openalex id, doi and title.

As a first step, try to extract normalized titles.

```
$ time zstdcat -T0 openalex-works-2024-02-09-ids-reduced.ndj.zst | \
    parallel --pipe --j 20 --block 40M "jq -rc '.title' | \
    strnorm -a nowslo" | \
    LC_ALL=C sort -S50% -u > openalex-works-2024-02-09-norm-title.tsv
```

Example lines:

```
experimentalresearchontheanthelminticpowerofcertainaromaticaldehydes
graphcutbasedbinarisationofnoisycheckimages
экономическоеразвитиемакрорегионадальнеговостокапроблемыиперспективы
thecanadiangrainandoilseedsectorastudyintothecanadianmarketingsystem
중고등학교의스트레스정도와스트레스대처방식
aplikasikamuskebidananberbasisandroid
remarkelementallyonthenewdevelopmentopportuniti
```

How many normalized title in openalex:

```
$ LC_ALL=C wc -l openalex-works-2024-02-09-norm-title.tsv
212268586 openalex-works-2024-02-09-norm-title.tsv
```

oaiscrape TSV with "oai", any "id", any "title" (only keep records which have a title)

```
$ time zstdcat -T0 2023-11-01-metha-oai.ndjson.zst | \
    parallel --pipe -j 24 --block 100M "jq -rc 'select(.titles | length > 0) | [.oai, .ids[0]?, .titles[0]] | @tsv'" | pv -l > 2023-11-01-metha-oai-titles.tsv.zst
```

Normalized title list only.

```
 $ LC_ALL=C wc -l 2023-11-01-metha-oai-norm-titles.tsv
138710468 2023-11-01-metha-oai-norm-titles.tsv
```

fatcat 2023-10-10 version:

```
$ time zstdcat -T0 release_export_expanded_2023-10-10.json.zst | \
    pv -l | \
    parallel --pipe -j 24 --block 40M 'jq -rc .title | strnorm -a nowslo' | \
    LC_ALL=C sort -u -S 70% > release_export_expanded_2023-10-10-norm-titles.tsv
```

Normalized titles count:

* openalex: 212,268,586
* oaiscrape: 138,710,468
* release: 139,678,220

## fatcat-openalex

117M records shared by normalized title; 22M in fatcat only; 95M in openalex only.

```
$ LC_ALL=C comm -12 ../../openalex-works/openalex-works-2024-02-09-norm-title.tsv ../../refcat/2023-10-10/release_export_expanded_2023-10-10-norm-titles.tsv | wc -l
117222072
$ LC_ALL=C comm -13 ../../openalex-works/openalex-works-2024-02-09-norm-title.tsv ../../refcat/2023-10-10/release_export_expanded_2023-10-10-norm-titles.tsv | wc -l
22456148
$ LC_ALL=C comm -23 ../../openalex-works/openalex-works-2024-02-09-norm-title.tsv ../../refcat/2023-10-10/release_export_expanded_2023-10-10-norm-titles.tsv | wc -l
95046514
```

What are the 95M openalex title we do not know about?

## fatcat-oaiscrape

33M shared title between fatcat and oaiscrape. 106M fatcat only, 105M oaiscrape only.

```
$ LC_ALL=C comm -12 ../../oaiscrape/2023-11-01-metha-oai-norm-titles.tsv ../../refcat/2023-10-10/release_export_expanded_2023-10-10-norm-titles.tsv | pv -l | wc -l
33054995
$ LC_ALL=C comm -13 ../../oaiscrape/2023-11-01-metha-oai-norm-titles.tsv ../../refcat/2023-10-10/release_export_expanded_2023-10-10-norm-titles.tsv | pv -l | wc -l
106623225
$ LC_ALL=C comm -23 ../../oaiscrape/2023-11-01-metha-oai-norm-titles.tsv ../../refcat/2023-10-10/release_export_expanded_2023-10-10-norm-titles.tsv | pv -l | wc -l
105655473
```

## oaiscrape-openalex

50M shared title between openalex and oaiscrape. 161M openalex only, 87M oaiscrape only.

```
$ LC_ALL=C comm -12 openalex-works-2024-02-09-norm-title.tsv ../oaiscrape/2023-11-01-metha-oai-norm-titles.tsv | wc -l
50820876

$ LC_ALL=C comm -13 openalex-works-2024-02-09-norm-title.tsv ../oaiscrape/2023-11-01-metha-oai-norm-titles.tsv | wc -l
87889592

$ LC_ALL=C comm -23 openalex-works-2024-02-09-norm-title.tsv ../oaiscrape/2023-11-01-metha-oai-norm-titles.tsv | wc -l
161447710
```

There are 50M normalized title matches between openalex and oaiscrape; 87M
title are oaiscrape only; 161M are openalex only.

