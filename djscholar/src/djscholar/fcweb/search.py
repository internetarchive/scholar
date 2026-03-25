"""Elasticsearch queries for the fatcat web UI."""

import uuid
from dataclasses import dataclass, field
from typing import Any

from django.conf import settings

from djscholar import es
from djscholar.fcapi.fcid import fcid2uuid, uuid2fcid


_RELEASE_SOURCE_FIELDS = [
    "ident", "title", "contrib_names", "release_year",
    "release_type", "preservation",
    "doi", "pmcid", "pmid", "arxiv_id",
    "container_name",
]


def _parse_release_hit(src: dict[str, Any]) -> dict[str, Any]:
    """Convert an ES release hit _source into a template-friendly dict."""
    contrib_names = src.get("contrib_names") or []
    if isinstance(contrib_names, str):
        contrib_names = [contrib_names]
    return {
        "uuid": fcid2uuid(src["ident"]),
        "title": src.get("title"),
        "contrib_names": contrib_names,
        "release_year": src.get("release_year"),
        "release_type": src.get("release_type"),
        "preservation": src.get("preservation"),
        "doi": src.get("doi"),
        "pmcid": src.get("pmcid"),
        "pmid": src.get("pmid"),
        "arxiv_id": src.get("arxiv_id"),
        "container_name": src.get("container_name"),
    }


@dataclass
class SearchHits:
    count_returned: int = 0
    count_found: int = 0
    offset: int = 0
    limit: int = 25
    deep_page_limit: int = 2000
    query_time_ms: int = 0
    results: list[dict[str, Any]] = field(default_factory=list)


def get_container_stats(container_uuid: uuid.UUID) -> dict[str, Any] | None:
    """Fetch container-level stats from the fatcat_release ES index.

    Returns a dict with keys: total, in_web, in_kbart, is_preserved,
    preservation (dict of bright/dark/none counts), release_type (dict).
    Returns None if ES is unreachable or the query fails.
    """
    try:
        client = es.client()
    except Exception:
        return None

    legacy_ident = uuid2fcid(container_uuid)

    body = {
        "size": 0,
        "query": {"term": {"container_id": legacy_ident}},
        "aggs": {
            "container_stats": {
                "filters": {
                    "filters": {
                        "in_web": {"term": {"in_web": True}},
                        "in_kbart": {"term": {"in_kbart": True}},
                        "is_preserved": {"term": {"is_preserved": True}},
                    }
                }
            },
            "preservation": {
                "terms": {"field": "preservation", "missing": "_unknown"}
            },
            "release_type": {
                "terms": {"field": "release_type", "missing": "_unknown"}
            },
        },
    }

    try:
        resp = client.search(
            index=settings.ES_FATCAT_RELEASE_INDEX,
            body=body,
            request_cache=True,
            track_total_hits=True,
        )
    except Exception:
        return None

    total_hits = resp["hits"]["total"]
    total = total_hits["value"] if isinstance(total_hits, dict) else total_hits

    container_buckets = resp["aggregations"]["container_stats"]["buckets"]

    # preservation histogram
    preservation = {"bright": 0, "dark": 0, "shadows_only": 0, "none": 0, "total": total}
    for bucket in resp["aggregations"]["preservation"]["buckets"]:
        preservation[bucket["key"]] = bucket["doc_count"]
    # fold shadows_only into none
    preservation["none"] += preservation.pop("shadows_only", 0)

    # release type histogram
    release_type = {}
    for bucket in resp["aggregations"]["release_type"]["buckets"]:
        release_type[bucket["key"]] = bucket["doc_count"]

    return {
        "total": total,
        "in_web": container_buckets["in_web"]["doc_count"],
        "in_kbart": container_buckets["in_kbart"]["doc_count"],
        "is_preserved": container_buckets["is_preserved"]["doc_count"],
        "preservation": preservation,
        "release_type": release_type,
    }


def get_container_example_releases(
    container_uuid: uuid.UUID, limit: int = 5
) -> list[dict[str, Any]]:
    """Fetch a few recent/notable releases for a container.

    Queries the fatcat_release ES index sorted by web availability and date.
    Returns a list of dicts with keys: uuid, title, contrib_names,
    release_year, release_type, preservation. Returns [] on failure.
    """
    try:
        client = es.client()
    except Exception:
        return []

    legacy_ident = uuid2fcid(container_uuid)

    body = {
        "size": limit,
        "query": {
            "bool": {
                "must": [
                    {"term": {"container_id": legacy_ident}},
                ],
            }
        },
        "sort": [
            {"in_web": {"order": "desc"}},
            {"release_date": {"order": "desc"}},
        ],
        "_source": _RELEASE_SOURCE_FIELDS,
    }

    try:
        resp = client.search(
            index=settings.ES_FATCAT_RELEASE_INDEX,
            body=body,
            request_cache=True,
        )
    except Exception:
        return []

    return [_parse_release_hit(hit["_source"]) for hit in resp["hits"]["hits"]]


def search_releases(
    q: str,
    container_id: uuid.UUID | None = None,
    offset: int = 0,
    limit: int = 25,
) -> SearchHits:
    """Full-text search of the fatcat_release ES index.

    Supports optional container_id filtering for container-scoped search.
    """
    client = es.client()

    limit = min(limit, 300)
    offset = min(max(offset, 0), 2000)

    basic_query = {
        "query_string": {
            "query": q,
            "default_operator": "AND",
            "analyze_wildcard": True,
            "allow_leading_wildcard": False,
            "lenient": True,
            "fields": ["title^2", "biblio"],
        }
    }

    filters = []
    if container_id:
        filters.append({"term": {"container_id": uuid2fcid(container_id)}})

    if filters:
        query = {"bool": {"must": basic_query, "filter": filters}}
    else:
        query = {
            "boosting": {
                "positive": {
                    "bool": {
                        "must": basic_query,
                        "should": [{"term": {"in_ia": True}}],
                    }
                },
                "negative": {
                    "bool": {
                        "should": [
                            {"bool": {"must_not": {"exists": {"field": "title"}}}},
                            {"bool": {"must_not": {"exists": {"field": "release_year"}}}},
                            {"bool": {"must_not": {"exists": {"field": "release_type"}}}},
                            {"bool": {"must_not": {"exists": {"field": "release_stage"}}}},
                            {"bool": {"must_not": {"exists": {"field": "container_id"}}}},
                        ]
                    }
                },
                "negative_boost": 0.5,
            }
        }

    body = {
        "size": limit,
        "from": offset,
        "query": query,
        "_source": _RELEASE_SOURCE_FIELDS,
    }

    resp = client.search(
        index=settings.ES_FATCAT_RELEASE_INDEX,
        body=body,
        track_total_hits=True,
    )

    total_hits = resp["hits"]["total"]
    count_found = total_hits["value"] if isinstance(total_hits, dict) else total_hits

    results = [_parse_release_hit(hit["_source"]) for hit in resp["hits"]["hits"]]

    return SearchHits(
        count_returned=len(results),
        count_found=count_found,
        offset=offset,
        limit=limit,
        query_time_ms=resp.get("took", 0),
        results=results,
    )


def get_preservation_by_type(
    container_uuid: uuid.UUID,
) -> list[dict[str, Any]] | None:
    """Fetch preservation coverage broken down by release type for a container.

    Returns a list of dicts sorted by total, each with keys:
    release_type, bright, dark, none, total.
    Returns None on failure.
    """
    try:
        client = es.client()
    except Exception:
        return None

    legacy_ident = uuid2fcid(container_uuid)

    body = {
        "size": 0,
        "query": {"term": {"container_id": legacy_ident}},
        "aggs": {
            "type_preservation": {
                "composite": {
                    "size": 1500,
                    "sources": [
                        {"release_type": {"terms": {"field": "release_type"}}},
                        {"preservation": {"terms": {"field": "preservation"}}},
                    ],
                }
            }
        },
    }

    try:
        resp = client.search(
            index=settings.ES_FATCAT_RELEASE_INDEX,
            body=body,
            request_cache=True,
            track_total_hits=True,
        )
    except Exception:
        return None

    buckets = resp["aggregations"]["type_preservation"]["buckets"]
    type_dicts: dict[str, dict[str, Any]] = {}
    for row in buckets:
        rt = row["key"]["release_type"]
        if rt not in type_dicts:
            type_dicts[rt] = {
                "release_type": rt,
                "bright": 0, "dark": 0, "shadows_only": 0, "none": 0, "total": 0,
            }
        type_dicts[rt][row["key"]["preservation"]] = int(row["doc_count"])

    for td in type_dicts.values():
        td["none"] += td.pop("shadows_only", 0)
        td["total"] = td["bright"] + td["dark"] + td["none"]

    return sorted(type_dicts.values(), key=lambda x: x["total"], reverse=True)


def get_container_browse_year_volume_issue(
    container_uuid: uuid.UUID,
) -> list[dict[str, Any]] | None:
    """Fetch year/volume/issue breakdown for a container's releases.

    Returns a nested structure:
        [{ year: int, volumes: [{ volume: str|None, issues: [{ issue: str|None, count: int }] }] }]
    Sorted by year descending, volume descending, issue descending.
    Returns None on failure.
    """
    try:
        client = es.client()
    except Exception:
        return None

    legacy_ident = uuid2fcid(container_uuid)

    body = {
        "size": 0,
        "query": {
            "bool": {
                "filter": [
                    {"term": {"container_id": legacy_ident}},
                    {"bool": {"must_not": {"match": {"release_type": "stub"}}}},
                ],
            }
        },
        "aggs": {
            "year_volume": {
                "composite": {
                    "size": 1500,
                    "sources": [
                        {"year": {"histogram": {"field": "release_year", "interval": 1, "missing_bucket": True}}},
                        {"volume": {"terms": {"field": "volume", "missing_bucket": True}}},
                        {"issue": {"terms": {"field": "issue", "missing_bucket": True}}},
                    ],
                }
            }
        },
    }

    try:
        resp = client.search(
            index=settings.ES_FATCAT_RELEASE_INDEX,
            body=body,
            request_cache=True,
        )
    except Exception:
        return None

    buckets = resp["aggregations"]["year_volume"]["buckets"]
    # filter out rows with no year
    buckets = [h for h in buckets if h["key"]["year"] is not None]

    # build nested structure: year -> volume -> issue -> count
    year_dicts: dict[int, dict[str, dict[str, int]]] = {}
    for row in buckets:
        year = int(row["key"]["year"])
        volume = row["key"]["volume"] or ""
        issue = row["key"]["issue"] or ""
        if year not in year_dicts:
            year_dicts[year] = {}
        if volume not in year_dicts[year]:
            year_dicts[year][volume] = {}
        year_dicts[year][volume][issue] = int(row["doc_count"])

    def _sort_key(val: str | None) -> tuple[bool, bool, int, str]:
        if not val:
            return (False, False, 0, "")
        if val.isdigit():
            return (True, True, int(val), "")
        return (True, False, 0, val)

    result = []
    for year in sorted(year_dicts.keys(), reverse=True):
        volumes = []
        for vol in sorted(year_dicts[year].keys(), key=_sort_key, reverse=True):
            issues = []
            for iss in sorted(year_dicts[year][vol].keys(), key=_sort_key, reverse=True):
                issues.append({"issue": iss or None, "count": year_dicts[year][vol][iss]})
            volumes.append({"volume": vol or None, "issues": issues})
        result.append({"year": year, "volumes": volumes})
    return result


def search_container_releases(
    container_uuid: uuid.UUID,
    year: int | None = None,
    volume: str | None = None,
    issue: str | None = None,
) -> SearchHits | None:
    """Search releases in a container filtered by year/volume/issue.

    Used by the Browse tab when drilling into a specific year/volume/issue.
    """
    legacy_ident = uuid2fcid(container_uuid)

    # build query string from filters
    #
    # Three drill-down levels:
    #   year only          → all releases in that year (no volume/issue filter)
    #   year + volume      → releases in that volume (issue unconstrained)
    #   year + volume=""   → releases with NO volume value (issue="" → no issue)
    #   year + vol + issue → releases in that specific issue
    parts = [f"year:{year}"]
    if volume is not None:
        if volume != "":
            parts.append(f'volume:"{volume}"')
        else:
            parts.append("!volume:*")
        if issue is not None and issue != "":
            parts.append(f'issue:"{issue}"')
        elif issue is not None:
            # issue="" explicitly passed → no issue value
            parts.append("!issue:*")
        # else: issue not passed at all → don't filter on issue

    query_string = " ".join(parts)

    # determine sort order
    if volume is not None:
        sort_fields = ["first_page", "pages", "release_date"]
    else:
        sort_fields = ["release_date"]

    try:
        client = es.client()
    except Exception:
        return None

    body = {
        "size": 300,
        "query": {
            "bool": {
                "must": {
                    "query_string": {
                        "query": query_string,
                        "default_operator": "AND",
                        "lenient": True,
                    }
                },
                "filter": [
                    {"term": {"container_id": legacy_ident}},
                    {"bool": {"must_not": {"match": {"release_type": "stub"}}}},
                ],
            }
        },
        "sort": [{f: {"order": "asc"}} for f in sort_fields],
        "_source": _RELEASE_SOURCE_FIELDS + ["pages", "first_page", "release_date"],
    }

    try:
        resp = client.search(
            index=settings.ES_FATCAT_RELEASE_INDEX,
            body=body,
            track_total_hits=True,
        )
    except Exception:
        return None

    total_hits = resp["hits"]["total"]
    count_found = total_hits["value"] if isinstance(total_hits, dict) else total_hits

    results = []
    for hit in resp["hits"]["hits"]:
        src = hit["_source"]
        r = _parse_release_hit(src)
        r["pages"] = src.get("pages")
        r["first_page"] = src.get("first_page")
        r["release_date"] = src.get("release_date")
        results.append(r)

    # numeric re-sort by first_page when browsing by volume
    if volume is not None and results:
        for r in results:
            fp = r.get("first_page")
            if fp and str(fp).isdigit():
                r["first_page"] = int(fp)
        results.sort(key=lambda d: d.get("first_page") or 99999999)

    return SearchHits(
        count_returned=len(results),
        count_found=count_found,
        offset=0,
        limit=300,
        results=results,
    )
