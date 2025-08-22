#!/usr/bin/env python3

"""
Helper to generate KBART reports for fatcat inclusion in Keeper's Registry.

By default, this command looks for a list of container idents on stdin, and
will do a fatcat API fetch, then a search index lookup for stats, and print out
KBART-format TSV lines for any filtered containers (journals).

Two flags/modes are helpful when developing and debugging: The '--json' flag
results in JSON metadata getting written to stdout instead, for all containers
that are successfully fetched. The '--from-existing' flag results in full JSON
objects getting read from stdin, instead of doing API and search lookups.

"""

import csv
import datetime
import json
import os
import sys
from typing import Optional

import requests
from requests.adapters import HTTPAdapter
from requests.packages.urllib3.util.retry import Retry
from requests.exceptions import RetryError

THIS_YEAR = datetime.date.today().year
PAPER_RELEASE_TYPES = ['article-journal', 'conference_paper', 'article',
                       'post', 'report', 'retraction']
BASIC_THRESHOLD = 15


def requests_retry_session(
    retries: int = 10,
    backoff_factor: int = 3,
    status_forcelist: list[int] = [500, 502, 504],
    session: requests.Session = None,
) -> requests.Session:
    """
    From: https://www.peterbe.com/plog/best-practice-with-retries-with-requests
    """
    session = session or requests.Session()
    retry = Retry(
        total=retries,
        read=retries,
        connect=retries,
        backoff_factor=backoff_factor,
        status_forcelist=status_forcelist,
    )
    adapter = HTTPAdapter(max_retries=retry)
    session.mount("http://", adapter)
    session.mount("https://", adapter)
    return session


session = requests_retry_session()


def fetch_container_info(ident: str) -> dict:
    web_host = os.environ.get("FATCAT_WEB_HOST",
                              "https://scholar.archive.org/fatcat")
    api_host = os.environ.get("FATCAT_API_HOST",
                              "https://scholar.archive.org/_fc")
    info = dict(ident=ident)

    # https://scholar.archive.org/_fc/v0/container/iznnn644szdwva7khyxqzc73bi
    resp = session.get(f"{api_host}/v0/container/{ident}")
    resp.raise_for_status()
    info['fatcat_container'] = resp.json()

    # https://scholar.archive.org/fatcat/container/iznnn644szdwva7khyxqzc73bi/stats.json
    # https://scholar.archive.org/fatcat/container/iznnn644szdwva7khyxqzc73bi/preservation_by_year.json
    # https://scholar.archive.org/fatcat/container/iznnn644szdwva7khyxqzc73bi/preservation_by_volume.json
    # https://scholar.archive.org/fatcat/container/iznnn644szdwva7khyxqzc73bi/preservation_by_type.json
    for k in ('stats', 'preservation_by_year',
              'preservation_by_volume', 'preservation_by_type'):
        resp = session.get(f"{web_host}/container/{ident}/{k}.json")
        if resp.status_code == 503:
            info['status'] = 'fatcat-stats-503'
            return info
        resp.raise_for_status()
        info[k] = resp.json()
    return info


def filter_preservation_histogram(vals: list) -> list:
    """
    Takes a "preservation_by_year" or "preservation_by_volume" histogram dict,
    and filters out any buckets without complete preservation coverage.
    """
    return [v for v in vals
            if v['bright'] == v['bright'] + v['dark']
            + v['none'] + v['shadows_only']]


def eligible_status(info: dict) -> str:
    container = info['fatcat_container']
    raw_by_year = info['preservation_by_year']['histogram']
    raw_by_volume = info['preservation_by_volume']['histogram']
    by_year = filter_preservation_histogram(raw_by_year)
    by_volume = filter_preservation_histogram(raw_by_volume)
    by_type = info['preservation_by_type']['histogram']
    stats = info['stats']
    if container.get('container_type') in ['book-series',
                                           'blog',
                                           'magazine',
                                           'trade',
                                           'test',
                                           'repository',
                                           'archive']:
        return "container-type"
    if not container.get('issnl'):
        return "missing-issnl"

    # copy old/deprecated location of ISSN-E/ISSN-P to canonical location
    if not container.get('issne') and container.get('extra'):
        container['issne'] = container['extra'].get('issne')
    if not container.get('issnp') and container.get('extra'):
        container['issnp'] = container['extra'].get('issnp')

    if not (container.get('issne') or container.get('issnp')):
        print(f"container_{container['ident']}: missing both issne and issnp",
              file=sys.stderr)
        return "missing-issn"

    # overall counts
    if stats['total'] < BASIC_THRESHOLD:
        return "few-releases"

    if 1.0 * stats['preservation']['bright'] / stats['preservation']['total'] < 0.8:
        return "low-overall-preservation-fraction"

    # convert from "release" basis to "papers" basis
    papers_total = sum([v for k,v in stats['release_type'].items() if k in PAPER_RELEASE_TYPES])
    papers_preserved = sum([v['bright'] for v in by_type if v['release_type'] in PAPER_RELEASE_TYPES])

    if papers_total < BASIC_THRESHOLD:
        return "few-papers"

    if (1.0 * papers_total / stats['total']) < 0.8:
        print(f"container_{container['ident']}: {papers_total} papers of {stats['total']} total", file=sys.stderr)
        return "low-paper-fraction"

    if papers_preserved < BASIC_THRESHOLD:
        return "few-preserved-papers"

    if (1.0 * papers_preserved / papers_total) < 0.8:
        return "low-paper-preservation-fraction"

    # check that preservation counts line up well enough with volume/year counts
    if not raw_by_year:
        return "no-year-spans"
    if not raw_by_volume:
        return "no-volume-spans"
    if not by_year:
        return "no-preserved-year-spans"
    if not by_volume:
        return "no-preserved-volume-spans"
    if len(by_year) < 2:
        return "short-preserved-year-spans"
    if len(by_volume) < 2:
        return "short-preserved-volume-spans"

    # NOTE: we want to count full "raw_by_*" because we skip entire years if only one release missing
    preserved_with_year = sum([v['bright'] for v in raw_by_year])
    preserved_with_volume = sum([v['bright'] for v in raw_by_volume])
    if preserved_with_year < BASIC_THRESHOLD:
        return "few-preserved-with-year"
    if preserved_with_volume < BASIC_THRESHOLD:
        return "few-preserved-with-volume"

    if (1.0 * preserved_with_year / papers_total) < 0.8:
        print(f"container_{container['ident']}: {preserved_with_year} preserved-with-year of {papers_total} papers", file=sys.stderr)
        return "low-preserved-with-year-fraction"
    if (1.0 * preserved_with_volume / papers_total) < 0.8:
        print(f"container_{container['ident']}: {preserved_with_volume} preserved-with-volume of {papers_total} papers", file=sys.stderr)
        return "low-preserved-with-volume-fraction"

    years = sorted(list(set([v['year'] for v in by_year])))
    volumes = sorted(list(set([v['volume'] for v in by_volume])))
    if len(years) != max(years) - min(years) + 1:
        return "non-contiguous-years"
    if len(volumes) != max(volumes) - min(volumes) + 1:
        return "non-contiguous-volumes"
    assert years[0] == min(years) and years[-1] == max(years)
    assert volumes[0] == min(volumes) and volumes[-1] == max(volumes)
    return "success"

KBART_FIELD_NAMES = [
    'publication_type',
    'publication_title',
    'print_identifier',
    'online_identifier',
    'date_first_issue_online',
    'num_first_vol_online',
    'num_first_issue_online',
    'date_last_issue_online',
    'num_last_vol_online',
    'num_last_issue_online',
    'title_url',
    'first_author',
    'title_id',
    'coverage_depth',
    'coverage_notes',
    'publisher_name',
    'linking_issn',
]

def to_kbart(info: dict) -> bool:
    container = info['fatcat_container']
    by_year = filter_preservation_histogram(info['preservation_by_year']['histogram'])
    by_volume = filter_preservation_histogram(info['preservation_by_volume']['histogram'])
    assert (by_year and by_volume)
    #print(container)
    #print(by_year)
    #print(by_volume)
    last_year = by_year[-1]['year']
    last_volume = by_volume[-1]['volume']
    if last_year == THIS_YEAR:
        last_year -= 1
        last_volume -= 1
    row = dict(
        publication_type='serial',
        publication_title=container['name'],
        print_identifier=container.get('issnp'),
        online_identifier=container.get('issne'),
        date_first_issue_online=by_year[0]['year'],
        num_first_vol_online=by_volume[0]['volume'],
        num_first_issue_online='',
        date_last_issue_online=last_year,
        num_last_vol_online=last_volume,
        num_last_issue_online='',
        title_url='',
        first_author='',
        title_id=f"container_{container['ident']}",
        coverage_depth='fulltext',
        coverage_notes='',
        publisher_name=container.get('publisher', ''),
        linking_issn=container['issnl'],
        #embargo_info='', ?
        #access_type ?
    )
    return row


def parse_ident(raw: str) -> Optional[str]:
    raw = raw.strip()
    if not raw:
        return None
    if raw.startswith('{'):
        obj = json.loads(raw)
        if obj.get('ident'):
            return obj['ident']
    elif len(raw) == 26:
        return raw
    return None


def run(dump_json=False, from_existing=False):
    kbart_writer = csv.DictWriter(sys.stdout,
                                  fieldnames=KBART_FIELD_NAMES,
                                  dialect='excel-tab')
    if not dump_json:
        kbart_writer.writeheader()

    for line in sys.stdin:
        info = None
        if not line.strip():
            continue

        if from_existing:
            info = json.loads(line)
        else:
            ident = parse_ident(line)
            if not line:
                continue
            try:
                info = fetch_container_info(ident)
            except RetryError:
                print(f"container_{ident}: 500", file=sys.stderr)
                continue
            if not info:
                continue

        if not info.get('status'):
            info['status'] = eligible_status(info)
        info['is_eligible'] = info['status'] == "success"

        if dump_json:
            print(json.dumps(info))
        else:
            if not info['is_eligible']:
                continue
            row = to_kbart(info)
            kbart_writer.writerow(row)


if __name__ == '__main__':
    run(dump_json='--json' in sys.argv,
        from_existing='--from-existing' in sys.argv)
