"""Elasticsearch queries for the fatcat web UI."""

import uuid
from typing import Any

from django.conf import settings

from djscholar import es
from djscholar.fcapi.fcid import fcid2uuid, uuid2fcid


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
        "_source": [
            "ident", "title", "contrib_names", "release_year",
            "release_type", "preservation",
        ],
    }

    try:
        resp = client.search(
            index=settings.ES_FATCAT_RELEASE_INDEX,
            body=body,
            request_cache=True,
        )
    except Exception:
        return []

    results = []
    for hit in resp["hits"]["hits"]:
        src = hit["_source"]
        # convert legacy ident to UUID for links
        release_uuid = fcid2uuid(src["ident"])
        contrib_names = src.get("contrib_names") or []
        if isinstance(contrib_names, str):
            contrib_names = [contrib_names]
        results.append({
            "uuid": release_uuid,
            "title": src.get("title"),
            "contrib_names": contrib_names,
            "release_year": src.get("release_year"),
            "release_type": src.get("release_type"),
            "preservation": src.get("preservation"),
        })
    return results
