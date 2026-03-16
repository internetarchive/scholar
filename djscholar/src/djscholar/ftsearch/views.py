from django.conf import settings
from django.http import HttpRequest, HttpResponse
from django.shortcuts import render
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
            "size": 50,
        },
        request_timeout=20,
    )

    hits = data.get("hits", {}).get("hits", [])[:50]
    results = []
    for h in hits:
        source = h["_source"]
        results.append({
            "title": source.get("biblio", {}).get("title", "(untitled)"),
            "thumbnail_url": source.get("fulltext", {}).get("thumbnail_url", "").replace(
                "https://blobs.fatcat.wiki/", "https://scholar.archive.org/_s3/"
            ),
        })

    mode = request.GET.get("mode", "list")
    if mode not in ("list", "grid"):
        mode = "list"

    return render(request, "ftsearch/search.html", {
        "query": q,
        "results": results,
        "mode": mode,
    })


def work(request: HttpRequest, work_ident: str) -> HttpResponse:
    raise NotImplementedError


def access_redirect_wayback(request: HttpRequest, work_ident: str, url: str) -> HttpResponse:
    raise NotImplementedError


def access_redirect_ia_file(request: HttpRequest, work_ident: str, item: str, file_path: str) -> HttpResponse:
    raise NotImplementedError
