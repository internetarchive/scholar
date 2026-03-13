import json
import urllib.request

from django.conf import settings
from django.http import HttpRequest, HttpResponse
from django.shortcuts import render


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

    es_query = json.dumps({
        "query": {
            "query_string": {
                "query": q,
                "default_operator": "AND",
                "analyze_wildcard": True,
                "allow_leading_wildcard": False,
                "lenient": True,
            }
        },
        "size": 20,
    }).encode()

    req = urllib.request.Request(
        f"{settings.ES_BASE}/{settings.ES_INDEX}/_search",
        data=es_query,
        headers={"Content-Type": "application/json"},
    )
    with urllib.request.urlopen(req, timeout=20) as resp:
        data = json.loads(resp.read())

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
