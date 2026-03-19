import re

from django.conf import settings
from django.http import Http404, HttpRequest, HttpResponse
from django.shortcuts import render
from django.utils.html import escape
from django.utils.safestring import mark_safe
from elasticsearch import Elasticsearch

_es = None


def get_es() -> Elasticsearch:
    global _es
    if _es is None:
        _es = Elasticsearch(
            settings.ES_HOSTS,
            sniff_on_start=settings.ES_SNIFF,
            sniff_on_connection_fail=settings.ES_SNIFF,
            sniffer_timeout=60,
        )
    return _es


def webhealth(request: HttpRequest) -> HttpResponse:
    try:
        if get_es().ping():
            return HttpResponse("ok")
    except Exception:
        pass
    return HttpResponse("es ping failed", status=503)


def searchhealth(request: HttpRequest) -> HttpResponse:
    try:
        data = get_es().search(
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
    raise NotImplementedError


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

DEFAULT_PAGE_SIZE = 25
DEFAULT_DATE_FILTER = "all_time"
DEFAULT_TYPE_FILTER = "papers"
DEFAULT_ACCESS_FILTER = "fulltext"
DEFAULT_SORT = "relevancy"


def build_es_body(q, offset, page_size, date_filter, type_filter, access_filter, sort):
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
        "fatcat_url": f"https://scholar.archive.org/fatcat/release/{biblio['release_ident']}" if biblio.get("release_ident") else "",
    }


def search(request: HttpRequest) -> HttpResponse:
    q = request.POST.get("q", "").strip() or request.GET.get("q", "").strip()
    if not q:
        return render(request, "ftsearch/home.html")

    page_size = DEFAULT_PAGE_SIZE
    try:
        page = max(1, int(request.GET.get("page", 1)))
    except (ValueError, TypeError):
        page = 1
    offset = (page - 1) * page_size

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

    # TODO handler for get_es failing that can render a nice outage page
    body = build_es_body(q, offset, page_size, date_filter, type_filter, access_filter, sort)
    data = get_es().search(
        index=settings.ES_INDEX,
        body=body,
        request_timeout=20,
    )

    took_secs = round(data.get("took", 0) / 1000, 2)
    total = data.get("hits", {}).get("total", {}).get("value", 0)
    hits = data.get("hits", {}).get("hits", [])

    # Adjust total when collapse returns fewer than a full page on first page
    if offset == 0 and len(hits) < page_size:
        total = len(hits)

    results = [_build_result(h) for h in hits]

    mode = request.GET.get("mode", "list")
    if mode not in ("list", "grid"):
        mode = "list"

    total_pages = (total + page_size - 1) // page_size

    return render(request, "ftsearch/search.html", {
        "query": q,
        "results": results,
        "mode": mode,
        "page": page,
        "total_pages": total_pages,
        "has_prev": page > 1,
        "has_next": page < total_pages,
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
    })


def work(request: HttpRequest, work_ident: str) -> HttpResponse:
    data = get_es().search(
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


def access_redirect_wayback(request: HttpRequest, work_ident: str, url: str) -> HttpResponse:
    raise NotImplementedError


def access_redirect_ia_file(request: HttpRequest, work_ident: str, item: str, file_path: str) -> HttpResponse:
    raise NotImplementedError
