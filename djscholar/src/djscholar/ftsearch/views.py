import datetime
import logging
import re
import urllib.parse
import uuid

from django.conf import settings
from django.db import IntegrityError
from django.db.models import F, Sum
from django.http import Http404, HttpRequest, HttpResponse
from django.shortcuts import redirect, render, resolve_url
from django.utils import timezone
from django.utils.html import escape
from django.utils.safestring import mark_safe
from elasticsearch.exceptions import RequestError, TransportError

import djscholar.es as es
from djscholar.fcapi.fcid import fcid2uuid, uuid2fcid
from djscholar.fcapi.models import File
from djscholar.fcapi.services import EntityNotFound
from djscholar.fcapi.services import files as file_svc
from djscholar.fcapi.services import works as work_svc
from djscholar.ftsearch.models import DailyAccessStat

logger = logging.getLogger(__name__)


def _record_access(access_type: str):
    """Increment today's access counter for the given type."""
    today = datetime.date.today()
    rows = DailyAccessStat.objects.filter(
        date=today, access_type=access_type,
    ).update(count=F("count") + 1)
    if rows == 0:
        try:
            DailyAccessStat.objects.create(
                date=today, access_type=access_type, count=1,
            )
        except IntegrityError:
            # Race: another request created it first
            DailyAccessStat.objects.filter(
                date=today, access_type=access_type,
            ).update(count=F("count") + 1)


def webhealth(request: HttpRequest) -> HttpResponse:
    try:
        if es.client().ping():
            return HttpResponse("ok")
    except Exception:
        pass
    return HttpResponse("es ping failed", status=503)


def searchhealth(request: HttpRequest) -> HttpResponse:
    try:
        data = es.client().search(
            index=settings.ES_INDEX,
            body={"query": {"match_all": {}}, "size": 1},
            request_timeout=20,
        )
        if data.get("hits", {}).get("hits"):
            return HttpResponse("ok")
    except Exception:
        pass
    return HttpResponse("search failed", status=503)


def home(request: HttpRequest) -> HttpResponse:
    return render(request, "ftsearch/home.html")


def about(request: HttpRequest) -> HttpResponse:
    return render(request, "ftsearch/about.html")


def help(request: HttpRequest) -> HttpResponse:
    return render(request, "ftsearch/help.html")


STATS_PERIODS = {
    "last_7d": ("Last 7 days", datetime.timedelta(days=7)),
    "last_30d": ("Last 30 days", datetime.timedelta(days=30)),
    "last_90d": ("Last 90 days", datetime.timedelta(days=90)),
    "last_365d": ("Last year", datetime.timedelta(days=365)),
    "all_time": ("All time", None),
}
STATS_PERIOD_LABELS = [
    ("last_7d", "Last 7 days"),
    ("last_30d", "Last 30 days"),
    ("last_90d", "Last 90 days"),
    ("last_365d", "Last year"),
    ("all_time", "All time"),
]
DEFAULT_STATS_PERIOD = "last_30d"

_FULLTEXT_ACCESS_TYPES = ["wayback", "ia_file", "ia_sim"]


def _es_fulltext_count(since_iso=None):
    """Count docs in scholar_fulltext that have PDF access.

    If since_iso is provided, only count docs indexed on or after that date.
    """
    filters = [{"terms": {"access.access_type": _FULLTEXT_ACCESS_TYPES}}]
    if since_iso:
        filters.append({"range": {"doc_index_ts": {"gte": since_iso}}})
    try:
        resp = es.client().count(
            index=settings.ES_INDEX,
            body={"query": {"bool": {"filter": filters}}},
        )
        return resp["count"]
    except Exception:
        return None


def stats(request: HttpRequest) -> HttpResponse:
    period_key = request.GET.get("period", DEFAULT_STATS_PERIOD)
    if period_key not in STATS_PERIODS:
        period_key = DEFAULT_STATS_PERIOD

    period_label, delta = STATS_PERIODS[period_key]

    # -- PDFs In --
    if delta is not None:
        since = timezone.now() - delta
        files_created = File.objects.filter(created__gte=since).count()
        es_indexed = _es_fulltext_count(since_iso=since.isoformat())
        access_qs = DailyAccessStat.objects.filter(date__gte=since.date())
    else:
        files_created = File.objects.count()
        es_indexed = _es_fulltext_count()
        access_qs = DailyAccessStat.objects.all()

    es_total = _es_fulltext_count()

    # -- PDFs Out --
    access_rows = access_qs.values("access_type").annotate(total=Sum("count"))
    access_by_type = {row["access_type"]: row["total"] for row in access_rows}
    total_access = sum(access_by_type.values()) if access_by_type else 0

    return render(request, "ftsearch/stats.html", {
        "period": period_key,
        "period_label": period_label,
        "period_labels": STATS_PERIOD_LABELS,
        "files_created": files_created,
        "es_indexed": es_indexed,
        "es_total": es_total,
        "access_by_type": access_by_type,
        "total_access": total_access,
    })


def random_paper(request: HttpRequest) -> HttpResponse:
    filters = [{"exists": {"field": "work_ident"}}]
    type_values = TYPE_FILTERS[DEFAULT_TYPE_FILTER]
    if type_values:
        filters.append({"terms": {"biblio.release_type": type_values}})
    access_clause = ACCESS_FILTERS[DEFAULT_ACCESS_FILTER]
    if access_clause:
        filters.append(access_clause)

    query: dict = {
        "function_score": {
            "query": {"match_all": {}},
            "random_score": {},
            "boost_mode": "replace",
        }
    }
    if filters:
        query = {
            "bool": {
                "must": query,
                "filter": filters,
            }
        }

    data = es.client().search(
        index=settings.ES_INDEX,
        body={"query": query, "size": 1},
        request_timeout=10,
    )
    hits = data.get("hits", {}).get("hits", [])
    if not hits:
        return redirect("ftsearch:home")
    work_ident = hits[0]["_source"].get("work_ident", "")
    if not work_ident:
        return redirect("ftsearch:home")
    work_uuid = fcid2uuid(work_ident)
    return redirect("ftsearch:work", work_uuid=work_uuid)


DATE_FILTERS = {
    "all_time": None,
    "past_week": {"gte": "now-1w"},
    "past_year": {"gte": "now-1y"},
    "since_2000": {"gte": "2000-01-01"},
    "before_1931": {"lt": "1931-01-01"},
}

DATE_FILTER_LABELS = [
    ("all_time", "All Time"),
    ("past_week", "Past Week"),
    ("past_year", "Past Year"),
    ("since_2000", "Since 2000"),
    ("before_1931", "Before 1931"),
]

TYPE_FILTERS = {
    "papers": ["article-journal", "paper-conference", "chapter", "article"],
    "reports": ["report", "standard"],
    "datasets": ["dataset", "software"],
    "everything": None,
}

TYPE_FILTER_LABELS = [
    ("papers", "Papers"),
    ("reports", "Reports"),
    ("datasets", "Datasets"),
    ("everything", "Everything"),
]

ACCESS_FILTERS = {
    "fulltext": {"terms": {"access.access_type": ["wayback", "ia_file", "ia_sim"]}},
    "microfilm": {"term": {"access.access_type": "ia_sim"}},
    "oa": {"term": {"tags": "oa"}},
    "everything": None,
}

ACCESS_FILTER_LABELS = [
    ("fulltext", "Fulltext"),
    ("microfilm", "Microfilm"),
    ("oa", "Open Access"),
    ("everything", "All Records"),
]

HIGHLIGHT_FIELDS = {
    "abstracts.body": {
        "number_of_fragments": 2,
        "fragment_size": 150,
    },
    "fulltext.body": {
        "number_of_fragments": 3,
        "fragment_size": 150,
    },
    "fulltext.acknowledgement": {
        "number_of_fragments": 2,
        "fragment_size": 150,
    },
    "fulltext.annex": {
        "number_of_fragments": 2,
        "fragment_size": 150,
    },
}

POOR_METADATA = {
    "bool": {
        "should": [
            {"bool": {"must_not": {"exists": {"field": "year"}}}},
            {"bool": {"must_not": {"exists": {"field": "type"}}}},
            {"bool": {"must_not": {"exists": {"field": "stage"}}}},
            {"bool": {"must_not": {"exists": {"field": "biblio.container_name"}}}},
        ],
    }
}


SORT_OPTIONS = {
    "relevancy": None,
    "newest": [{"biblio.release_date": {"order": "desc", "missing": "_last"}}],
    "oldest": [{"biblio.release_date": {"order": "asc", "missing": "_last"}}],
}

SORT_LABELS = [
    ("relevancy", "Relevancy"),
    ("newest", "Newest"),
    ("oldest", "Oldest"),
]

DEFAULT_PAGE_SIZE = 20
DEEP_PAGE_LIMIT = 2000
DEFAULT_DATE_FILTER = "all_time"
DEFAULT_TYPE_FILTER = "papers"
DEFAULT_ACCESS_FILTER = "fulltext"
DEFAULT_SORT = "relevancy"

_DOI_URL_RE = re.compile(r'^https?://(?:dx\.)?doi\.org/', re.I)
_RAW_ID_PATTERNS = [
    (re.compile(r'^10\.\d{4,}/\S+$'), "doi"),
    (re.compile(r'^PMC\d+$', re.I), "pmcid"),
    (re.compile(r'^\d{4}\.\d{4,5}(?:v\d+)?$'), "arxiv_id"),
    (re.compile(r'^[a-z-]+(?:\.[A-Z]{2})?/\d{7}$'), "arxiv_id"),
]


def _rewrite_id_query(q: str) -> str:
    """If q looks like a raw identifier, rewrite it as a field-specific query."""
    q = _DOI_URL_RE.sub("", q)
    for pattern, field in _RAW_ID_PATTERNS:
        if pattern.match(q):
            return f'{field}:"{q}"'
    return q


def _build_es_body(q, offset, page_size, date_filter, type_filter, access_filter, sort):
    qs = {
        "query_string": {
            "query": q,
            "default_operator": "AND",
            "analyze_wildcard": True,
            "allow_leading_wildcard": False,
            "lenient": True,
            "quote_field_suffix": ".exact",
            "fields": ["title^4", "biblio_all^3", "everything"],
        }
    }

    # When not filtering to fulltext, boost results that have fulltext access
    if access_filter != "fulltext":
        positive = {
            "bool": {
                "must": qs,
                "should": [
                    {"terms": {"access_type": ["ia_sim", "ia_file", "wayback"]}},
                ],
            }
        }
    else:
        positive = qs

    query = {
        "boosting": {
            "positive": positive,
            "negative": POOR_METADATA,
            "negative_boost": 0.5,
        }
    }

    # Build filter clauses
    filters = []

    date_range = DATE_FILTERS[date_filter]
    if date_range:
        filters.append({"range": {"biblio.release_date": date_range}})

    type_values = TYPE_FILTERS[type_filter]
    if type_values:
        filters.append({"terms": {"biblio.release_type": type_values}})

    access_clause = ACCESS_FILTERS[access_filter]
    if access_clause:
        filters.append(access_clause)

    if filters:
        query = {
            "bool": {
                "must": query,
                "filter": filters,
            }
        }

    body = {
        "query": query,
        "track_total_hits": True,
        "from": offset,
        "size": page_size,
        "collapse": {
            "field": "collapse_key",
            "inner_hits": {
                "name": "more_pages",
                "size": 0,
            },
        },
        "highlight": {
            "fields": HIGHLIGHT_FIELDS,
            "require_field_match": False,
            "highlight_query": {
                "query_string": {
                    "query": q,
                    "default_operator": "AND",
                    "lenient": True,
                }
            },
        },
    }

    sort_clause = SORT_OPTIONS[sort]
    if sort_clause:
        body["sort"] = sort_clause

    return body


_WAYBACK_URL_RE = re.compile(r'^https?://web\.archive\.org/web/\d{14}/(.*)')
_IA_FILE_URL_RE = re.compile(r'^https?://archive\.org/download/([^/]+)/(.*)')


def _rewrite_access_url(access_url: str, work_ident: str) -> str:
    """Rewrite external access URLs to route through our redirect views.

    This ensures PDF accesses are tracked via DailyAccessStat. URLs that
    don't match wayback or ia_file patterns are returned unchanged.
    """
    if not access_url or not work_ident:
        return access_url

    m = _WAYBACK_URL_RE.match(access_url)
    if m:
        original_url = m.group(1)
        return f"/_sd/work/{work_ident}/access/wayback/{original_url}"

    m = _IA_FILE_URL_RE.match(access_url)
    if m:
        item, file_path = m.group(1), m.group(2)
        return f"/_sd/work/{work_ident}/access/ia_file/{item}/{file_path}"

    return access_url


def _build_result(hit):
    source = hit["_source"]
    biblio = source.get("biblio", {})
    highlight = hit.get("highlight", {})
    raw_snippets = []
    for field in highlight.values():
        raw_snippets.extend(field)
    snippets = [
        mark_safe(escape(s).replace("&lt;em&gt;", "<strong>").replace("&lt;/em&gt;", "</strong>"))
        for s in raw_snippets
    ]
    ext_ids = []
    for label, key in [("doi", "doi"), ("pmid", "pmid"), ("pmcid", "pmcid"),
                       ("arxiv", "arxiv_id"), ("dblp", "dblp_id"),
                       ("doaj", "doaj_id"), ("jstor", "jstor_id")]:
        val = biblio.get(key)
        if val:
            ext_ids.append(f"{label}:{val}")

    fulltext = source.get("fulltext", {})
    access_url = fulltext.get("access_url", "")
    work_ident = source.get("work_ident", "")

    # Rewrite wayback/ia_file URLs to route through our redirect views for tracking
    access_url = _rewrite_access_url(access_url, work_ident)

    # Format file size for display
    size_bytes = fulltext.get("size_bytes")
    if size_bytes and size_bytes >= 1_000_000:
        size_label = f"{size_bytes / 1_000_000:.1f} MB"
    elif size_bytes and size_bytes >= 1_000:
        size_label = f"{size_bytes // 1_000} kB"
    else:
        size_label = ""

    # Extract capture year from wayback URL (/web/YYYYMMDD.../)
    capture_match = re.search(r"/web/(\d{4})", access_url)
    capture_year = capture_match.group(1) if capture_match else ""

    return {
        "title": biblio.get("title", "(untitled)"),
        "authors": biblio.get("contrib_names", []),
        "year": biblio.get("release_year"),
        "journal": biblio.get("container_name", ""),
        "thumbnail_url": fulltext.get("thumbnail_url", "").replace(
            "https://blobs.fatcat.wiki/", "https://scholar.archive.org/_s3/"
        ),
        "access_url": access_url,
        "access_type": fulltext.get("access_type", ""),
        "access_size": size_label,
        "capture_year": capture_year,
        "highlights": snippets,
        "ext_ids": ext_ids,
        "doi": biblio.get("doi", ""),
        "arxiv_id": biblio.get("arxiv_id", ""),
        "pmcid": biblio.get("pmcid", ""),
        "pmid": biblio.get("pmid", ""),
        "doaj_id": biblio.get("doaj_id", ""),
        "work_ident": source.get("work_ident", ""),
        "work_uuid": fcid2uuid(source["work_ident"]) if source.get("work_ident") else "",
        "release_stage": biblio.get("release_stage", ""),
        "fatcat_url": f"https://scholar.archive.org/_sd/fatcat/release/{fcid2uuid(biblio['release_ident'])}" if biblio.get("release_ident") else "",
    }


def search(request: HttpRequest) -> HttpResponse:
    q = request.POST.get("q", "").strip() or request.GET.get("q", "").strip()
    if not q:
        return render(request, "ftsearch/home.html")
    q = _rewrite_id_query(q)

    page_size = DEFAULT_PAGE_SIZE
    try:
        page = max(1, int(request.GET.get("page", 1)))
    except (ValueError, TypeError):
        page = 1
    offset = (page - 1) * page_size
    clamped = offset > DEEP_PAGE_LIMIT
    if clamped:
        offset = DEEP_PAGE_LIMIT
        page = offset // page_size + 1

    date_filter = request.GET.get("date", DEFAULT_DATE_FILTER)
    if date_filter not in DATE_FILTERS:
        date_filter = DEFAULT_DATE_FILTER

    type_filter = request.GET.get("type", DEFAULT_TYPE_FILTER)
    if type_filter not in TYPE_FILTERS:
        type_filter = DEFAULT_TYPE_FILTER

    access_filter = request.GET.get("access", DEFAULT_ACCESS_FILTER)
    if access_filter not in ACCESS_FILTERS:
        access_filter = DEFAULT_ACCESS_FILTER

    sort = request.GET.get("sort", DEFAULT_SORT)
    if sort not in SORT_OPTIONS:
        sort = DEFAULT_SORT

    search_error = None
    status_code = 200
    body = _build_es_body(q, offset, page_size, date_filter, type_filter, access_filter, sort)
    try:
        data = es.client().search(
            index=settings.ES_INDEX,
            body=body,
            request_timeout=20,
        )
    except RequestError as e:
        logger.warning("elasticsearch RequestError: %s", e.info)
        root_causes = e.info.get("error", {}).get("root_cause", [])
        if root_causes:
            message = root_causes[0].get("reason", str(e.info))
        else:
            message = str(e.info)
        search_error = {"type": "query", "message": message}
        status_code = 400
        data = None
    except (TransportError, Exception) as e:
        logger.warning("elasticsearch error: %s", e)
        search_error = {"type": "backend", "message": str(e)}
        status_code = 500
        data = None

    if data is not None:
        took_secs = round(data.get("took", 0) / 1000, 2)
        total = data.get("hits", {}).get("total", {}).get("value", 0)
        hits = data.get("hits", {}).get("hits", [])

        # Adjust total when collapse returns fewer than a full page on first page
        if offset == 0 and len(hits) < page_size:
            total = len(hits)

        results = [_build_result(h) for h in hits]
    else:
        took_secs = 0
        total = 0
        results = []

    mode = request.GET.get("mode", "list")
    if mode not in ("list", "grid"):
        mode = "list"

    total_pages = (total + page_size - 1) // page_size

    return render(request, "ftsearch/search.html", {
        "query": q,
        "results": results,
        "search_error": search_error,
        "mode": mode,
        "page": page,
        "total_pages": total_pages,
        "has_prev": page > 1,
        "has_next": page < total_pages and not clamped,
        "total": total,
        "took_secs": took_secs,
        "date_filter": date_filter,
        "date_filter_labels": DATE_FILTER_LABELS,
        "type_filter": type_filter,
        "type_filter_labels": TYPE_FILTER_LABELS,
        "access_filter": access_filter,
        "access_filter_labels": ACCESS_FILTER_LABELS,
        "sort": sort,
        "sort_labels": SORT_LABELS,
        "has_filters": (
            date_filter != DEFAULT_DATE_FILTER
            or type_filter != DEFAULT_TYPE_FILTER
            or access_filter != DEFAULT_ACCESS_FILTER
            or sort != DEFAULT_SORT
        ),
    }, status=status_code)


def work(request: HttpRequest, work_uuid: str) -> HttpResponse:
    work_ident = uuid2fcid(uuid.UUID(str(work_uuid)))
    data = es.client().search(
        index=settings.ES_INDEX,
        body={
            "query": {"term": {"work_ident": work_ident}},
            "size": 1,
        },
        request_timeout=10,
    )
    hits = data.get("hits", {}).get("hits", [])
    if not hits:
        raise Http404
    result = _build_result(hits[0])
    return render(request, "ftsearch/work.html", {"result": result})


def work_legacy(request: HttpRequest, work_ident: str) -> HttpResponse:
    """Redirect old fatcat-ident URLs to the canonical UUID URL."""
    work_uuid = fcid2uuid(work_ident)
    return redirect(resolve_url("ftsearch:work", work_uuid=work_uuid), permanent=True)


def _get_es_doc(work_ident: str):
    """Fetch raw ES _source for a work by doc ID. Returns None if not found."""
    try:
        resp = es.client().get(settings.ES_INDEX, f"work_{work_ident}")
        return resp["_source"]
    except Exception:
        return None


def _get_access_options(source: dict) -> list:
    """Combine fulltext and access entries from an ES doc source."""
    options = []
    fulltext = source.get("fulltext") or {}
    if fulltext.get("access_type") and fulltext.get("access_url"):
        options.append(fulltext)
    for entry in source.get("access") or []:
        if entry.get("access_type") and entry.get("access_url"):
            options.append(entry)
    return options


def _access_redirect_fallback(request, work_ident, *, original_url=None, archiveorg_path=None):
    """Fall back to the fatcat DB when ES doesn't have a match."""
    def _404():
        try:
            work_uuid = fcid2uuid(work_ident)
        except Exception:
            work_uuid = None
        return render(request, "ftsearch/access_404.html", {
            "work_ident": work_ident,
            "work_uuid": work_uuid,
            "original_url": original_url,
            "archiveorg_path": archiveorg_path,
        }, status=404)

    try:
        work_uuid = fcid2uuid(work_ident)
        work_svc.get(work_uuid)
    except (EntityNotFound, Exception):
        return _404()

    access_urls = file_svc.get_work_access_urls(work_uuid)
    for access_url in access_urls:
        if (
            original_url
            and "://web.archive.org/web/" in access_url
            and access_url.endswith(original_url)
        ):
            timestamp = access_url.split("/")[4]
            _record_access("wayback")
            return redirect(
                f"https://web.archive.org/web/{timestamp}id_/{original_url}"
            )
        elif (
            archiveorg_path
            and "://archive.org/" in access_url
            and archiveorg_path in access_url
        ):
            _record_access("ia_file")
            return redirect(access_url)

    return _404()


def access_redirect_wayback(request: HttpRequest, work_ident: str, url: str) -> HttpResponse:
    # Reconstruct the original URL from the raw request path, since Django
    # decodes the path parameter. Split after ".../access/wayback/".
    raw_path = request.get_full_path()
    marker = f"/work/{work_ident}/access/wayback/"
    idx = raw_path.find(marker)
    if idx >= 0:
        raw_original_url = raw_path[idx + len(marker):]
    else:
        raw_original_url = url
    original_url = urllib.parse.quote(
        raw_original_url,
        safe=":/%#?=@[]!$&'()*+,;",
    )

    source = _get_es_doc(work_ident)
    if not source:
        return _access_redirect_fallback(
            request, work_ident, original_url=original_url
        )

    for opt in _get_access_options(source):
        if (
            opt.get("access_type") == "wayback"
            and opt.get("access_url", "")
            and "://web.archive.org/web/" in opt["access_url"]
            and opt["access_url"].endswith(original_url)
        ):
            timestamp = opt["access_url"].split("/")[4]
            if len(timestamp) == 14 and timestamp.isdigit():
                _record_access("wayback")
                return redirect(
                    f"https://web.archive.org/web/{timestamp}id_/{original_url}"
                )

    return _access_redirect_fallback(
        request, work_ident, original_url=original_url
    )


def access_redirect_ia_file(request: HttpRequest, work_ident: str, item: str, file_path: str) -> HttpResponse:
    # Reconstruct the file path from the raw request path, since Django
    # decodes the path parameter. Split after ".../ia_file/{item}/".
    raw_path = request.get_full_path()
    marker = f"/access/ia_file/{item}/"
    idx = raw_path.find(marker)
    if idx >= 0:
        raw_file_path = raw_path[idx + len(marker):]
    else:
        raw_file_path = file_path
    original_path = urllib.parse.quote(raw_file_path)
    access_url = f"https://archive.org/download/{item}/{original_path}"

    source = _get_es_doc(work_ident)
    if not source:
        return _access_redirect_fallback(
            request, work_ident, archiveorg_path=f"/{item}/{original_path}"
        )

    for opt in _get_access_options(source):
        if opt.get("access_type") == "ia_file" and opt.get("access_url") == access_url:
            _record_access("ia_file")
            return redirect(access_url)

    return _access_redirect_fallback(
        request, work_ident, archiveorg_path=f"/{item}/{original_path}"
    )
