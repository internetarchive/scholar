"""Stub views for the fatcat web UI.

Each view corresponds to a route from fatcat-scholar's src/scholar/fatcat/web.py.
They will be implemented incrementally.
"""

from django.http import HttpRequest, HttpResponse, Http404
from django.template import engines

from djscholar.fcapi.fcid import fcid2uuid, uuid2fcid, resolve_ident
from djscholar.fcapi.services import EntityNotFound
from djscholar.fcapi.services import releases as release_svc
from djscholar.fcapi.services import files as file_svc


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

    return _render(request, "fcweb/release_view.html", {
        "release": release,
        "authors": authors,
        "contribs": contribs,
        "extids": extids,
        "abstracts": abstracts,
        "files": files,
        "webcaptures": webcaptures,
        "container": container,
        "ident": ident,
        "uuid2fcid": uuid2fcid,
    })


release_view_metadata = _stub
release_view_contribs = _stub
release_view_references = _stub
release_save = _stub

container_view = _stub
container_view_metadata = _stub
container_view_browse = _stub
container_view_search = _stub
container_view_coverage = _stub

creator_view = _stub
creator_view_metadata = _stub

file_view = _stub
file_view_metadata = _stub

fileset_view = _stub
fileset_view_metadata = _stub

webcapture_view = _stub
webcapture_view_metadata = _stub

work_view = _stub
work_view_metadata = _stub

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
