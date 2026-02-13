#!/usr/bin/env -S uv run --script
# /// script
# dependencies = [
#   "duckdb",
# ]
# ///

import itertools
import duckdb
import json

# set paths here, all files contain one DOI per line from that dataset; files
# required some sorting, uniq-ing and lowercasing

fileset = {
    "crossref": "/var/data/share/crossref/crossref-2024-01-01-doi-sorted-lower.tsv.zst",
    "datacite": "/var/data/share/datacite/datacite-2024-01-26-doi.tsv.zst",
    "dblp": "/var/data/share/dblp/dblp-2024-02-01-doi.tsv.zst",
    "doaj": "/var/data/share/doaj/doaj-oaipmh-2024-01-01-doi-lower.tsv.zst",
    "oaiscrape": "/var/data/share/oaiscrape/2023-11-01-metha-oai-doi.ndjson.zst",
    "openalex": "/var/data/share/openalex-works/openalex-works-2024-02-09-doi-sorted-lower.tsv.zst",
    "pubmed": "/var/data/share/pubmed/pubmed-2024-02-09-doi-lower.tsv.zst",
    "fatcat": "/var/data/share/fatcat/release_doi.tsv.zst",
}

dbfile = "/var/data/share/doi.duck"

if __name__ == "__main__":
    con = duckdb.connect(dbfile)
    for name, path in fileset.items():
        con.execute(
            """
            create table if not exists %s as
            select * from read_csv('%s',
                sep="\t",
                compression="zstd",
                names=['doi'],
                columns={'doi': 'VARCHAR'})
            """
            % (name, path)
        )

    # for name in fileset.keys():
    #     result = con.execute(""" select count(*) from %s """ % name).fetchone()[
    #         0
    #     ]  # (153842785,)
    #     print("\t".join([name, str(result)]))

    for a, b in itertools.combinations(fileset.keys(), r=2):
        q = (
            """ select count(*) from (select doi from %s intersect select doi from %s) """
            % (a, b)
        )
        intersect_count = con.execute(q).fetchone()[0]
        q = (
            """ select count(*) from (select doi from %s union select doi from %s) """
            % (a, b)
        )
        union_count = con.execute(q).fetchone()[0]

        sim = intersect_count / union_count
        print(json.dumps({"a": a, "b": b, "sim": sim}))
