import json
import os
import pathlib
import re
import sys
import time
from subprocess import check_output
from typing import Any

import httpx
import pandas
from httpx_retries import Retry, RetryTransport


ES_URL = "https://scholar.archive.org/_es/"
SC_URL = "http://wbgrp-svc506.us.archive.org:3030/rpc/"
FC_STATS_URL = "https://scholar.archive.org/fatcat/stats.json"
STATS_PATH = pathlib.Path("stats")


def sandcrawler_stats() -> dict[str, Any]:
    timeout = 60.0
    out: dict[str, int] = {}
    r = httpx.get(SC_URL + "stat_pdf_totals", timeout=timeout)
    if r.status_code != 200:
        raise Exception(f"sandcrawler error: {r.text}")
    s = json.loads(r.text)
    out = out | {
            "sandcrawler_pdf_reqs": s[0]["reqs"],
            "sandcrawler_pdf_hits": s[0]["hits"],
            "sandcrawler_pdf_misses": s[0]["misses"],
            }

    r = httpx.get(SC_URL + "stat_pdf_error_totals", timeout=timeout)
    if r.status_code != 200:
        raise Exception(f"sandcrawler error: {r.text}")
    key_re = re.compile("^[a-zA-Z]")
    for error_total in json.loads(r.text):
        if not key_re.match(error_total['status']):
            continue
        key = f"sandcrawler_pdf_error_{error_total['status']}"
        out[key] = error_total["count"]

    return out


def fatcat_json_stats() -> dict[str, Any]:
    timeout = 30.0
    # this is a huge backoff because the fatcat API routinely goes down for up
    # to an hour. the formula used here is backoff_factor * 2**attempts which
    # means the maximum timeout will be 3 * 2^10 or 3072. The previous waits
    # will put the overall wait time over an hour.
    retry = Retry(total=10, backoff_factor=3)

    s = {}

    with httpx.Client(transport=RetryTransport(retry=retry)) as client:
        r = client.get(FC_STATS_URL, timeout=timeout)
        if r.status_code != 200:
            raise Exception(f"fatcat API down after much retrying: {r.text}")

        s = json.loads(r.text)

    return {
            "fatcat_releases": s["release"]["total"],
            "fatcat_refs": s["release"]["refs_total"],
            "fatcat_papers": s["papers"]["total"],
            # i'm actually not sure what the in_web thing is, but from my
            # reading of fatcat code it appears to note releases of type
            # article with files that have http or ftp urls.
            "fatcat_papers_in_web": s["papers"]["in_web"],
            "fatcat_papers_in_kbart": s["papers"]["in_kbart"],
            "fatcat_containers": s["container"]["total"],
            }


def elasticsearch_stats() -> dict[str, Any]:
    timeout = 30.0
    out: dict[str, int] = {}

    # full text PDFs indexed in scholar
    esq: dict[str, Any] = {
            "query": {
                "bool": {
                    "must": [
                        {"term": {"access.access_type": "wayback"}},
                        {"term": {"access.mimetype": "application/pdf"}},
                        ]
                    }
                }
            }

    r = httpx.request("GET", ES_URL + "scholar_fulltext/_count",
                      timeout=timeout, json=esq)
    if r.status_code != 200:
        raise Exception(f"elasticsearch failed: {r.text}")

    out["elasticsearch_scholar_indexed_pdfs"] = json.loads(r.text)["count"]

    # containers reflected by IAS index
    esq = {
            "size": 0,
            "aggs": {
                "unique_count": {
                    "cardinality": {
                         "field": "biblio.container_ident"
                         }
                    }
                }
            }

    r = httpx.request("GET", ES_URL + "scholar_fulltext/_search",
                      timeout=timeout, json=esq)
    if r.status_code != 200:
        raise Exception(f"elasticsearch failed: {r.text}")

    out["elasticsearch_scholar_containers"] = \
        json.loads(r.text)["aggregations"]["unique_count"]["value"]

    # searches in scholar
    r = httpx.get(ES_URL + "scholar_fulltext/_stats/search", timeout=timeout)
    if r.status_code != 200:
        raise Exception(f"elasticsearch failed: {r.text}")

    out["elasticsearch_scholar_searches"] = \
        json.loads(r.text)["_all"]["total"]["search"]["query_total"]

    # searches in fatcat
    ixs = ["fatcat_container", "fatcat_file", "fatcat_release", "fatcat_ref"]
    total_es_fc_queries = 0
    for ix in ixs:
        r = httpx.get(ES_URL + f"{ix}/_stats/search", timeout=timeout)
        if r.status_code != 200:
            raise Exception(f"elasticsearch failed: {r.text}")

        total_es_fc_queries += \
            json.loads(r.text)["_all"]["total"]["search"]["query_total"]

    out["elasticsearch_fatcat_searches"] = total_es_fc_queries

    return out


def scholar_sitemap_stats() -> dict[str, int]:
    output = check_output("cat /srv/scholar/sitemap/* | wc -l", shell=True)
    return {"scholar_sitemap_lines": int(output)}


def gather():
    STATS_PATH.mkdir(exist_ok=True)

    stats: dict[str, int] = {}
    stats = stats | elasticsearch_stats()
    stats = stats | sandcrawler_stats()
    stats = stats | fatcat_json_stats()
    stats = stats | scholar_sitemap_stats()
    # TODO fatcat: works
    # TODO fatcat: works with an archived release
    # TODO periodic crawl counts

    with open(STATS_PATH / f"{time.time()}.json", "w") as f:
        json.dump(stats, f)


def report():
    dfd: dict[str, Any] = {"date": []}
    for fname in os.listdir(STATS_PATH):
        dfd["date"].append(pandas.to_datetime(float(fname[:-5])))
        with open(STATS_PATH / fname, 'r') as f:
            for k, v in json.loads(f):
                if not dfd.get(k):
                    dfd[k] = []
                dfd[k].append(v)
    df = pandas.DataFrame(dfd)
    print(df["date"].max())
    print(df["sandcrawler_pdf_reqs"].max())
    print(df.describe())

    # TODO


if __name__ == "__main__":
    match sys.argv:
        case [_, "gather"]:
            gather()
        case [_, "report"]:
            report()
        case _:
            print("expected either 'gather' or 'report'", file=sys.stderr)
            sys.exit(1)
