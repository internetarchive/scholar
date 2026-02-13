# works clustering

input: joint dataset of fatcat release entities, want: clustered version
(similar to google scholar "N versions"), no need to merge; just group

Currently, the input is about 845950447 records across 8 inputs:

```
$ ls -1 input/
arxiv.ndj.zst
crossref.ndj.zst
datacite.ndj.zst
dblp.ndj.zst
doaj.ndj.zst
oaiscrape.ndj.zst
openalex.ndj.zst
pubmed.ndj.zst
```

## dumb approach

* [ ] group first by DOI, there are overlaps
* [ ] group by other ids

We can use doi.duck database for that.

```
D select doi from crossref intersect select doi from openalex;
┌─────────────────────────────┐
│             doi             │
│           varchar           │
├─────────────────────────────┤
│ 10.1002/9781118164792       │
│ 10.1002/9781118164860.ch1   │
│ 10.1002/9781118164860.ch8   │
│            ·                │
│            ·                │
│            ·                │
│ 10.7748/paed.19.4.7.s14     │
│ 10.7748/paed.2.9.5.s9       │
├─────────────────────────────┤
│  146044108 rows (40 shown)  │
└─────────────────────────────┘
```

We would need one "own" id for each document. We could then try to just curate
a simple DB for the clustering, like `(cluster, id)` e.g. `(1, C123)`, `(1,
O123)`, ...

We start with an empty database.

First, take fatcat.ndj.zst and extract all ids into tables; source would be
nice as well.

```
source | id | doi | pmid | pmcid | arxiv | oai | any
```

Also, create a table per `match_key` algorithm.

```
id | match_key
```

Then find all groups and run a finer comparison, similar to citation graph
clustering. For that we would need a fast way to iterate over the keys. In
refcat, we use the "zipkey" structure for that. Can we generalize this a bit?

----

# clustering releases

Input size, 800M records, growing, should work with 1B or more records as well.
Should work on a single machine, with moderate RAM. Should support multiple
algorithms; precision first, recall second.

Can we use generic similitity indices like faiss or annoy for this task? Would
love to just dump (title, author, year) into a db and then do lookups. Would
work for updates as well, as this could be a some "API" over a file, etc.

## naive approach

* match key: normalize title then group by that
* [ex: DNB](https://wiki.dnb.de/download/attachments/146377939/2019-04-03_KIMWS19_GVI.pdf#page=14)

three match keys

```
1. Material:ISBN:Pubdate
2. Material:Author:Title:Pubdate:Publisher
3. Material:Author:Title:Pubdate
```

* solr: [https://solr.apache.org/guide/8_11/de-duplication.html](https://solr.apache.org/guide/8_11/de-duplication.html)

## numbers

we would need to index 1B docs, we could generate embeddings for the record

## faiss

* memory issues: https://github.com/facebookresearch/faiss/issues/3020


----

## 2024-02-18

Take releases file and create tabular version.

* line number, source, title normalized, doi, other ids ...

----

We could:

* try to find a way to turn parts of our JSON docs into an d-dimensional vector
  and then index that into e.g. annoy
* what kind of embedding should we use?
* similar docs should be have small distance

Could use something like this:

* [https://github.com/typesense/showcase-ecommerce-store/blob/master/scripts/vector-generation/main.py](https://github.com/typesense/showcase-ecommerce-store/blob/master/scripts/vector-generation/main.py)
* [https://huggingface.co/sentence-transformers/all-MiniLM-L6-v2](https://huggingface.co/sentence-transformers/all-MiniLM-L6-v2)

Boils down to SBERT: [https://www.sbert.net](https://www.sbert.net)

* sentence transformer has a limit of 256 tokens
* [https://www.reddit.com/r/LocalLLaMA/comments/176u53g/is_the_075_tokens_per_word_rule_of_thumb_general/](https://www.reddit.com/r/LocalLLaMA/comments/176u53g/is_the_075_tokens_per_word_rule_of_thumb_general/)

Let's see how long the titles of the records are on average. We would need
title, author, embeddings. If we are above 192 words too often, we cannot use
this approach.

In order to see, whether we get some sensible results, we could try to
calculate embeddings for arxiv; and then see, whether we are getting all the
different versions of a paper; most likely they are almost exact matches.

We could distort the data to get some better idea how far an embedding goes.
Could try to evaluate a list of embeddings for this task.

Summing a file with 10M integers, one per line?

```
$ paste -sd+ 10M.len | bc
712219434
```

On average 71 bytes. But that is just the title.

```
In [7]: df.describe()
Out[7]:
               92
count 9999999.000
mean       71.222
std        49.205
min         3.000
25%        39.000
50%        66.000
75%        94.000
max     20612.000
```

However, we would need to skip about 5% of the data:

```
In [15]: df.quantile([0.5, 0.9, 0.95, 0.98, 0.99, 0.999, 0.9999])
Out[15]:
           92
0.500  66.000
0.900 124.000
0.950 147.000
0.980 183.000
0.990 217.000
0.999 387.000
1.000 898.000
```

Try to match by ID first.

Extract table from combined data, `zstdcat fatcat.ndj.zst | clowder -T >
fatcat.tsv` - should list all ids and a normalized title.

Load into duckdb.

```
create table record (
    id VARCHAR,
    source VARCHAR,
    ark VARCHAR,
    arxiv VARCHAR,
    core VARCHAR,
    dblp VARCHAR,
    doi VARCHAR,
    ri VARCHAR,
    wi VARCHAR,
    hdl VARCHAR,
    isbn13 VARCHAR,
    jstor VARCHAR,
    mag VARCHAR,
    mid VARCHAR,
    oai VARCHAR,
    openalex VARCHAR,
    pii VARCHAR,
    pmcid VARCHAR,
    pmid VARCHAR,
    qid VARCHAR,
    ntitle VARCHAR
)
```

Create sqlite3 db for metadata lookup based on identifiers. Takes 22min to tabularize 800M+ records.

```
$ time zstdcat -T0 fatcat.ndj.zst | pv | clowder -T | zstd -c -T0 > fatcat.tsv.zst

522GiB 0:22:10 [ 402MiB/s] [                                                                                                                               <=>                                                                               ]

real    22m11.234s
user    291m19.859s
sys     15m56.985s

$ ll
-rw-rw-r--  1 tir tir  97G Apr 19 18:12 fatcat.ndj.zst
-rw-rw-r--  1 tir tir  50G Apr 19 18:48 fatcat.tsv.zst
```

Load into duck.

```sql
D create table record (
      id VARCHAR,
      source VARCHAR,
      ark VARCHAR,
      arxiv VARCHAR,
      core VARCHAR,
      dblp VARCHAR,
      doi VARCHAR,
      ri VARCHAR,
      wi VARCHAR,
      hdl VARCHAR,
      isbn13 VARCHAR,
      jstor VARCHAR,
      mag VARCHAR,
      mid VARCHAR,
      oai VARCHAR,
      openalex VARCHAR,
      pii VARCHAR,
      pmcid VARCHAR,
      pmid VARCHAR,
      qid VARCHAR,
      ntitle VARCHAR
  )
  ;
D INSERT INTO record SELECT * FROM read_csv('fatcat.tsv.zst', compression='zstd', sep='\t');
```

Failed on some line:

```
 85% ▕███████████████████████████████████████████████████         ▏ Error: Invalid Input Error: CSV Error on Line: 275747065
Expected Number of Columns: 21 Found: 7
  file=fatcat.tsv.zst
  delimiter =    (Set By User)
  quote = " (Auto-Detected)
  escape = " (Auto-Detected)
  new_line = \n (Auto-Detected)
  header = false (Auto-Detected)
  skip_rows = 0 (Auto-Detected)
  date_format =  (Auto-Detected)
  timestamp_format =  (Auto-Detected)
  null_padding=0
  sample_size=20480
  ignore_errors=0
  all_varchar=0
```

Try with `ignore_errors=true`, just to see, if it fits into memory.

```
D INSERT INTO record SELECT * FROM read_csv('fatcat.tsv.zst', compression='zstd', sep='\t', ignore_errors=true);
```

This take about 15min (99% ....) but then actually is done:

```
D select count(*) from record;
┌──────────────┐
│ count_star() │
│    int64     │
├──────────────┤
│    812130305 │
└──────────────┘

D select source, count(*) from record group by source;
┌───────────┬──────────────┐
│  source   │ count_star() │
│  varchar  │    int64     │
├───────────┼──────────────┤
│ arxiv     │      2439358 │
│ datacite  │     54173141 │
│ doaj      │     29357186 │
│ dblp      │      3465807 │
│ crossref  │    146402248 │
│           │          245 │
│ oaiscrape │    326162587 │
│ openalex  │    250129733 │
└───────────┴──────────────┘

D select count(*) from record where doi is NULL;
┌──────────────┐
│ count_star() │
│    int64     │
├──────────────┤
│    355570276 │
└──────────────┘

D select source, count(*) from record where doi is NULL group by source;
┌───────────┬──────────────┐
│  source   │ count_star() │
│  varchar  │    int64     │
├───────────┼──────────────┤
│ dblp      │       544483 │
│           │          245 │
│ arxiv     │      1269542 │
│ doaj      │      6367603 │
│ oaiscrape │    253614915 │
│ openalex  │     93773488 │
└───────────┴──────────────┘

D select count(*) from record where ntitle == '';
100% ▕████████████████████████████████████████████████████████████▏
┌──────────────┐
│ count_star() │
│    int64     │
├──────────────┤
│            0 │
└──────────────┘

D select count(*) from record where ntitle != '';
100% ▕████████████████████████████████████████████████████████████▏
┌──────────────┐
│ count_star() │
│    int64     │
├──────────────┤
│    812130305 │
└──────────────┘
D select count(distinct ntitle) from record;
100% ▕████████████████████████████████████████████████████████████▏
┌────────────────────────┐
│ count(DISTINCT ntitle) │
│         int64          │
├────────────────────────┤
│              309071441 │
└────────────────────────┘

D select count(distinct doi) from record;
100% ▕████████████████████████████████████████████████████████████▏
┌─────────────────────┐
│ count(DISTINCT doi) │
│        int64        │
├─────────────────────┤
│           218230487 │
└─────────────────────┘

D select * from record where doi = '10.1112/S002557931600005X';
┌──────────────────────┬───────────┬─────────┬───────────┬─────────┬─────────┬──────────────────────┬─────────┬───┬─────────┬─────────┬──────────────────────┬──────────┬─────────┬─────────┬─────────┬─────────┬──────────────────────┐
│          id          │  source   │   ark   │   arxiv   │  core   │  dblp   │         doi          │   ri    │ … │   mag   │   mid   │         oai          │ openalex │   pii   │  pmcid  │  pmid   │   qid   │        ntitle        │
│       varchar        │  varchar  │ varchar │  varchar  │ varchar │ varchar │       varchar        │ varchar │   │ varchar │ varchar │       varchar        │ varchar  │ varchar │ varchar │ varchar │ varchar │       varchar        │
├──────────────────────┼───────────┼─────────┼───────────┼─────────┼─────────┼──────────────────────┼─────────┼───┼─────────┼─────────┼──────────────────────┼──────────┼─────────┼─────────┼─────────┼─────────┼──────────────────────┤
│ arxiv-f36f78b39e91…  │ arxiv     │         │ 1401.0035 │         │         │ 10.1112/S002557931…  │         │ … │         │         │ oai:arXiv.org:1401…  │          │         │         │         │         │ theduffinschaeffer…  │
│ oaiscrape-87344624…  │ oaiscrape │         │           │         │         │ 10.1112/S002557931…  │         │ … │         │         │ oai:arXiv.org:1401…  │          │         │         │         │         │ theduffinschaeffer…  │
│ oaiscrape-84d829b9…  │ oaiscrape │         │           │         │         │ 10.1112/S002557931…  │         │ … │         │         │ oai:tib.eu:arxiv:8…  │          │         │         │         │         │ theduffinschaeffer…  │
├──────────────────────┴───────────┴─────────┴───────────┴─────────┴─────────┴──────────────────────┴─────────┴───┴─────────┴─────────┴──────────────────────┴──────────┴─────────┴─────────┴─────────┴─────────┴──────────────────────┤
│ 3 rows                                                                                                                                                                                                         21 columns (17 shown) │
└──────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────┘

D select count(*) from record group by doi having count(doi) > 1;
100% ▕████████████████████████████████████████████████████████████▏
┌────────────────┐
│  count_star()  │
│     int64      │
├────────────────┤
│              5 │
│              3 │
│             10 │
│              7 │
│              5 │
│              4 │
│              6 │
│              8 │
│              3 │
│             12 │
│             13 │
│              4 │
│              3 │
│              3 │
│              3 │
│              3 │
│             11 │
│              3 │
│              3 │
│              8 │
│              · │
│              · │
│              · │
│              2 │
│              2 │
│              2 │
│              2 │
│              2 │
│              2 │
│              2 │
│              2 │
│              2 │
│              2 │
│              2 │
│              2 │
│              2 │
│              2 │
│              2 │
│              2 │
│            100 │
│              2 │
│              3 │
│              2 │
├────────────────┤
│ 155028678 rows │
│   (40 shown)   │
└────────────────┘

D select id, source, doi, ntitle from record where doi in (select * from (select doi from record group by doi having count(doi) > 1) using sample 10);
100% ▕████████████████████████████████████████████████████████████▏
┌────────────────────────────────────────────────────┬───────────┬──────────────────────────────────┬─────────────────────────────────────────────────────────────────────────────────────────────────────────────┐
│                         id                         │  source   │               doi                │                                                   ntitle                                                    │
│                      varchar                       │  varchar  │             varchar              │                                                   varchar                                                   │
├────────────────────────────────────────────────────┼───────────┼──────────────────────────────────┼─────────────────────────────────────────────────────────────────────────────────────────────────────────────┤
│ oaiscrape-31e2a46dc092f9bb2ed7fb79eda5131284a65885 │ oaiscrape │ 10.26575/daj.v13i1.199           │ dentalhealthanddietoftwoprehistoricpopulationsfromchilessemiaridnorth                                       │
│ oaiscrape-31e2a46dc092f9bb2ed7fb79eda5131284a65885 │ oaiscrape │ 10.26575/daj.v13i1.199           │ dentalhealthanddietoftwoprehistoricpopulationsfromchilessemiaridnorth                                       │
│ oaiscrape-31e2a46dc092f9bb2ed7fb79eda5131284a65885 │ oaiscrape │ 10.26575/daj.v13i1.199           │ dentalhealthanddietoftwoprehistoricpopulationsfromchilessemiaridnorth                                       │
│ crossref-0e57f4a51cc01f4700a5850727c27947e305ffef  │ crossref  │ 10.26575/daj.v13i1.199           │ dentalhealthanddietoftwoprehistoricpopulationsfromchilessemiaridnorth                                       │
│ oaiscrape-74d832aa997fe190876d7bc3cfb070578d10ad1f │ oaiscrape │ 10.1594/PANGAEA.196469           │ physicaloceanographyfromseasoarctdditr                                                                      │
│ oaiscrape-74d832aa997fe190876d7bc3cfb070578d10ad1f │ oaiscrape │ 10.1594/PANGAEA.196469           │ physicaloceanographyfromseasoarctdditr                                                                      │
│ openalex-2fe3ce55311dc8d4e2167eea68e441419f8c1d61  │ openalex  │ 10.1016/s0016-0032(32)90481-5    │ theexplodingwedge                                                                                           │
│ crossref-87922e90367f424892e4d609024c856a58dc0c65  │ crossref  │ 10.1001/jama.1973.03220070030007 │ survivalofspermatozoainahumanspermbank                                                                      │
│ openalex-f30ad23fc4c95a5cf98908b3d23802e982ea2365  │ openalex  │ 10.2514/6.2003-3287              │ numericalmodelingrequirementsforcoaxialandchevronnozzleflows                                                │
│ crossref-94ec8e1552edb1c9183f09e47c39e3e5ce46ee19  │ crossref  │ 10.1016/0002-8703(47)90404-3     │ renalfiltrationratesinpregnancytoxemiainulinandexogenouscreatinine                                          │
│ openalex-f3fc235fe1a24718c44a8e6c8ec03e6d58056078  │ openalex  │ 10.32839/2304-5809/2021-11-99-44 │ рoзвитoкемоційногоінтелектуучнівпoчaткoвoїшколивконтекстімистецькоїосвіти                                   │
│ crossref-eed529d818c76671fd5750b48879f2ac7c1c0f81  │ crossref  │ 10.1016/s0016-0032(32)90481-5    │ theexplodingwedge                                                                                           │
│ oaiscrape-e4e27a8e606a9ea39c5481e9f275b3cf7bdd0c5b │ oaiscrape │ 10.32839/2304-5809/2021-11-99-44 │ developmentofemotionalintelligenceofprimaryschoolstudentsinthecontextofarteducation                         │
│ crossref-03affd2efef5abea0bb0007721f7ff5e760c3100  │ crossref  │ 10.2514/6.2003-3287              │ numericalmodelingrequirementsforcoaxialandchevronnozzleflows                                                │
│ crossref-6fdb706b9b6918db0c05afe8f280baadeaccc5d1  │ crossref  │ 10.32839/2304-5809/2021-11-99-44 │ рoзвитoкемоційногоінтелектуучнівпoчaткoвoїшколивконтекстімистецькоїосвіти                                   │
│ openalex-96bd2222e201052e3c427ebaade03454d7709b45  │ openalex  │ 10.1016/0002-8703(47)90404-3     │ renalfiltrationratesinpregnancytoxemiainulinandexogenouscreatinine                                          │
│ oaiscrape-7de7a3b24b8c825bc1d24fd86cc5e090b10d75ef │ oaiscrape │ 10.26575/daj.v13i1.199           │ dentalhealthanddietoftwoprehistoricpopulationsfromchilessemiaridnorth                                       │
│ crossref-ba3e97de54799ec24ed10fb4db604ea26d2830b2  │ crossref  │ 10.5980/jpnjurol1989.87.831      │ roleofserumeselectinelamandinflammatorparametersinpatientswithrenalcellcarcinoma                            │
│ crossref-d017d56929d1cea3d5bd74785763a90d77fbc974  │ crossref  │ 10.1021/am509116s                │ indiumtinoxidefreetransparentconductiveelectrodeforganbasedultravioletlightemittingdiodes                   │
│ openalex-89effe19254196a37042d0ffd2c34d1f39850247  │ openalex  │ 10.1001/jama.1973.03220070030007 │ survivalofspermatozoainahumanspermbank                                                                      │
│ openalex-37de582ef62bc172a6f4e919ab41ecac95afd54e  │ openalex  │ 10.1021/am509116s                │ indiumtinoxidefreetransparentconductiveelectrodeforganbasedultravioletlightemittingdiodes                   │
│ openalex-b1025a66d4fb404e7a000ac04d9aa58b1e759f1d  │ openalex  │ 10.5980/jpnjurol1989.87.831      │ roleofserumeselectinelamandinflammatorparametersinpatientswithrenalcellcarcinoma                            │
│ crossref-4acb19724e7884ae7b206eb906158b35dd67e3e3  │ crossref  │ 10.1016/j.ijrobp.2014.05.2443    │ ctnumberchangesobservedduringctguidedradiationtherapyforheadandneckcanceranewindicatorfortreatmentresponse  │
│ openalex-8e9fcbaba3bc350eb65a677a03d6cab6919d2cdb  │ openalex  │ 10.1016/j.ijrobp.2014.05.2443    │ ctnumberchangesobservedduringctguidedradiationtherapyforheadandneckcanceranewindicatorfortreatmentresponse  │
│ openalex-0263900bc03655b89aaba35e570dabe74b35e95c  │ openalex  │ 10.26575/daj.v13i1.199           │ dentalhealthanddietoftwoprehistoricpopulationsfromchilessemiaridnorth                                       │
├────────────────────────────────────────────────────┴───────────┴──────────────────────────────────┴─────────────────────────────────────────────────────────────────────────────────────────────────────────────┤
│ 25 rows                                                                                                                                                                                               4 columns │
└─────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────┘

D select id, source, doi, ntitle from record where doi in (select * from (select doi from record group by doi having count(doi) > 1) using sample 15) order by doi;
100% ▕████████████████████████████████████████████████████████████▏
┌────────────────────────────────────────────────────┬───────────┬─────────────────────────────────┬──────────────────────────────────────────────────────────────────────────────────────────────────────────────────────┐
│                         id                         │  source   │               doi               │                                                        ntitle                                                        │
│                      varchar                       │  varchar  │             varchar             │                                                       varchar                                                        │
├────────────────────────────────────────────────────┼───────────┼─────────────────────────────────┼──────────────────────────────────────────────────────────────────────────────────────────────────────────────────────┤
│ openalex-0929e456ba2d48bf933bdfd24ab92f8cc801b02d  │ openalex  │ 10.1007/978-1-4939-1197-4_21    │ medicalapproachtoapatientwithpostoperativeweightregain                                                               │
│ crossref-579cca5b746d88785404fa7c765254784035a794  │ crossref  │ 10.1007/978-1-4939-1197-4_21    │ medicalapproachtoapatientwithpostoperativeweightregain                                                               │
│ crossref-99620989379ec3505346a2619f44cc93852e5aee  │ crossref  │ 10.1016/j.jradio.2013.12.012    │ lesmacrobiopsiesmammairessousirm                                                                                     │
│ openalex-ce4cc0dd8262bf8c8c0758e2d8720c7c5ba718ef  │ openalex  │ 10.1016/j.jradio.2013.12.012    │ lesmacrobiopsiesmammairessousirm                                                                                     │
│ openalex-a45cc3e679bc99ca484704a53191922f072c9589  │ openalex  │ 10.1039/c6ra24949c              │ insiturepairofgraphenedefectsandenhancementofitsreinforcementeffectinpolyvinylalcoholhydrogels                       │
│ crossref-c31fce5463a83cd0ee7838d31ba9d2dee602324a  │ crossref  │ 10.1039/c6ra24949c              │ insiturepairofgraphenedefectsandenhancementofitsreinforcementeffectinpolyvinylalcoholhydrogels                       │
│ crossref-71c90d2f1559a9f6a8eda32bd9a68e0c96e1df5f  │ crossref  │ 10.1134/1.1771435               │ erratumtocorrelatedcomplexexponentialwavefunctionsforatomicmolecularsystems                                          │
│ openalex-69af63f7a45c402f3526d7cfbdcd02b376c28987  │ openalex  │ 10.1134/1.1771435               │ erratumtocorrelatedcomplexexponentialwavefunctionsforatomicmolecularsystems                                          │
│ oaiscrape-2ff182e1fda6f8eac743aef5b6c1ca3d03b62632 │ oaiscrape │ 10.1134/1.1771435               │ erratumtocorrelatedcomplexexponentialwavefunctionsforatomicmolecularsystems                                          │
│ openalex-5825455c9a459f9a8db55659a0347a5e0b2a52a9  │ openalex  │ 10.1163/9789004209107_018       │ traditionalgrammaranditslegacyintwentiethcenturylinguistics                                                          │
│ crossref-ce8aee2b991c0e5e5451c8fdc590f77822839f44  │ crossref  │ 10.1163/9789004209107_018       │ traditionalgrammaranditslegacyintwentiethcenturylinguistics                                                          │
│ openalex-aab186a04cb6c007417329c7671c800df88afd6f  │ openalex  │ 10.1176/ajp.138.2.272-a         │ alcoholismandaffectivedisordersclinicalgeneticandbiochemicalstudieswithemphasisonalcohollithiuminteraction           │
│ crossref-33f4fd1ec8656182e56d2321b336ed51f0f455f1  │ crossref  │ 10.1176/ajp.138.2.272-a         │ alcoholismandaffectivedisordersclinicalgeneticandbiochemicalstudieswithemphasisonalcohollithiuminteraction           │
│ openalex-fb978669df8be7c63127cd74039fdfc9ed3217b6  │ openalex  │ 10.2147/cmar.s359545            │ cstfactsasaprognosticmarkercorrelatedwithimmuneinfiltrationinhepatocellularcarcinoma                                 │
│ crossref-0064a170199b0f0a93ec1f3952853e05f14bba8b  │ crossref  │ 10.2147/cmar.s359545            │ cstfactsasaprognosticmarkercorrelatedwithimmuneinfiltrationinhepatocellularcarcinoma                                 │
│ crossref-634aa3b926755acb467ab8616c2e743043c63a8b  │ crossref  │ 10.2218/iclass.2021.6112        │ experimentalinvestigationonspraycharacteristicsofacoaltoliquidblendedfuelunderdifferentinjectionconditions           │
│ openalex-4e4f9550d83c40c2e8a5b84ce0eb2913d5ecf3fd  │ openalex  │ 10.2218/iclass.2021.6112        │ experimentalinvestigationonspraycharacteristicsofacoaltoliquidblendedfuelunderdifferentinjectionconditions           │
│ crossref-f174ee8f7ce8d021116e7182205143fa2c0c1d59  │ crossref  │ 10.24843/jiwsp.2022.v04.i02.p01 │ analisisperubahanorientasipolaperilakumasyarakatdalampemanfaatanaplikasieperpusprovinsibengkulupadamasapandemicovid  │
│ openalex-057e2229d99ae06262a82ff7d780ffeeb8fa3a89  │ openalex  │ 10.24843/jiwsp.2022.v04.i02.p01 │ analisisperubahanorientasipolaperilakumasyarakatdalampemanfaatanaplikasieperpusprovinsibengkulupadamasapandemicovid  │
│ openalex-dcb1d28a0e3368f6946146a8802868a53f9c2470  │ openalex  │ 10.3102/1588159                 │ howwegonnawinwhenweaintrightwithinmindfulnesssocialjusticeandhiphopliteracies                                        │
│ crossref-86af837168e7731faa712a8f032b4fdf55166fef  │ crossref  │ 10.3102/1588159                 │ howwegonnawinwhenweaintrightwithinmindfulnesssocialjusticeandhiphopliteracies                                        │
│ crossref-920ba1256a7b876ffb768e00a5472f91ea4bd246  │ crossref  │ 10.4009/jsdt.37.1286            │                                                                                                                      │
│ openalex-4cf32cd5c5c5654cbf6acf083323c251a649322a  │ openalex  │ 10.4009/jsdt.37.1286            │                                                                                                                      │
│ crossref-cd79b1ee13dcdc9db74cf91193e0738b9d30ac0b  │ crossref  │ 10.4103/ijn.ijn_247_20          │ passengerlymphocytesyndromeafterrenaltransplantcasereport                                                            │
│ openalex-c2e8c154f3f1b133671e4fd2d0a4278914ec07a4  │ openalex  │ 10.4103/ijn.ijn_247_20          │ passengerlymphocytesyndromeafterrenaltransplantcasereport                                                            │
│ openalex-5d32916975b04402627d56372d93894d10135e25  │ openalex  │ 10.48550/arxiv.2401.04609       │ analysisofafamilyoftimecontinuousstronglyconservativespacetimefiniteelementmethodsforthedynamicbiotmodel             │
│ datacite-44ff8da6ada7f4fe39a8683fa05ae340f22ac049  │ datacite  │ 10.48550/arxiv.2401.04609       │ analysisofafamilyoftimecontinuousstronglyconservativespacetimefiniteelementmethodsforthedynamicbiotmodel             │
│ datacite-b728bae392763e7174e2924ce65e1c9b36dfa040  │ datacite  │ 10.5281/zenodo.6448549          │ boshlangichsinflardaoqishsavodxonliginioshirish                                                                      │
│ openalex-a0d0a0e533a171bbbdce2500657852d4607fa3cf  │ openalex  │ 10.5281/zenodo.6448549          │ boshlangichsinflardaoqishsavodxonliginioshirish                                                                      │
│ crossref-f795856495f95094f2d4bcd9a3cd4cfa3b7103a0  │ crossref  │ 10.5539/mer.v7n2p18             │ numericalanalysisofflowbehaviorinvortextubefordifferentgases                                                         │
│ openalex-c5ca5f4a59057e7f8fe9f3e0563a82312fe4e80c  │ openalex  │ 10.5539/mer.v7n2p18             │ numericalanalysisofflowbehaviorinvortextubefordifferentgases                                                         │
├────────────────────────────────────────────────────┴───────────┴─────────────────────────────────┴──────────────────────────────────────────────────────────────────────────────────────────────────────────────────────┤
│ 31 rows                                                                                                                                                                                                       4 columns │
└─────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────┘
```

## Fine-grained comparison

* group by all ids and ntitle first


## Some more questions

Using: fatcat.duck (62GB)

```
D select count(*) from record;
┌──────────────┐
│ count_star() │
│    int64     │
├──────────────┤
│    812130305 │
└──────────────┘

D select count(*) from record where doi != '';
100% ▕████████████████████████████████████████████████████████████▏
┌──────────────┐
│ count_star() │
│    int64     │
├──────────────┤
│    456560029 │
└──────────────┘
```

There are only 309M distinct titles.

```
D select count(distinct ntitle) from record;
100% ▕████████████████████████████████████████████████████████████▏
┌────────────────────────┐
│ count(DISTINCT ntitle) │
│         int64          │
├────────────────────────┤
│              309071441 │
└────────────────────────┘
```

Dumb thing: order by `ntitle` then run over all the lines and verify clusters.

```
$ time zstdcat -T0 fatcat.tsv.zst | pv -l | \
    LC_ALL=C sort -S75% -k21,21 -t' ' | \
    zstd -c -T0 > fatcat-sorted-by-ntitle.tsv.zst

real    35m13.370s
user    33m3.674s
sys     7m27.703s
```

Note-to-self: Reading and writing from an SATA SSD is slower; use `-T` on the nvme raid.

We need a random access metadata store as well or store the metadata as a
column in the TSV. Try the latter first, as it will be faster. The result is a 230GB compressed file.

```
$ time zstdcat -T0 data/fatcat.ndj.zst | ./clowder -T -I -b 200000 | pv -l | zstd -c -T0 > /var/data/tmp/fatcat-full.tsv.zst

real    23m29.478s
user    443m37.330s
sys     39m29.418s

 $ ll -h /var/data/tmp/fatcat-full.tsv.zst
-rw-rw-r-- 1 tir tir 230G Apr 24 17:06 /var/data/tmp/fatcat-full.tsv.zst
```

At 34M/min, that's about 25min for the tabularization; 70% CPU.

How quickly can we iterate over 230GB compressed JSON (about 1TB uncompressed):

```
$ time zstdcat -T0 /var/data/tmp/fatcat-full.tsv.zst | pv | wc -lc
812130550 1049752849574

real    15m33.818s
user    13m10.946s
sys     6m22.835s
```

## Embedding ideas

Another approach would be to index a subset of the data into "annoy" then query for similar docs.

```
$ time zstdcat -T0 fatcat.ndj.zst | \
    parallel --pipe -j 28 --block 100M "jq -rc '{"id": .id, \"title\": .title, \"contribs\": .contribs, \"date\": .release_date}'" | \
    pv -l | \
    zstd -c -T0 > fatcat.min.ndj.zst

real    34m22.669s
user    400m26.495s
sys     40m16.553s
```

Output is `fatcat.min.ndj.zst` 72GB compressed.
