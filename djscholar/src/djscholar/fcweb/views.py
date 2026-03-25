"""Stub views for the fatcat web UI.

Each view corresponds to a route from fatcat-scholar's src/scholar/fatcat/web.py.
They will be implemented incrementally.
"""

from django.db import models
from django.http import HttpRequest, HttpResponse, Http404
from django.template import engines

from djscholar.fcapi.fcid import resolve_ident
from djscholar.fcapi.services import EntityNotFound
from djscholar.fcapi.services import containers as container_svc
from djscholar.fcapi.services import creators as creator_svc
from djscholar.fcapi.services import files as file_svc
from djscholar.fcapi.services import filesets as fileset_svc
from djscholar.fcapi.services import releases as release_svc
from djscholar.fcapi.services import webcaptures as webcapture_svc
from djscholar.fcapi.services import works as work_svc


def _get_jinja_env():
    return engines["jinja2"]


def _render(request: HttpRequest, template_name: str, context: dict | None = None, status: int = 200) -> HttpResponse:
    env = _get_jinja_env()
    template = env.get_template(template_name)
    html = template.render(context or {})
    return HttpResponse(html, status=status)


def _stub(request: HttpRequest, **kwargs) -> HttpResponse:
    return HttpResponse("not yet implemented", status=501)


# -- Index & search ----------------------------------------------------------

def index(request: HttpRequest) -> HttpResponse:
    return _render(request, "fcweb/index.html")


search = _stub
release_search = _stub
container_search = _stub

# -- Lookups -----------------------------------------------------------------

creator_lookup = _stub
file_lookup = _stub
container_lookup = _stub
release_lookup = _stub

# -- Helpers -----------------------------------------------------------------


METADATA_SKIP_FIELDS = {"id", "extra", "legacy_rev", "source", "hidden_reason", "hidden_when"}


def _entity_schema_metadata(entity: models.Model) -> dict:
    """Extract model field values as an ordered dict for the metadata view.

    Skips internal fields (id, extra, legacy_rev, etc.) and None values.
    """
    result = {}
    for field in entity._meta.get_fields():
        if not hasattr(field, "attname"):
            # skip reverse relations, M2M, etc.
            continue
        name = field.attname
        if name in METADATA_SKIP_FIELDS:
            continue
        value = getattr(entity, name)
        if value is None:
            continue
        result[name] = value
    return result


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


# -- Entity views ------------------------------------------------------------


def release_view(request: HttpRequest, ident: str) -> HttpResponse:
    try:
        release_uuid = resolve_ident(ident)
        release = release_svc.get(release_uuid)
    except EntityNotFound:
        raise Http404(f"release not found: {ident}")
    except ValueError:
        return HttpResponse(f"bad id: {ident}", status=400)

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
        "metadata": _entity_schema_metadata(release),
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


release_view_references = _stub
release_save = _stub

def container_view(request: HttpRequest, ident: str) -> HttpResponse:
    try:
        container_uuid = resolve_ident(ident)
        container = container_svc.get(container_uuid)
    except EntityNotFound:
        raise Http404(f"container not found: {ident}")
    except ValueError:
        return HttpResponse(f"bad id: {ident}", status=400)

    release_count = container_svc.get_releases(container_uuid).count()

    return _render(request, "fcweb/container_view.html", {
        "container": container,
        "release_count": release_count,
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
        "metadata": _entity_schema_metadata(container),
        "extra": container.extra,
        "ident": str(container_uuid),
    })
container_view_browse = _stub
container_view_search = _stub
container_view_coverage = _stub

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
        "metadata": _entity_schema_metadata(creator),
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
        "metadata": _entity_schema_metadata(file),
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
        "metadata": _entity_schema_metadata(fileset),
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
        "metadata": _entity_schema_metadata(webcapture),
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
        "metadata": _entity_schema_metadata(work),
        "extra": work.extra,
        "ident": str(work_uuid),
    })

# -- Underscore redirects (legacy URLs) --------------------------------------
# TODO remember what these were for

container_underscore_view = _stub
file_underscore_view = _stub
creator_underscore_view = _stub
release_underscore_view = _stub
webcapture_underscore_view = _stub
work_underscore_view = _stub
fileset_underscore_view = _stub
editgroup_underscore_view = _stub
editor_underscore_view = _stub

# -- Release export formats --------------------------------------------------

release_bibtex = _stub
release_citeproc = _stub

# -- References (HTML) -------------------------------------------------------

release_view_refs_inbound = _stub
release_view_refs_outbound = _stub
openlibrary_view_refs_inbound = _stub
wikipedia_view_refs_outbound = _stub

# -- References (JSON, CORS) -------------------------------------------------

release_view_refs_outbound_json = _stub
release_view_refs_inbound_json = _stub
openlibrary_view_refs_inbound_json = _stub
wikipedia_view_refs_outbound_json = _stub
reference_match_json = _stub

# -- Reference match (HTML) --------------------------------------------------

reference_match = _stub

# -- Stats / changelog -------------------------------------------------------

changelog_view = _stub
changelog_entry_view = _stub
stats_page = _stub
stats_json = _stub
container_ident_stats = _stub
container_ident_preservation_by_year = _stub
container_ident_preservation_by_volume = _stub
container_ident_preservation_by_type = _stub

# -- Coverage ----------------------------------------------------------------

coverage_search = _stub

# -- Static pages ------------------------------------------------------------

page_about = _stub
page_guide = _stub

# -- Revision views ----------------------------------------------------------
# TODO these are no longer useful as we're dropping the notion of revisions;
# however, these should attempt to redirect to detail pages as we're able.
# container_revision_view = _stub
# container_revision_view_metadata = _stub
# creator_revision_view = _stub
# creator_revision_view_metadata = _stub
# file_revision_view = _stub
# file_revision_view_metadata = _stub
# fileset_revision_view = _stub
# fileset_revision_view_metadata = _stub
# webcapture_revision_view = _stub
# webcapture_revision_view_metadata = _stub
# release_revision_view = _stub
# release_revision_view_metadata = _stub
# release_revision_view_contribs = _stub
# release_revision_view_references = _stub
# work_revision_view = _stub
# work_revision_view_metadata = _stub

# -- Editgroup entity views --------------------------------------------------
# TODO the editgroup concept is also dropped. I'm not sure that there is a
# great way to redirect these -- they will likely just 404.
# container_editgroup_view = _stub
# container_editgroup_view_metadata = _stub
# creator_editgroup_view = _stub
# creator_editgroup_view_metadata = _stub
# file_editgroup_view = _stub
# file_editgroup_view_metadata = _stub
# fileset_editgroup_view = _stub
# fileset_editgroup_view_metadata = _stub
# webcapture_editgroup_view = _stub
# webcapture_editgroup_view_metadata = _stub
# release_editgroup_view = _stub
# release_editgroup_view_metadata = _stub
# release_editgroup_view_contribs = _stub
# release_editgroup_view_references = _stub
# work_editgroup_view = _stub
# work_editgroup_view_metadata = _stub

# -- Editgroup / editor views ------------------------------------------------
# TODO the editgroup concept is also dropped. I'm not sure that there is a
# great way to redirect these -- they will likely just 404.
# editgroup_view = _stub
# editgroup_diff_view = _stub
# editor_view = _stub
# editor_editgroups = _stub
# editor_annotations = _stub
# editor_username_redirect = _stub

# -- Entity history ----------------------------------------------------------
# TODO 404 or 301?
# container_history = _stub
# creator_history = _stub
# file_history = _stub
# fileset_history = _stub
# webcapture_history = _stub
# release_history = _stub
# work_history = _stub
