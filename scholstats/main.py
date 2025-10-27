# notes to a future self
#
# the doc for this work talks about averages for different periods of time --
# 7d, 30d, 60d etc. I have not implemented anything like that since as of
# writing we haven't been collecting data for more than two days. This
# currently produces graphs where each line of data gets its own tick on the x
# axis -- in other words, a point per day. I think that's good enough until we
# have >30 days worth of stuff at least.

import base64
import io
import json
import os
import pathlib
import re
import sys
import warnings
from datetime import datetime, UTC, date
from subprocess import check_output
from typing import Any

import httpx
import jinja2
import pandas as pd
import matplotlib.pyplot as plt
import seaborn as sns
from httpx_retries import Retry, RetryTransport

# this useless pandas warning was annoying me
warnings.simplefilter(action='ignore', category=pd.errors.PerformanceWarning)

jenv = jinja2.Environment(loader=jinja2.FileSystemLoader("."))

sns.set_style("darkgrid")
sns.set_palette("Paired")


ES_URL = "https://scholar.archive.org/_es/"
SC_URL = "http://wbgrp-svc506.us.archive.org:3030/rpc/"
FC_STATS_URL = "https://scholar.archive.org/fatcat/stats.json"
DEFAULT_STATS_PATH = pathlib.Path("./stats.jsonl")


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
    #
    # TODO fatcat went down and it only waited 761 seconds
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
    works_output = check_output(
            "cat /srv/scholar/sitemap/sitemap-works* | wc -l", shell=True)
    access_output = check_output(
            "cat /srv/scholar/sitemap/sitemap-works* | wc -l", shell=True)
    return {
            "scholar_sitemap_works_lines": int(works_output),
            "scholar_sitemap_access_lines": int(access_output),
            }


def gather(jsonl_path: pathlib.Path):
    now = datetime.now(UTC)
    ds = now.strftime("%Y-%m-%d %H:%M:%S")

    stats: dict[str, str | int] = {"datetime": ds}
    stats = stats | elasticsearch_stats()
    stats = stats | sandcrawler_stats()
    stats = stats | fatcat_json_stats()
    stats = stats | scholar_sitemap_stats()
    # TODO fatcat: works
    #  - this is not exposed in ES, so need to connect directly to fc db
    # TODO fatcat: works with an archived release
    #  - this is a complex, slow query -- a join across files, releases, works,
    #    idents, everything
    # TODO periodic crawl counts
    #  - this will have to be manual
    # TODO kbart report size
    #  - this will have to be manual
    # TODO percentage of releases by source
    #  - could be computed from changelog but i don't feel like writing tooling
    #    for the legacy version

    with open(jsonl_path, "a") as f:
        json.dump(stats, f)
        print("", file=f)


def make_frame(jsonl_path: pathlib.Path) -> pd.DataFrame:
    df = pd.read_json(jsonl_path, lines=True)
    for col in df.columns:
        if col != 'datetime':
            df[col+'_diff'] = df[col].diff()
            df[col+'_pct'] = df[col].pct_change()
    df.index = pd.DatetimeIndex(df['datetime'])

    return df


def plot_to_b64() -> str:
    """Invokes global plt to save current plot as image and returns it as a
    base64 string"""
    bio = io.BytesIO()
    plt.savefig(bio, format='png')
    bio.seek(0)
    return base64.b64encode(bio.read()).decode()


# TODO this should be part of a context manager
def plot_clear():
    plt.clf()
    plt.cla()


def to_cols(df: pd.DataFrame) -> list[dict]:
    out = []
    for x in range(len(df) - 1, -1, -1):
        ws = df.iloc[x]
        pct_change = 0
        if x == 0:
            pct_change = "?"
        else:
            prev = df.iloc[x-1]
            pct_change = "∞"
            if prev > 0:
                pct_change = int(((ws-prev)/prev) * 100)
            elif prev == 0 and ws == 0:
                pct_change = 0

        out.append({
            "total": "{:,}".format(int(ws)),
            "pct_change": f"{pct_change}%",
            })
        x += 1
    return out


def report(df: pd.DataFrame, tmpl: jinja2.Template) -> str:
    ctx = {
        "generated": datetime.today(),
        }

    # generate an arbitrary stat first in order to figure out how many weeks
    # worth of data we have
    weekly_sums = df.sandcrawler_pdf_reqs_diff.resample("7D").sum()

    ctx["week_labels"] = []

    for x in range(len(weekly_sums)):
        ctx["week_labels"].append("last week" if x == 0 else f"-{x+1} wk")

    ctx["spn_reqs_weeks"] = to_cols(weekly_sums)
    # generate the rest
    ctx["spn_hits_weeks"] = to_cols(df.sandcrawler_pdf_hits_diff.resample("7D").sum())
    ctx["fc_releases_weeks"] = to_cols(df.fatcat_releases_diff.resample("7D").sum())

    # generate an arbirary stat first in order to figure out how many months
    # worh of data we have
    monthly_sums = df["fatcat_containers_diff"].resample("30D").sum()

    ctx["month_labels"] = []
    for x in range(len(monthly_sums)):
        ctx["month_labels"].append("last 30 days" if x == 0 else f"-{(x*30)+30} days")

    ctx["fc_container_months"] = to_cols(monthly_sums)
    ctx["fc_releases_months"] = to_cols(df["fatcat_releases_diff"].resample("30D").sum())
    ctx["fc_papers_months"] = to_cols(df["fatcat_papers_diff"].resample("30D").sum())
    ctx["fc_archived_papers_months"] = to_cols(df["fatcat_papers_in_web_diff"].resample("30D").sum())
    ctx["fc_refs_months"] = to_cols(df["fatcat_refs_diff"].resample("30D").sum())
    # TODO total works
    # TODO % of works with archived release

    # scholar holdings - big table with this month, month-1, month-2
    ctx["ias_containers_months"] = to_cols(
            df["elasticsearch_scholar_containers_diff"].resample("30D").sum())
    ctx["ias_releases_months"] = to_cols(
            df["elasticsearch_scholar_indexed_pdfs_diff"].resample("30D").sum())
    ctx["ias_sitemap_works_months"] = to_cols(
            df["scholar_sitemap_works_lines_diff"].resample("30D").sum())
    ctx["ias_sitemap_access_months"] = to_cols(
            df["scholar_sitemap_access_lines_diff"].resample("30D").sum())

    # search queries
    # TODO these numbers are useless. need to switch to looking at access logs
    #ctx["scholar_searches_weeks"] = to_cols(
    #        df["elasticsearch_scholar_searches_diff"].resample("7D").sum())
    #ctx["fatcat_searches_weeks"] = to_cols(
    #        df["elasticsearch_fatcat_searches_diff"].resample("7D").sum())

    # older stuff below
    default_plot_args = {
            "figsize": (12, 8),
            "rot": 30,
            "grid": True,
            "ylabel": "count",
            }

    # sandcrawler

    plot_args = default_plot_args | {
            "title": "Weekly SPN PDF requests",
            "stacked": True}
    ax = df[["sandcrawler_pdf_misses_diff",
             "sandcrawler_pdf_hits_diff"]].resample("7D").mean().plot.bar(**plot_args)
    ax.legend(framealpha=0.5)
    ctx["sc_graph_b64"] = plot_to_b64()

    plot_clear()

    spn_error_cols = []
    for col in df.columns:
        if col.startswith("sandcrawler_pdf_error") and col.endswith("_diff"):
            if df[col].mean() > 50 and len(spn_error_cols) < 10:
                spn_error_cols.append(col)

    if len(spn_error_cols) > 0:
        plot_args = default_plot_args | {
            "title": "Weekly SPN errors (mean diff >50)"
            }
        ax = df[spn_error_cols].resample("7D").sum().plot(**plot_args)
        ax.legend(framealpha=0.5)
        ctx["sc_pdf_errors_graph_b64"] = plot_to_b64()

        plot_clear()

    # scholar
    # regarding totals, most recent row wanted
    latest = df.loc[df.index.max()]
    ctx = ctx | {
            "ias_indexed_pdfs": int(latest.elasticsearch_scholar_indexed_pdfs),
            "ias_containers": int(latest.elasticsearch_scholar_containers),
            "ias_sitemap_works": int(latest.scholar_sitemap_works_lines),
            "ias_sitemap_access": int(latest.scholar_sitemap_access_lines),
            }

    plot_args = default_plot_args | {
            "title": "scholar releases indexed per week",
            }
    ax = df["elasticsearch_scholar_indexed_pdfs_diff"].resample("7D").sum().plot.bar(**plot_args)
    ax.legend(framealpha=0.5)
    plt.ticklabel_format(style='plain', axis='y')
    ctx["ias_releases_diff_graph_b64"] = plot_to_b64()

    plot_clear()

    # ES queries

    plot_args = default_plot_args | {
            "title": "average scholar searches per week",
            }
    ax = df["elasticsearch_scholar_searches"].resample("7D").mean().plot.bar(**plot_args)
    ax.legend(framealpha=0.5)
    plt.ticklabel_format(style='plain', axis='y')
    ctx["ias_searches_diff_graph_b64"] = plot_to_b64()

    plot_clear()

    plot_args = default_plot_args | {
            "title": "average fatcat searches per week",
            }
    ax = df["elasticsearch_fatcat_searches"].resample("7D").mean().plot.bar(**plot_args)
    ax.legend(framealpha=0.5)
    plt.ticklabel_format(style='plain', axis='y')
    ctx["fc_searches_diff_graph_b64"] = plot_to_b64()

    plot_clear()

    # fatcat

    # regarding totals, most recent row wanted
    latest = df.loc[df.index.max()]
    ctx = ctx | {
            "fatcat_releases": int(latest.fatcat_releases),
            "fatcat_papers": int(latest.fatcat_papers),
            "fatcat_papers_in_web": int(latest.fatcat_papers_in_web),
            "fatcat_refs": int(latest.fatcat_refs),
            "fatcat_containers": int(latest.fatcat_containers),
            }

    plot_args = default_plot_args | {
            "title": "fatcat release change per week",
            }
    ax = df[["fatcat_releases_diff", "fatcat_papers_diff",
             "fatcat_papers_in_web_diff"]].resample("7D").sum().plot.bar(**plot_args)
    ax.legend(framealpha=0.5)
    plt.ticklabel_format(style='plain', axis='y')
    ctx["fc_release_diff_graph_b64"] = plot_to_b64()

    plot_clear()

    plot_args = default_plot_args | {
            "title": "fatcat citations change per week",
            }
    ax = df.fatcat_refs_diff.resample("7D").sum().plot.bar(**plot_args)
    ax.legend(framealpha=0.5)
    plt.ticklabel_format(style='plain', axis='y')
    ctx["fc_refs_diff_graph_b64"] = plot_to_b64()

    plot_clear()

    plot_args = default_plot_args | {
            "title": "fatcat containers change per week",
            }
    ax = df["fatcat_containers_diff"].resample("7D").sum().plot.bar(**plot_args)
    ax.legend(framealpha=0.5)
    plt.ticklabel_format(style='plain', axis='y')
    ctx["fc_containers_diff_graph_b64"] = plot_to_b64()

    plot_clear()

    return tmpl.render(ctx)


def email_header(emails: list[str]) -> str:
    return f'''From: nsmith@archive.org
To: {", ".join(emails)}
Subject: scholar stats for {date.today()}
Content-Type: text/html'''


if __name__ == "__main__":
    stats_path = os.environ.get("SCHOLSTATS_PATH", DEFAULT_STATS_PATH)
    match sys.argv:
        case [_, "gather"]:
            gather(stats_path)
        case [_, "report", *emails]:
            if len(emails) > 0:
                print(email_header(emails))
                print()
            print(report(make_frame(stats_path),
                         jenv.get_template("report.html")))
        case _:
            print("expected either 'gather' or 'report [emails]'",
                  file=sys.stderr)
            sys.exit(1)
