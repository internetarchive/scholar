"""Stub views for the fatcat web UI.

Each view corresponds to a route from fatcat-scholar's src/scholar/fatcat/web.py.
They will be implemented incrementally.
"""

import datetime
import uuid
from types import SimpleNamespace
from urllib.parse import urlencode

from django.http import HttpRequest, HttpResponse, HttpResponseRedirect, Http404
from django.template import engines
from django.urls import reverse

from djscholar.fcapi.fcid import fcid2uuid, resolve_ident
from djscholar.fcapi.services import EntityNotFound
from djscholar.fcapi.services import containers as container_svc
from djscholar import fcsearch as fc_search
from djscholar.fcapi.services import creators as creator_svc
from djscholar.fcapi.services import files as file_svc
from djscholar.fcapi.services import filesets as fileset_svc
from djscholar.fcapi.services import releases as release_svc
from djscholar.fcapi.services import webcaptures as webcapture_svc
from djscholar.fcapi.services import works as work_svc
from djscholar.fcapi.services import changelog as changelog_svc
from djscholar.fcweb import graphics
from djscholar.fcweb import csl as csl_mod


def _get_jinja_env():
    return engines["jinja2"]


def _render(request: HttpRequest, template_name: str,
            context: dict | None = None, status: int = 200) -> HttpResponse:
    env = _get_jinja_env()
    template = env.get_template(template_name)
    html = template.render(context or {})
    return HttpResponse(html, status=status)


# some stubs remain but it's stuff we decided to not ship as part of the
# fatcat2 launch
# def _stub(request: HttpRequest, **kwargs) -> HttpResponse:
#     return HttpResponse("not yet implemented", status=501)


# -- Index & search ----------------------------------------------------------

def index(request: HttpRequest) -> HttpResponse:
    return _render(request, "fcweb/index.html")


_SEARCH_ENTITY_TYPES = {"releases", "containers"}


def search(request: HttpRequest) -> HttpResponse:
    q = request.GET.get("q", "").strip()
    entity_type = request.GET.get("entity_type", "releases")
    if entity_type not in _SEARCH_ENTITY_TYPES:
        entity_type = "releases"

    if not q:
        return _render(request, "fcweb/search.html", {
            "q": "",
            "entity_type": entity_type,
        })

    offset = request.GET.get("offset", "0")
    offset = max(0, int(offset)) if offset.isdigit() else 0

    found = None
    es_error = None

    try:
        if entity_type == "containers":
            found = fc_search.search_containers(q=q, offset=offset)
        else:
            found = fc_search.search_releases(q=q, offset=offset)
    except Exception as e:
        es_error = str(e)

    return _render(request, "fcweb/search.html", {
        "q": q,
        "entity_type": entity_type,
        "found": found,
        "es_error": es_error,
    })


def release_search(request: HttpRequest) -> HttpResponse:
    q = request.GET.get("q", "")
    params = {"entity_type": "releases"}
    if q:
        params["q"] = q
    return HttpResponseRedirect(reverse("fcweb:search") + "?" + urlencode(params))


def container_search(request: HttpRequest) -> HttpResponse:
    q = request.GET.get("q", "")
    params = {"entity_type": "containers"}
    if q:
        params["q"] = q
    return HttpResponseRedirect(reverse("fcweb:search") + "?" + urlencode(params))


# -- Lookups -----------------------------------------------------------------

_RELEASE_LOOKUP_PARAMS = [
    "doi", "wikidata_qid", "pmid", "pmcid", "isbn13",
    "jstor", "arxiv", "core", "ark", "mag", "oai", "hdl",
]
_CONTAINER_LOOKUP_PARAMS = ["issn", "issnl", "issne", "issnp", "wikidata_qid"]
_CREATOR_LOOKUP_PARAMS = ["orcid", "wikidata_qid"]
_FILE_LOOKUP_PARAMS = ["md5", "sha1", "sha256"]


def _generic_lookup(request, svc, param_names, entity_type, view_name):
    """Shared lookup logic: find first matching query param, look up entity, redirect."""
    lookup_key = None
    lookup_value = None
    for p in param_names:
        val = request.GET.get(p)
        if val:
            lookup_key = p
            lookup_value = val
            break

    if not lookup_key:
        return _render(request, f"fcweb/{entity_type}_lookup.html", {})

    try:
        entity = svc.lookup(lookup_key, lookup_value)
    except EntityNotFound:
        return _render(request, f"fcweb/{entity_type}_lookup.html", {
            "lookup_key": lookup_key,
            "lookup_value": lookup_value,
            "lookup_error": 404,
        }, status=404)
    except ValueError:
        return _render(request, f"fcweb/{entity_type}_lookup.html", {
            "lookup_key": lookup_key,
            "lookup_value": lookup_value,
            "lookup_error": 400,
        }, status=400)

    url = reverse(f"fcweb:{view_name}", kwargs={"ident": str(entity.id)})
    return HttpResponseRedirect(url)


def release_lookup(request: HttpRequest) -> HttpResponse:
    return _generic_lookup(request, release_svc, _RELEASE_LOOKUP_PARAMS,
                           "release", "release_view")


def container_lookup(request: HttpRequest) -> HttpResponse:
    return _generic_lookup(request, container_svc, _CONTAINER_LOOKUP_PARAMS,
                           "container", "container_view")


def creator_lookup(request: HttpRequest) -> HttpResponse:
    return _generic_lookup(request, creator_svc, _CREATOR_LOOKUP_PARAMS,
                           "creator", "creator_view")


def file_lookup(request: HttpRequest) -> HttpResponse:
    return _generic_lookup(request, file_svc, _FILE_LOOKUP_PARAMS,
                           "file", "file_view")


# -- Helpers -----------------------------------------------------------------

def _release_preservation(files, extids):
    """Compute preservation status for a release.

    Returns 'bright' (in IA/Wayback), 'dark' (preserved elsewhere), or 'none'.
    Logic mirrors fatcat-scholar's release_to_elasticsearch().
    """
    in_ia = False
    is_preserved = False

    for f in files:
        for u in f.urls.all():
            if "archive.org" in u.url:
                in_ia = True
            if u.rel in ("webarchive", "repository", "archive", "repo"):
                is_preserved = True

    if not in_ia and not is_preserved:
        # check external IDs that imply preservation
        is_preserved = bool(
            extids.get("pmcid")
            or extids.get("arxiv")
        )

    if in_ia:
        return "bright"
    elif is_preserved:
        return "dark"
    return "none"


# Maps ES _source keys (LHS) to the extids dict keys the release_view.html
# template expects (RHS, matching PG ReleaseExtId.id_type values).
_ES_EXTID_KEYS = {
    "doi": "doi",
    "pmid": "pmid",
    "pmcid": "pmcid",
    "isbn13": "isbn13",
    "wikidata_qid": "wikidata_qid",
    "arxiv_id": "arxiv",
    "jstor_id": "jstor",
    "core_id": "core",
    "ark_id": "ark",
    "hdl": "hdl",
    "doaj_id": "doaj",
    "dblp_id": "dblp",
    "mag_id": "mag",
}


def _release_view_es_context(release_uuid: uuid.UUID) -> dict | None:
    """Build a release_view context from the fatcat_release ES index.

    Stopgap for releases that have not yet been imported to Postgres from
    the legacy fatcat1 system. Returns None if the release is not indexed.
    """
    src = fc_search.get_release_es(release_uuid)
    if not src:
        return None

    work_id = None
    if src.get("work_id"):
        try:
            work_id = uuid.UUID(fcid2uuid(src["work_id"]))
        except (AssertionError, ValueError):
            pass

    release = SimpleNamespace(
        title=src.get("title"),
        subtitle=src.get("subtitle"),
        original_title=src.get("original_title"),
        release_type=src.get("release_type"),
        release_stage=src.get("release_stage"),
        release_year=src.get("release_year"),
        release_date=src.get("release_date"),
        volume=src.get("volume"),
        issue=src.get("issue"),
        pages=src.get("pages"),
        number=src.get("number"),
        version=src.get("version"),
        publisher=src.get("publisher"),
        language=src.get("language"),
        withdrawn_status=src.get("withdrawn_status"),
        work_id=work_id,
        refs=None,
    )

    container = None
    if src.get("container_id"):
        try:
            container_uuid = uuid.UUID(fcid2uuid(src["container_id"]))
        except (AssertionError, ValueError):
            container_uuid = None
        if container_uuid:
            container = SimpleNamespace(
                id=container_uuid,
                name=src.get("container_name"),
                issnl=src.get("container_issnl"),
                publisher=src.get("publisher"),
                container_type=src.get("container_type"),
            )

    extids = {}
    for es_key, extid_key in _ES_EXTID_KEYS.items():
        val = src.get(es_key)
        if val:
            extids[extid_key] = val

    contrib_names = src.get("contrib_names") or []
    if isinstance(contrib_names, str):
        contrib_names = [contrib_names]
    authors = [SimpleNamespace(raw_name=name, creator=None)
               for name in contrib_names]

    return {
        "release": release,
        "authors": authors,
        "contribs": authors,
        "extids": extids,
        "abstracts": [],
        "files": [],
        "webcaptures": [],
        "container": container,
        "ident": str(release_uuid),
        "preservation": src.get("preservation") or "none",
        "from_es_fallback": True,
    }


# -- Entity views ------------------------------------------------------------


def release_view(request: HttpRequest, ident: str) -> HttpResponse:
    try:
        release_uuid = resolve_ident(ident)
    except ValueError:
        return HttpResponse(f"bad id: {ident}", status=400)

    try:
        release = release_svc.get(release_uuid)
    except EntityNotFound:
        # Stopgap: PG migration is ongoing, so fall back to the fatcat_release
        # ES index if we don't have the release in PG yet.
        ctx = _release_view_es_context(release_uuid)
        if ctx is None:
            raise Http404(f"release not found: {ident}")
        return _render(request, "fcweb/release_view.html", ctx)

    authors = release_svc.get_authors(release_uuid)
    contribs = list(release_svc.get_contribs(release_uuid))
    extids = release_svc.get_extids(release_uuid)
    abstracts = list(release_svc.get_abstracts(release_uuid))
    files = list(release_svc.get_files(release_uuid))
    webcaptures = list(release_svc.get_webcaptures(release_uuid))
    container = release.container

    preservation = _release_preservation(files, extids)

    return _render(request, "fcweb/release_view.html", {
        "release": release,
        "authors": authors,
        "contribs": contribs,
        "extids": extids,
        "abstracts": abstracts,
        "files": files,
        "webcaptures": webcaptures,
        "container": container,
        "ident": str(release_uuid),
        "preservation": preservation,
    })


def release_view_metadata(request: HttpRequest, ident: str) -> HttpResponse:
    try:
        release_uuid = resolve_ident(ident)
        release = release_svc.get(release_uuid)
    except EntityNotFound:
        raise Http404(f"release not found: {ident}")
    except ValueError:
        return HttpResponse(f"bad id: {ident}", status=400)

    authors = release_svc.get_authors(release_uuid)
    contribs = list(release_svc.get_contribs(release_uuid))

    return _render(request, "fcweb/release_view_metadata.html", {
        "release": release,
        "authors": authors,
        "contribs": contribs,
        "metadata": release_svc.schema_metadata(release),
        "extra": release.extra,
        "ident": str(release_uuid),
    })


def release_view_contribs(request: HttpRequest, ident: str) -> HttpResponse:
    try:
        release_uuid = resolve_ident(ident)
        release = release_svc.get(release_uuid)
    except EntityNotFound:
        raise Http404(f"release not found: {ident}")
    except ValueError:
        return HttpResponse(f"bad id: {ident}", status=400)

    authors = release_svc.get_authors(release_uuid)
    contribs = list(release_svc.get_contribs(release_uuid))

    return _render(request, "fcweb/release_view_contribs.html", {
        "release": release,
        "authors": authors,
        "contribs": contribs,
        "ident": str(release_uuid),
    })


def release_view_references(request: HttpRequest, ident: str) -> HttpResponse:
    try:
        release_uuid = resolve_ident(ident)
        release = release_svc.get(release_uuid)
    except EntityNotFound:
        raise Http404(f"release not found: {ident}")
    except ValueError:
        return HttpResponse(f"bad id: {ident}", status=400)

    authors = release_svc.get_authors(release_uuid)
    contribs = list(release_svc.get_contribs(release_uuid))

    return _render(request, "fcweb/release_view_references.html", {
        "release": release,
        "authors": authors,
        "contribs": contribs,
        "ident": str(release_uuid),
    })


def release_save(request: HttpRequest, ident: str) -> HttpResponse:
    try:
        release_uuid = resolve_ident(ident)
        release = release_svc.get(release_uuid)
    except EntityNotFound:
        raise Http404(f"release not found: {ident}")
    except ValueError:
        return HttpResponse(f"bad id: {ident}", status=400)

    extids = release_svc.get_extids(release_uuid)

    return _render(request, "fcweb/release_save.html", {
        "release": release,
        "extids": extids,
        "ident": str(release_uuid),
    })


def _enrich_refs(hits, direction: str) -> None:
    """Annotate ref hits with release titles from PostgreSQL (in-place)."""
    if not hits or not hits.result_refs:
        return
    # collect release UUIDs that need enrichment
    import uuid as _uuid
    uuids = []
    for ref in hits.result_refs:
        key = "target_release_uuid" if direction == "out" else "source_release_uuid"
        val = ref.get(key)
        if val:
            try:
                uuids.append(_uuid.UUID(val))
            except ValueError:
                pass
    if not uuids:
        return
    releases = release_svc.get_bulk(uuids)
    for ref in hits.result_refs:
        key = "target_release_uuid" if direction == "out" else "source_release_uuid"
        val = ref.get(key)
        if val:
            try:
                r = releases.get(_uuid.UUID(val))
            except ValueError:
                r = None
            if r:
                ref["enriched_title"] = r.title


def release_view_refs_outbound(request: HttpRequest, ident: str) -> HttpResponse:
    try:
        release_uuid = resolve_ident(ident)
        release = release_svc.get(release_uuid)
    except EntityNotFound:
        raise Http404(f"release not found: {ident}")
    except ValueError:
        return HttpResponse(f"bad id: {ident}", status=400)

    authors = release_svc.get_authors(release_uuid)
    contribs = list(release_svc.get_contribs(release_uuid))

    offset = request.GET.get("offset", "0")
    offset = max(0, int(offset)) if offset.isdigit() else 0

    hits = fc_search.get_outbound_refs(release_uuid, offset=offset)
    _enrich_refs(hits, "out")

    legacy_ref_count = len(release.refs) if release.refs else 0

    return _render(request, "fcweb/release_view_refs.html", {
        "release": release,
        "authors": authors,
        "contribs": contribs,
        "ident": str(release_uuid),
        "hits": hits,
        "direction": "out",
        "legacy_ref_count": legacy_ref_count,
    })


def release_view_refs_inbound(request: HttpRequest, ident: str) -> HttpResponse:
    try:
        release_uuid = resolve_ident(ident)
        release = release_svc.get(release_uuid)
    except EntityNotFound:
        raise Http404(f"release not found: {ident}")
    except ValueError:
        return HttpResponse(f"bad id: {ident}", status=400)

    authors = release_svc.get_authors(release_uuid)
    contribs = list(release_svc.get_contribs(release_uuid))

    offset = request.GET.get("offset", "0")
    offset = max(0, int(offset)) if offset.isdigit() else 0

    hits = fc_search.get_inbound_refs(release_uuid, offset=offset)
    _enrich_refs(hits, "in")

    return _render(request, "fcweb/release_view_refs.html", {
        "release": release,
        "authors": authors,
        "contribs": contribs,
        "ident": str(release_uuid),
        "hits": hits,
        "direction": "in",
    })


def container_view(request: HttpRequest, ident: str) -> HttpResponse:
    try:
        container_uuid = resolve_ident(ident)
        container = container_svc.get(container_uuid)
    except EntityNotFound:
        raise Http404(f"container not found: {ident}")
    except ValueError:
        return HttpResponse(f"bad id: {ident}", status=400)

    stats = fc_search.get_container_stats(container_uuid)
    example_releases = fc_search.get_container_example_releases(container_uuid)

    extra = container.extra or {}
    in_doaj = bool(extra.get("doaj", {}).get("as_of"))
    in_road = bool(extra.get("road", {}).get("as_of"))
    is_oa = bool(
        in_doaj
        or in_road
        or extra.get("szczepanski", {}).get("as_of")
        or (extra.get("default_license") or "").startswith("CC-")
    )
    if extra.get("sherpa_romeo", {}).get("color") == "white":
        is_oa = False

    return _render(request, "fcweb/container_view.html", {
        "container": container,
        "stats": stats,
        "is_oa": is_oa,
        "in_doaj": in_doaj,
        "in_road": in_road,
        "example_releases": example_releases,
        "ident": str(container_uuid),
    })


def container_view_metadata(request: HttpRequest, ident: str) -> HttpResponse:
    try:
        container_uuid = resolve_ident(ident)
        container = container_svc.get(container_uuid)
    except EntityNotFound:
        raise Http404(f"container not found: {ident}")
    except ValueError:
        return HttpResponse(f"bad id: {ident}", status=400)

    return _render(request, "fcweb/container_view_metadata.html", {
        "container": container,
        "metadata": container_svc.schema_metadata(container),
        "extra": container.extra,
        "ident": str(container_uuid),
    })


def container_view_browse(request: HttpRequest, ident: str) -> HttpResponse:
    try:
        container_uuid = resolve_ident(ident)
        container = container_svc.get(container_uuid)
    except EntityNotFound:
        raise Http404(f"container not found: {ident}")
    except ValueError:
        return HttpResponse(f"bad id: {ident}", status=400)

    year_str = request.GET.get("year")
    volume = request.GET.get("volume")
    issue = request.GET.get("issue")

    year = None
    if year_str and year_str.isdigit():
        year = int(year_str)

    browse_data = None
    releases_found = None

    if year is not None:
        # drill into a specific year (and optionally volume/issue)
        releases_found = fc_search.search_container_releases(
            container_uuid, year=year, volume=volume, issue=issue,
        )
    else:
        # show the year/volume/issue overview table
        browse_data = fc_search.get_container_browse_year_volume_issue(container_uuid)

    return _render(request, "fcweb/container_view_browse.html", {
        "container": container,
        "browse_data": browse_data,
        "releases_found": releases_found,
        "year": year,
        "volume": volume,
        "issue": issue,
        "ident": str(container_uuid),
    })


def container_view_coverage(request: HttpRequest, ident: str) -> HttpResponse:
    try:
        container_uuid = resolve_ident(ident)
        container = container_svc.get(container_uuid)
    except EntityNotFound:
        raise Http404(f"container not found: {ident}")
    except ValueError:
        return HttpResponse(f"bad id: {ident}", status=400)

    stats = fc_search.get_container_stats(container_uuid)
    type_preservation = fc_search.get_preservation_by_type(container_uuid)

    year_data = fc_search.get_container_preservation_by_year(container_uuid)
    year_histogram_svg = graphics.preservation_by_year_histogram(year_data) if year_data else None

    volume_data = fc_search.get_container_preservation_by_volume(container_uuid)
    volume_histogram_svg = graphics.preservation_by_volume_histogram(volume_data) if volume_data else None

    return _render(request, "fcweb/container_view_coverage.html", {
        "container": container,
        "stats": stats,
        "type_preservation": type_preservation,
        "year_histogram_svg": year_histogram_svg,
        "volume_histogram_svg": volume_histogram_svg,
        "ident": str(container_uuid),
    })


def container_view_search(request: HttpRequest, ident: str) -> HttpResponse:
    try:
        container_uuid = resolve_ident(ident)
        container = container_svc.get(container_uuid)
    except EntityNotFound:
        raise Http404(f"container not found: {ident}")
    except ValueError:
        return HttpResponse(f"bad id: {ident}", status=400)

    q = request.GET.get("q")
    found = None
    es_error = None

    if q:
        offset = request.GET.get("offset", "0")
        offset = max(0, int(offset)) if offset.isdigit() else 0
        try:
            found = fc_search.search_releases(
                q=q, container_id=container_uuid, offset=offset,
            )
        except Exception as e:
            es_error = str(e)

    return _render(request, "fcweb/container_view_search.html", {
        "container": container,
        "q": q or "",
        "found": found,
        "es_error": es_error,
        "ident": str(container_uuid),
    })


def creator_view(request: HttpRequest, ident: str) -> HttpResponse:
    try:
        creator_uuid = resolve_ident(ident)
        creator = creator_svc.get(creator_uuid)
    except EntityNotFound:
        raise Http404(f"creator not found: {ident}")
    except ValueError:
        return HttpResponse(f"bad id: {ident}", status=400)

    releases = list(creator_svc.get_releases(creator_uuid))

    return _render(request, "fcweb/creator_view.html", {
        "creator": creator,
        "releases": releases,
        "ident": str(creator_uuid),
    })


def creator_view_metadata(request: HttpRequest, ident: str) -> HttpResponse:
    try:
        creator_uuid = resolve_ident(ident)
        creator = creator_svc.get(creator_uuid)
    except EntityNotFound:
        raise Http404(f"creator not found: {ident}")
    except ValueError:
        return HttpResponse(f"bad id: {ident}", status=400)

    return _render(request, "fcweb/creator_view_metadata.html", {
        "creator": creator,
        "metadata": creator_svc.schema_metadata(creator),
        "extra": creator.extra,
        "ident": str(creator_uuid),
    })


def file_view(request: HttpRequest, ident: str) -> HttpResponse:
    try:
        file_uuid = resolve_ident(ident)
        file = file_svc.get(file_uuid)
    except EntityNotFound:
        raise Http404(f"file not found: {ident}")
    except ValueError:
        return HttpResponse(f"bad id: {ident}", status=400)

    releases = list(file_svc.get_releases(file_uuid))
    urls = list(file.urls.all())

    # pick best access URL: prefer wayback > webarchive > any
    best_url = None
    for u in urls:
        if "web.archive.org" in u.url:
            best_url = u.url
            break
        if u.rel == "webarchive" and not best_url:
            best_url = u.url
        elif not best_url and u.url.startswith("http"):
            best_url = u.url

    return _render(request, "fcweb/file_view.html", {
        "file": file,
        "releases": releases,
        "urls": urls,
        "best_url": best_url,
        "ident": str(file_uuid),
    })


def file_view_metadata(request: HttpRequest, ident: str) -> HttpResponse:
    try:
        file_uuid = resolve_ident(ident)
        file = file_svc.get(file_uuid)
    except EntityNotFound:
        raise Http404(f"file not found: {ident}")
    except ValueError:
        return HttpResponse(f"bad id: {ident}", status=400)

    return _render(request, "fcweb/file_view_metadata.html", {
        "file": file,
        "metadata": file_svc.schema_metadata(file),
        "extra": file.extra,
        "ident": str(file_uuid),
    })


def fileset_view(request: HttpRequest, ident: str) -> HttpResponse:
    try:
        fs_uuid = resolve_ident(ident)
        fileset = fileset_svc.get(fs_uuid)
    except EntityNotFound:
        raise Http404(f"fileset not found: {ident}")
    except ValueError:
        return HttpResponse(f"bad id: {ident}", status=400)

    release = fileset.release
    manifest = list(fileset_svc.get_files(fs_uuid))
    urls = list(fileset_svc.get_urls(fs_uuid))

    # compute total size from manifest
    total_size = sum(f.size_bytes for f in manifest if f.size_bytes)

    # find base URLs for per-file access links
    archive_base = None
    webarchive_base = None
    for u in urls:
        if u.rel == "archive-base":
            archive_base = u.url
        elif u.rel in ("webarchive-base", "repository-base"):
            webarchive_base = u.url

    return _render(request, "fcweb/fileset_view.html", {
        "fileset": fileset,
        "release": release,
        "manifest": manifest,
        "urls": urls,
        "total_size": total_size or None,
        "archive_base": archive_base,
        "webarchive_base": webarchive_base,
        "ident": str(fs_uuid),
    })


def fileset_view_metadata(request: HttpRequest, ident: str) -> HttpResponse:
    try:
        fs_uuid = resolve_ident(ident)
        fileset = fileset_svc.get(fs_uuid)
    except EntityNotFound:
        raise Http404(f"fileset not found: {ident}")
    except ValueError:
        return HttpResponse(f"bad id: {ident}", status=400)

    return _render(request, "fcweb/fileset_view_metadata.html", {
        "fileset": fileset,
        "metadata": fileset_svc.schema_metadata(fileset),
        "extra": fileset.extra,
        "ident": str(fs_uuid),
    })


def webcapture_view(request: HttpRequest, ident: str) -> HttpResponse:
    try:
        wc_uuid = resolve_ident(ident)
        webcapture = webcapture_svc.get(wc_uuid)
    except EntityNotFound:
        raise Http404(f"webcapture not found: {ident}")
    except ValueError:
        return HttpResponse(f"bad id: {ident}", status=400)

    release = webcapture.release
    archive_urls = list(webcapture.urls.all())
    cdx_lines = list(webcapture.cdx_lines.all())

    # build wayback suffix from original_url and capture timestamp
    wayback_suffix = ""
    if webcapture.original_url and webcapture.captured:
        ts = webcapture.captured.strftime("%Y%m%d%H%M%S")
        wayback_suffix = f"{ts}/{webcapture.original_url}"

    # pick best access URL for the "View Web Archive" button
    best_url = None
    for u in archive_urls:
        if u.rel == "wayback":
            best_url = u.url + wayback_suffix
            break
        if u.rel == "webarchive" and not best_url:
            best_url = u.url

    return _render(request, "fcweb/webcapture_view.html", {
        "webcapture": webcapture,
        "release": release,
        "archive_urls": archive_urls,
        "cdx_lines": cdx_lines,
        "wayback_suffix": wayback_suffix,
        "best_url": best_url,
        "ident": str(wc_uuid),
    })


def webcapture_view_metadata(request: HttpRequest, ident: str) -> HttpResponse:
    try:
        wc_uuid = resolve_ident(ident)
        webcapture = webcapture_svc.get(wc_uuid)
    except EntityNotFound:
        raise Http404(f"webcapture not found: {ident}")
    except ValueError:
        return HttpResponse(f"bad id: {ident}", status=400)

    return _render(request, "fcweb/webcapture_view_metadata.html", {
        "webcapture": webcapture,
        "metadata": webcapture_svc.schema_metadata(webcapture),
        "extra": webcapture.extra,
        "ident": str(wc_uuid),
    })

def work_view(request: HttpRequest, ident: str) -> HttpResponse:
    try:
        work_uuid = resolve_ident(ident)
        work = work_svc.get(work_uuid)
    except EntityNotFound:
        raise Http404(f"work not found: {ident}")
    except ValueError:
        return HttpResponse(f"bad id: {ident}", status=400)

    releases = list(work_svc.get_releases(work_uuid))

    return _render(request, "fcweb/work_view.html", {
        "work": work,
        "releases": releases,
        "ident": str(work_uuid),
    })


def work_view_metadata(request: HttpRequest, ident: str) -> HttpResponse:
    try:
        work_uuid = resolve_ident(ident)
        work = work_svc.get(work_uuid)
    except EntityNotFound:
        raise Http404(f"work not found: {ident}")
    except ValueError:
        return HttpResponse(f"bad id: {ident}", status=400)

    return _render(request, "fcweb/work_view_metadata.html", {
        "work": work,
        "metadata": work_svc.schema_metadata(work),
        "extra": work.extra,
        "ident": str(work_uuid),
    })


# -- Underscore redirects (legacy URLs like /release_IDENT → /release/IDENT) --


def _underscore_redirect(request: HttpRequest, ident: str, view_name: str) -> HttpResponse:
    return HttpResponseRedirect(reverse(f"fcweb:{view_name}", kwargs={"ident": ident}))


def container_underscore_view(request: HttpRequest, ident: str) -> HttpResponse:
    return _underscore_redirect(request, ident, "container_view")


def file_underscore_view(request: HttpRequest, ident: str) -> HttpResponse:
    return _underscore_redirect(request, ident, "file_view")


def creator_underscore_view(request: HttpRequest, ident: str) -> HttpResponse:
    return _underscore_redirect(request, ident, "creator_view")


def release_underscore_view(request: HttpRequest, ident: str) -> HttpResponse:
    return _underscore_redirect(request, ident, "release_view")


def webcapture_underscore_view(request: HttpRequest, ident: str) -> HttpResponse:
    return _underscore_redirect(request, ident, "webcapture_view")


def work_underscore_view(request: HttpRequest, ident: str) -> HttpResponse:
    return _underscore_redirect(request, ident, "work_view")


def fileset_underscore_view(request: HttpRequest, ident: str) -> HttpResponse:
    return _underscore_redirect(request, ident, "fileset_view")


def editgroup_underscore_view(request: HttpRequest, ident: str) -> HttpResponse:
    return HttpResponse("editgroups are not supported", status=410)


def editor_underscore_view(request: HttpRequest, ident: str) -> HttpResponse:
    return HttpResponse("editor profiles are not supported", status=410)


def editgroup_view(request: HttpRequest, **kwargs) -> HttpResponse:
    return HttpResponse("editgroups are not supported", status=410)


def editor_view(request: HttpRequest, **kwargs) -> HttpResponse:
    return HttpResponse("editor profiles are not supported", status=410)

# -- Release export formats --------------------------------------------------

def _release_csl_data(ident: str):
    """Shared helper to load release data needed for CSL rendering."""
    release_uuid = resolve_ident(ident)
    release = release_svc.get(release_uuid)
    contribs = release_svc.get_contribs(release_uuid)
    extids = release_svc.get_extids(release_uuid)
    container = release.container
    return release, contribs, extids, container


def release_bibtex(request: HttpRequest, ident: str) -> HttpResponse:
    try:
        release, contribs, extids, container = _release_csl_data(ident)
    except EntityNotFound:
        raise Http404(f"release not found: {ident}")
    except ValueError as e:
        return HttpResponse(str(e), status=400, content_type="text/plain")

    try:
        csl = csl_mod.release_to_csl(release, contribs, extids, container)
    except ValueError as e:
        return HttpResponse(str(e), status=400, content_type="text/plain")

    bibtex = csl_mod.citeproc_csl(csl, "bibtex")
    return HttpResponse(bibtex, content_type="text/plain")


def release_citeproc(request: HttpRequest, ident: str) -> HttpResponse:
    style = request.GET.get("style", "harvard1")

    try:
        release, contribs, extids, container = _release_csl_data(ident)
    except EntityNotFound:
        raise Http404(f"release not found: {ident}")
    except ValueError as e:
        return HttpResponse(str(e), status=400, content_type="text/plain")

    try:
        csl = csl_mod.release_to_csl(release, contribs, extids, container)
    except ValueError as e:
        return HttpResponse(str(e), status=400, content_type="text/plain")

    try:
        cite = csl_mod.citeproc_csl(csl, style)
    except Exception as e:
        return HttpResponse(str(e), status=400, content_type="text/plain")

    if style == "csl-json":
        return HttpResponse(cite, content_type="application/json")
    return HttpResponse(cite, content_type="text/plain")


# -- References (HTML) -------------------------------------------------------

# dropped; part of refcat but only ever linked to in guide.
# openlibrary_view_refs_inbound = _stub
# wikipedia_view_refs_outbound = _stub

# -- References (JSON, CORS) -------------------------------------------------

# dropped. unclear these were used by anyone.
# release_view_refs_outbound_json = _stub
# release_view_refs_inbound_json = _stub
# openlibrary_view_refs_inbound_json = _stub
# wikipedia_view_refs_outbound_json = _stub

# -- Reference match --------------------------------------------------

# dropped. this was exposed at /reference/match but not linked from anywhere.
# it depends on grobid access and has been broken in prod for some time.
# reference_match = _stub
# reference_match_json = _stub

# -- Stats / changelog -------------------------------------------------------

_CHANGELOG_ENTITY_LABELS = {
    "releases": "Releases",
    "containers": "Containers",
    "creators": "Creators",
    "files": "Files",
    "works": "Works",
    "filesets": "Filesets",
    "webcaptures": "Webcaptures",
}

_CHANGELOG_VIEW_NAMES = {
    "releases": "release_view",
    "containers": "container_view",
    "creators": "creator_view",
    "files": "file_view",
    "works": "work_view",
    "filesets": "fileset_view",
    "webcaptures": "webcapture_view",
}


_CHANGELOG_PAGE_SIZE = 50


def _format_cursor(entity) -> str:
    """Encode a row's keyset position for use in a pagination link."""
    return f"{entity.updated.isoformat()}_{entity.id}"


def _parse_cursor(raw: str) -> tuple[datetime.datetime, uuid.UUID] | None:
    """Decode a keyset cursor, or None if malformed (caller falls back to page 1)."""
    try:
        updated_str, id_str = raw.rsplit("_", 1)
        return datetime.datetime.fromisoformat(updated_str), uuid.UUID(id_str)
    except (ValueError, AttributeError):
        return None


def changelog_view(request: HttpRequest) -> HttpResponse:
    entity_type = request.GET.get("entity_type", "releases")
    if entity_type not in _CHANGELOG_ENTITY_LABELS:
        entity_type = "releases"

    date_str = request.GET.get("date")
    if date_str:
        try:
            start_date = datetime.date.fromisoformat(date_str)
        except ValueError:
            start_date = datetime.date.today()
    else:
        start_date = datetime.date.today()

    source = request.GET.get("source", "").strip() or None

    # Keyset pagination: at most one of newer_than/older_than is set, carrying
    # the boundary row's (updated, id). A malformed cursor falls back to page 1.
    cursor = None
    direction = "older"
    newer_than = request.GET.get("newer_than")
    older_than = request.GET.get("older_than")
    if newer_than:
        cursor = _parse_cursor(newer_than)
        direction = "newer"
    elif older_than:
        cursor = _parse_cursor(older_than)
        direction = "older"
    if cursor is None:
        direction = "older"

    entries, has_more = changelog_svc.recent_page(
        entity_type, start_date, limit=_CHANGELOG_PAGE_SIZE, source=source,
        cursor=cursor, direction=direction,
    )

    # On the first page there is nothing newer; otherwise the page we came from
    # proves the opposite direction exists, and has_more covers the travelled one.
    if cursor is None:
        show_newer, show_older = False, has_more
    elif direction == "older":
        show_newer, show_older = True, has_more
    else:
        show_newer, show_older = has_more, True

    return _render(request, "fcweb/changelog.html", {
        "entity_type": entity_type,
        "entity_labels": _CHANGELOG_ENTITY_LABELS,
        "view_name": _CHANGELOG_VIEW_NAMES[entity_type],
        "date": start_date,
        "prev_date": start_date - datetime.timedelta(days=1),
        "next_date": start_date + datetime.timedelta(days=1),
        "today": datetime.date.today(),
        "entries": entries,
        "source_filter": source,
        "show_newer": show_newer,
        "show_older": show_older,
        "newer_cursor": _format_cursor(entries[0]) if entries else None,
        "older_cursor": _format_cursor(entries[-1]) if entries else None,
    })


# dropped
# changelog_entry_view = _stub


def stats_page(request: HttpRequest) -> HttpResponse:
    stats = fc_search.get_entity_stats() or {}
    return _render(request, "fcweb/stats.html", {"stats": stats})


def stats_json(request: HttpRequest) -> HttpResponse:
    return HttpResponseRedirect("/api/fatcat/v2/stats", status=301)


def container_ident_stats(request: HttpRequest, ident: str) -> HttpResponse:
    try:
        container_uuid = resolve_ident(ident)
    except ValueError:
        return HttpResponse(f"bad id: {ident}", status=400)
    return HttpResponseRedirect(
        f"/api/fatcat/v2/container/{container_uuid}/stats", status=301)


def container_ident_preservation_by_year(request: HttpRequest, ident: str) -> HttpResponse:
    try:
        container_uuid = resolve_ident(ident)
    except ValueError:
        return HttpResponse(f"bad id: {ident}", status=400)
    return HttpResponseRedirect(
        f"/api/fatcat/v2/container/{container_uuid}/preservation_by_year", status=301)


def container_ident_preservation_by_volume(request: HttpRequest, ident: str) -> HttpResponse:
    try:
        container_uuid = resolve_ident(ident)
    except ValueError:
        return HttpResponse(f"bad id: {ident}", status=400)
    return HttpResponseRedirect(
        f"/api/fatcat/v2/container/{container_uuid}/preservation_by_volume", status=301)


def container_ident_preservation_by_type(request: HttpRequest, ident: str) -> HttpResponse:
    try:
        container_uuid = resolve_ident(ident)
    except ValueError:
        return HttpResponse(f"bad id: {ident}", status=400)
    return HttpResponseRedirect(
        f"/api/fatcat/v2/container/{container_uuid}/preservation_by_type", status=301)

# -- Coverage ----------------------------------------------------------------


def coverage_search(request: HttpRequest) -> HttpResponse:
    q = request.GET.get("q")

    if not q:
        return _render(request, "fcweb/coverage_search.html", {
            "q": "",
        })

    es_error = None
    coverage_stats = None
    coverage_type_preservation = None
    year_histogram_svg = None
    date_histogram_svg = None

    try:
        coverage_stats = fc_search.get_coverage_stats(q)
    except Exception as e:
        es_error = str(e)

    if coverage_stats and coverage_stats["total"] > 1:
        coverage_type_preservation = fc_search.get_coverage_preservation_by_type(
            q,
        )
        year_data = fc_search.get_coverage_preservation_by_year(q)
        if year_data:
            year_histogram_svg = graphics.preservation_by_year_histogram(year_data)

    return _render(request, "fcweb/coverage_search.html", {
        "q": q,
        "es_error": es_error,
        "coverage_stats": coverage_stats,
        "coverage_type_preservation": coverage_type_preservation,
        "year_histogram_svg": year_histogram_svg,
        "date_histogram_svg": date_histogram_svg,
    })

# -- Static pages ------------------------------------------------------------


def page_about(request: HttpRequest) -> HttpResponse:
    return _render(request, "fcweb/about.html")


def page_guide(request: HttpRequest) -> HttpResponse:
    return _render(request, "fcweb/guide.html")

# -- Revision views (redirect to entity detail page) -------------------------

_REVISION_ENTITY_MAP = {
    "container": (container_svc.get_by_legacy_rev, "container_view"),
    "creator": (creator_svc.get_by_legacy_rev, "creator_view"),
    "file": (file_svc.get_by_legacy_rev, "file_view"),
    "fileset": (fileset_svc.get_by_legacy_rev, "fileset_view"),
    "webcapture": (webcapture_svc.get_by_legacy_rev, "webcapture_view"),
    "release": (release_svc.get_by_legacy_rev, "release_view"),
    "work": (work_svc.get_by_legacy_rev, "work_view"),
}


def _revision_redirect(request: HttpRequest, rev_id: str, entity_type: str) -> HttpResponse:
    get_by_legacy_rev, view_name = _REVISION_ENTITY_MAP[entity_type]
    try:
        rev_uuid = uuid.UUID(rev_id)
    except ValueError:
        raise Http404(f"invalid revision id: {rev_id}")
    try:
        entity = get_by_legacy_rev(rev_uuid)
    except EntityNotFound:
        raise Http404(f"no {entity_type} with revision {rev_id}")
    return HttpResponseRedirect(
        reverse(f"fcweb:{view_name}", kwargs={"ident": str(entity.id)}))


def container_revision_view(request, rev_id):
    return _revision_redirect(request, rev_id, "container")


def creator_revision_view(request, rev_id):
    return _revision_redirect(request, rev_id, "creator")


def file_revision_view(request, rev_id):
    return _revision_redirect(request, rev_id, "file")


def fileset_revision_view(request, rev_id):
    return _revision_redirect(request, rev_id, "fileset")


def webcapture_revision_view(request, rev_id):
    return _revision_redirect(request, rev_id, "webcapture")


def release_revision_view(request, rev_id):
    return _revision_redirect(request, rev_id, "release")


def work_revision_view(request, rev_id):
    return _revision_redirect(request, rev_id, "work")


# -- Entity history (410 Gone) -----------------------------------------------

def entity_history_view(request: HttpRequest, **kwargs) -> HttpResponse:
    return HttpResponse("entity history is not supported", status=410)
