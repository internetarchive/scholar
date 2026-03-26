"""Elasticsearch queries for the fatcat web UI."""

import datetime
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


# -- Reference graph queries ------------------------------------------------


@dataclass
class RefHits:
    count_returned: int = 0
    count_found: int = 0
    offset: int = 0
    limit: int = 25
    query_time_ms: int = 0
    result_refs: list[dict[str, Any]] = field(default_factory=list)


def _clean_ref_key(ref_key: str | None, ref_index: int | None) -> str | None:
    """Clean up messy ref_key values, matching the original hacks() logic."""
    if not ref_key:
        return str(ref_index) if ref_index is not None else None
    ref_key = ref_key.strip()
    if ref_key and ref_key[0] in ("/", "_"):
        ref_key = ref_key[1:]
    if ref_key.startswith("10.") and "SICI" in ref_key and "-" in ref_key:
        ref_key = ref_key.split("-")[-1]
    if ref_key.startswith("10.") and "_" in ref_key:
        ref_key = ref_key.split("_")[-1]
    if len(ref_key) > 10 and "#" in ref_key:
        ref_key = ref_key.split("#")[-1]
    if len(ref_key) > 10 and "_" in ref_key:
        ref_key = ref_key.split("_")[-1]
    return ref_key


def _parse_ref_hit(src: dict[str, Any]) -> dict[str, Any]:
    """Convert an ES fatcat_ref hit into a template-friendly dict."""
    ref_index = src.get("ref_index")
    target_ident = src.get("target_release_ident")
    source_ident = src.get("source_release_ident")
    return {
        "ref_index": ref_index,
        "ref_key": _clean_ref_key(src.get("ref_key"), ref_index),
        "ref_locator": src.get("ref_locator"),
        "source_release_ident": source_ident,
        "source_release_uuid": fcid2uuid(source_ident) if source_ident else None,
        "source_work_ident": src.get("source_work_ident"),
        "source_wikipedia_article": src.get("source_wikipedia_article"),
        "source_year": src.get("source_year"),
        "target_release_ident": target_ident,
        "target_release_uuid": fcid2uuid(target_ident) if target_ident else None,
        "target_work_ident": src.get("target_work_ident"),
        "target_openlibrary_work": src.get("target_openlibrary_work"),
        "target_url": src.get("target_url"),
        "target_unstructured": src.get("target_unstructured"),
        "target_csl": src.get("target_csl"),
        "match_provenance": src.get("match_provenance"),
        "match_status": src.get("match_status"),
        "match_reason": src.get("match_reason"),
    }


def get_outbound_refs(
    release_uuid: uuid.UUID,
    offset: int = 0,
    limit: int = 100,
) -> RefHits | None:
    """Fetch outbound references (this release cites others) from fatcat_ref.

    Sorted by ref_index (sequential order in the paper).
    """
    try:
        client = es.client()
    except Exception:
        return None

    legacy_ident = uuid2fcid(release_uuid)
    limit = min(limit, 200)
    offset = max(offset, 0)

    body = {
        "size": limit,
        "from": offset,
        "query": {
            "bool": {
                "filter": [
                    {"term": {"source_release_ident": legacy_ident}},
                ],
            }
        },
        "sort": [{"ref_index": {"order": "asc"}}],
    }

    try:
        resp = client.search(
            index=settings.ES_FATCAT_REF_INDEX,
            body=body,
            track_total_hits=True,
        )
    except Exception:
        return None

    total_hits = resp["hits"]["total"]
    count_found = total_hits["value"] if isinstance(total_hits, dict) else total_hits

    results = [_parse_ref_hit(hit["_source"]) for hit in resp["hits"]["hits"]]
    results.sort(key=lambda r: r.get("ref_index") or 0)

    return RefHits(
        count_returned=len(results),
        count_found=count_found,
        offset=offset,
        limit=limit,
        query_time_ms=resp.get("took", 0),
        result_refs=results,
    )


def get_inbound_refs(
    release_uuid: uuid.UUID,
    offset: int = 0,
    limit: int = 25,
) -> RefHits | None:
    """Fetch inbound references (other releases citing this one) from fatcat_ref.

    Sorted by source_year descending (newest first).
    """
    try:
        client = es.client()
    except Exception:
        return None

    legacy_ident = uuid2fcid(release_uuid)
    limit = min(limit, 200)
    offset = max(offset, 0)

    body = {
        "size": limit,
        "from": offset,
        "query": {
            "bool": {
                "filter": [
                    {"term": {"target_release_ident": legacy_ident}},
                ],
            }
        },
        "sort": [{"source_year": {"order": "desc"}}],
    }

    try:
        resp = client.search(
            index=settings.ES_FATCAT_REF_INDEX,
            body=body,
            track_total_hits=True,
        )
    except Exception:
        return None

    total_hits = resp["hits"]["total"]
    count_found = total_hits["value"] if isinstance(total_hits, dict) else total_hits

    results = [_parse_ref_hit(hit["_source"]) for hit in resp["hits"]["hits"]]

    return RefHits(
        count_returned=len(results),
        count_found=count_found,
        offset=offset,
        limit=limit,
        query_time_ms=resp.get("took", 0),
        result_refs=results,
    )


# -- Global stats queries ----------------------------------------------------


def get_entity_stats() -> dict[str, Any] | None:
    """Fetch global entity stats from ES (releases, papers, containers)."""
    try:
        client = es.client()
    except Exception:
        return None

    stats: dict[str, Any] = {}

    # -- release totals + ref count --
    try:
        resp = client.search(
            index=settings.ES_FATCAT_RELEASE_INDEX,
            body={
                "size": 0,
                "aggs": {
                    "release_ref_count": {"sum": {"field": "ref_count"}},
                },
            },
            request_cache=True,
            track_total_hits=True,
        )
        total_hits = resp["hits"]["total"]
        stats["release"] = {
            "total": total_hits["value"] if isinstance(total_hits, dict) else total_hits,
            "refs_total": int(resp["aggregations"]["release_ref_count"]["value"]),
        }
    except Exception:
        stats["release"] = {"total": 0, "refs_total": 0}

    # -- paper-like subset (article-journal + paper-conference) --
    try:
        resp = client.search(
            index=settings.ES_FATCAT_RELEASE_INDEX,
            body={
                "size": 0,
                "query": {
                    "terms": {
                        "release_type": ["article-journal", "paper-conference"],
                    }
                },
                "aggs": {
                    "paper_like": {
                        "filters": {
                            "filters": {
                                "in_web": {"term": {"in_web": True}},
                                "is_oa": {"term": {"is_oa": True}},
                                "in_kbart": {"term": {"in_kbart": True}},
                                "in_web_not_kbart": {
                                    "bool": {
                                        "filter": [
                                            {"term": {"in_web": True}},
                                            {"term": {"in_kbart": False}},
                                        ]
                                    }
                                },
                            }
                        }
                    }
                },
            },
            request_cache=True,
            track_total_hits=True,
        )
        total_hits = resp["hits"]["total"]
        buckets = resp["aggregations"]["paper_like"]["buckets"]
        stats["papers"] = {
            "total": total_hits["value"] if isinstance(total_hits, dict) else total_hits,
            "in_web": buckets["in_web"]["doc_count"],
            "is_oa": buckets["is_oa"]["doc_count"],
            "in_kbart": buckets["in_kbart"]["doc_count"],
            "in_web_not_kbart": buckets["in_web_not_kbart"]["doc_count"],
        }
    except Exception:
        stats["papers"] = {"total": 0, "in_web": 0, "is_oa": 0, "in_kbart": 0, "in_web_not_kbart": 0}

    # -- container totals --
    try:
        resp = client.search(
            index=settings.ES_FATCAT_CONTAINER_INDEX,
            body={"size": 0},
            request_cache=True,
            track_total_hits=True,
        )
        total_hits = resp["hits"]["total"]
        stats["container"] = {
            "total": total_hits["value"] if isinstance(total_hits, dict) else total_hits,
        }
    except Exception:
        stats["container"] = {"total": 0}

    return stats


# -- Coverage search queries -------------------------------------------------


def _coverage_base_query(q: str, recent: bool = False) -> dict:
    """Build the base ES query for coverage searches."""
    query = {
        "query_string": {
            "query": q,
            "default_operator": "AND",
            "analyze_wildcard": True,
            "allow_leading_wildcard": False,
            "lenient": True,
            "fields": ["biblio"],
        }
    }
    if recent:
        today = datetime.date.today()
        start = str(today - datetime.timedelta(days=60))
        end = str(today + datetime.timedelta(days=1))
        return {
            "bool": {
                "must": query,
                "filter": [{"range": {"release_date": {"gte": start, "lte": end}}}],
            }
        }
    return query


def get_coverage_stats(q: str, recent: bool = False) -> dict[str, Any] | None:
    """Overall coverage stats for a query: total + preservation breakdown."""
    try:
        client = es.client()
    except Exception:
        return None

    body = {
        "size": 0,
        "query": _coverage_base_query(q, recent),
        "aggs": {
            "preservation": {
                "terms": {"field": "preservation", "missing": "_unknown"},
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

    preservation = {"bright": 0, "dark": 0, "shadows_only": 0, "none": 0, "total": total}
    for bucket in resp["aggregations"]["preservation"]["buckets"]:
        preservation[bucket["key"]] = bucket["doc_count"]
    preservation["none"] += preservation.pop("shadows_only", 0)

    return {
        "total": total,
        "preservation": preservation,
    }


def get_coverage_preservation_by_type(
    q: str, recent: bool = False,
) -> list[dict[str, Any]] | None:
    """Preservation coverage by release type for a query."""
    try:
        client = es.client()
    except Exception:
        return None

    body = {
        "size": 0,
        "query": _coverage_base_query(q, recent),
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


def get_coverage_preservation_by_year(q: str) -> list[dict[str, Any]] | None:
    """Year-by-year preservation histogram for a query (last 250 years)."""
    try:
        client = es.client()
    except Exception:
        return None

    this_year = datetime.date.today().year

    body = {
        "size": 0,
        "query": {
            "bool": {
                "must": {
                    "query_string": {
                        "query": q,
                        "default_operator": "AND",
                        "analyze_wildcard": True,
                        "allow_leading_wildcard": False,
                        "lenient": True,
                        "fields": ["biblio"],
                    }
                },
                "filter": [
                    {"range": {"release_year": {"gte": this_year - 249, "lte": this_year}}},
                ],
            }
        },
        "aggs": {
            "year_preservation": {
                "composite": {
                    "size": 1500,
                    "sources": [
                        {"year": {"histogram": {"field": "release_year", "interval": 1}}},
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

    buckets = resp["aggregations"]["year_preservation"]["buckets"]
    year_dicts: dict[int, dict[str, Any]] = {}

    year_nums = {int(h["key"]["year"]) for h in buckets}
    if year_nums:
        for num in range(min(year_nums), max(year_nums) + 1):
            year_dicts[num] = {"year": num, "bright": 0, "dark": 0, "shadows_only": 0, "none": 0}
        for row in buckets:
            year_dicts[int(row["key"]["year"])][row["key"]["preservation"]] = int(row["doc_count"])

    for yd in year_dicts.values():
        yd["none"] += yd.pop("shadows_only", 0)

    return sorted(year_dicts.values(), key=lambda x: x["year"])


def get_coverage_preservation_by_date(q: str) -> list[dict[str, Any]] | None:
    """Day-by-day preservation histogram for recent publications (last 60 days)."""
    try:
        client = es.client()
    except Exception:
        return None

    today = datetime.date.today()
    start_date = today - datetime.timedelta(days=60)
    end_date = today + datetime.timedelta(days=1)

    body = {
        "size": 0,
        "query": {
            "bool": {
                "must": {
                    "query_string": {
                        "query": q,
                        "default_operator": "AND",
                        "analyze_wildcard": True,
                        "allow_leading_wildcard": False,
                        "lenient": True,
                        "fields": ["biblio"],
                    }
                },
                "filter": [
                    {"range": {"release_date": {"gte": str(start_date), "lte": str(end_date)}}},
                ],
            }
        },
        "aggs": {
            "date_preservation": {
                "composite": {
                    "size": 1500,
                    "sources": [
                        {"date": {"histogram": {"field": "release_date", "interval": 1}}},
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

    buckets = resp["aggregations"]["date_preservation"]["buckets"]
    date_dicts: dict[str, dict[str, Any]] = {}

    # pre-fill every date in the range
    d = start_date
    while d <= end_date:
        date_dicts[str(d)] = {"date": str(d), "bright": 0, "dark": 0, "shadows_only": 0, "none": 0}
        d += datetime.timedelta(days=1)

    for row in buckets:
        date_key = row["key"]["date"][:10]
        if date_key in date_dicts:
            date_dicts[date_key][row["key"]["preservation"]] = int(row["doc_count"])

    for dd in date_dicts.values():
        dd["none"] += dd.pop("shadows_only", 0)

    return sorted(date_dicts.values(), key=lambda x: x["date"])
