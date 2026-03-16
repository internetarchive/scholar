from django.conf import settings
from django.http import HttpRequest, HttpResponse
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
    raise NotImplementedError


def health(request: HttpRequest) -> HttpResponse:
    raise NotImplementedError


def searchhealth(request: HttpRequest) -> HttpResponse:
    raise NotImplementedError


def home(request: HttpRequest) -> HttpResponse:
    return render(request, "ftsearch/home.html")


def about(request: HttpRequest) -> HttpResponse:
    raise NotImplementedError


def help(request: HttpRequest) -> HttpResponse:
    raise NotImplementedError


def permalink(request: HttpRequest) -> HttpResponse:
    raise NotImplementedError


def search(request: HttpRequest) -> HttpResponse:
    q = request.POST.get("q", "").strip() or request.GET.get("q", "").strip()
    if not q:
        return render(request, "ftsearch/home.html")

    page_size = 25
    try:
        page = max(1, int(request.GET.get("page", 1)))
    except (ValueError, TypeError):
        page = 1
    offset = (page - 1) * page_size

    # TODO handler for get_es failing that can render a nice outage page
    data = get_es().search(
        index=settings.ES_INDEX,
        body={
            "query": {
                "query_string": {
                    "query": q,
                    "default_operator": "AND",
                    "analyze_wildcard": True,
                    "allow_leading_wildcard": False,
                    "lenient": True,
                }
            },
            "from": offset,
            "size": page_size,
            "highlight": {
                "fields": {
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
                },
            },
        },
        request_timeout=20,
    )

    total = data.get("hits", {}).get("total", {}).get("value", 0)
    hits = data.get("hits", {}).get("hits", [])
    results = []
    for h in hits:
        source = h["_source"]
        biblio = source.get("biblio", {})
        highlight = h.get("highlight", {})
        raw_snippets = []
        for field in highlight.values():
            raw_snippets.extend(field)
        snippets = [
            mark_safe(escape(s).replace("&lt;em&gt;", "<strong>").replace("&lt;/em&gt;", "</strong>"))
            for s in raw_snippets
        ]
        results.append({
            "title": biblio.get("title", "(untitled)"),
            "authors": biblio.get("contrib_names", []),
            "year": biblio.get("release_year"),
            "journal": biblio.get("container_name", ""),
            "thumbnail_url": source.get("fulltext", {}).get("thumbnail_url", "").replace(
                "https://blobs.fatcat.wiki/", "https://scholar.archive.org/_s3/"
            ),
            "highlights": snippets,
        })

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
    })


def work(request: HttpRequest, work_ident: str) -> HttpResponse:
    raise NotImplementedError


def access_redirect_wayback(request: HttpRequest, work_ident: str, url: str) -> HttpResponse:
    raise NotImplementedError


def access_redirect_ia_file(request: HttpRequest, work_ident: str, item: str, file_path: str) -> HttpResponse:
    raise NotImplementedError
