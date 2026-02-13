#!/usr/bin/env -S uv run --script
# /// script
# dependencies = [
#   "dataframe-image",
#   "duckdb",
#   "matplotlib",
#   "pandas",
# ]
# ///

import itertools
import duckdb
import pandas as pd
import matplotlib.pyplot as plt
import matplotlib
import dataframe_image as dfi

fileset = {
    "crossref": "/var/data/share/crossref/crossref-2024-01-01-doi-sorted-lower.tsv.zst",
    "datacite": "/var/data/share/datacite/datacite-2024-01-26-doi.tsv.zst",
    "dblp": "/var/data/share/dblp/dblp-2024-02-01-doi.tsv.zst",
    "doaj": "/var/data/share/doaj/doaj-oaipmh-2024-01-01-doi-lower.tsv.zst",
    "oaiscrape": "/var/data/share/oaiscrape/2023-11-01-metha-oai-doi.ndjson.zst",
    "openalex": "/var/data/share/openalex-works/openalex-works-2024-02-09-doi-sorted-lower.tsv.zst",
    "pubmed": "/var/data/share/pubmed/pubmed-2024-02-09-doi-lower.tsv.zst",
    # "fatcat": "/var/data/share/fatcat/release_doi.tsv.zst",
}

pd.set_option("display.float_format", lambda x: "%.3f" % x)

dbfile = "/var/data/share/doi.duck"

if __name__ == "__main__":
    con = duckdb.connect(dbfile)
    filesets = fileset.keys()
    counts = {}
    for k in filesets:
        q = """ select count(*) from %s """ % (k,)
        counts[k] = con.execute(q).fetchone()[0]

    df = pd.DataFrame(counts.values(), index=counts.keys(), columns=["num_doi"])
    print(df)
    df = df.sort_values(by="num_doi", ascending=True)
    ax = df.plot.barh(title="DOI count per dataset (02/2024)", grid=True)
    # ax.ticklabel_format(style='plain')
    # ax.get_xaxis().set_major_formatter(
    #         matplotlib.ticker.FuncFormatter(lambda x, p: format(int(x), ',')))
    ax.get_xaxis().set_major_formatter(
        matplotlib.ticker.FuncFormatter(lambda x, p: "{}M".format(int(x / 1_000_000)))
    )
    ax.tick_params(axis="x", labelrotation=45)
    fig = ax.get_figure()
    fig.savefig("out.png")

    # table as png
    dfi.export(df, "df_styled.png")
