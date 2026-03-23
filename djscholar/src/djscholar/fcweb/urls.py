"""
URL configuration for the fcweb (fatcat web UI) app.

These routes are ported from fatcat-scholar's src/scholar/fatcat/web.py.
All view functions are stubs (in views.py) to be implemented incrementally.
"""

from django.urls import path

from djscholar.fcweb import views

app_name = "fcweb"

urlpatterns = [
    # -- Index & search -------------------------------------------------------
    path("", views.index, name="index"),
    path("search", views.search, name="search"),
    path("release/search", views.release_search, name="release_search"),
    path("container/search", views.container_search, name="container_search"),

    # -- Lookups --------------------------------------------------------------
    path("creator/lookup", views.creator_lookup, name="creator_lookup"),
    path("file/lookup", views.file_lookup, name="file_lookup"),
    path("container/lookup", views.container_lookup, name="container_lookup"),
    path("release/lookup", views.release_lookup, name="release_lookup"),

    # -- Release export formats -----------------------------------------------
    path("release/<str:ident>.bib", views.release_bibtex, name="release_bibtex"),
    path("release/<str:ident>/citeproc", views.release_citeproc, name="release_citeproc"),

    # -- Release save ---------------------------------------------------------
    path("release/<str:ident>/save", views.release_save, name="release_save"),

    # -- Container sub-views --------------------------------------------------
    path("container/<str:ident>/browse", views.container_view_browse, name="container_view_browse"),
    path("container/<str:ident>/search", views.container_view_search, name="container_view_search"),
    path("container/<str:ident>/coverage",
         views.container_view_coverage, name="container_view_coverage"),
    path("container/<str:ident>/metadata",
         views.container_view_metadata, name="container_view_metadata"),
    path("container/<str:ident>/stats.json",
         views.container_ident_stats, name="container_ident_stats"),
    path("container/<str:ident>/preservation_by_year.json",
         views.container_ident_preservation_by_year, name="container_ident_preservation_by_year"),
    path("container/<str:ident>/preservation_by_volume.json",
         views.container_ident_preservation_by_volume,
         name="container_ident_preservation_by_volume"),
    path("container/<str:ident>/preservation_by_type.json",
         views.container_ident_preservation_by_type, name="container_ident_preservation_by_type"),

    # -- Release sub-views ----------------------------------------------------
    path("release/<str:ident>/contribs", views.release_view_contribs, name="release_view_contribs"),
    path("release/<str:ident>/references",
         views.release_view_references, name="release_view_references"),
    path("release/<str:ident>/metadata", views.release_view_metadata, name="release_view_metadata"),

    # -- References (HTML) ----------------------------------------------------
    path("release/<str:ident>/refs-in",
         views.release_view_refs_inbound, name="release_view_refs_inbound"),
    path("release/<str:ident>/refs-out",
         views.release_view_refs_outbound, name="release_view_refs_outbound"),
    path("openlibrary/OL<int:id_num>W/refs-in",
         views.openlibrary_view_refs_inbound, name="openlibrary_view_refs_inbound"),
    path("wikipedia/<str:wiki_lang>:<str:wiki_article>/refs-out",
         views.wikipedia_view_refs_outbound, name="wikipedia_view_refs_outbound"),

    # -- References (JSON, CORS) ----------------------------------------------
    path("release/<str:ident>/refs-out.json",
         views.release_view_refs_outbound_json, name="release_view_refs_outbound_json"),
    path("release/<str:ident>/refs-in.json",
         views.release_view_refs_inbound_json, name="release_view_refs_inbound_json"),
    path("openlibrary/OL<int:id_num>W/refs-in.json",
         views.openlibrary_view_refs_inbound_json, name="openlibrary_view_refs_inbound_json"),
    path("wikipedia/<str:wiki_lang>:<str:wiki_article>/refs-out.json",
         views.wikipedia_view_refs_outbound_json, name="wikipedia_view_refs_outbound_json"),
    path("reference/match.json", views.reference_match_json, name="reference_match_json"),

    # -- Reference match (HTML) -----------------------------------------------
    path("reference/match", views.reference_match, name="reference_match"),

    # -- Other entity metadata views ------------------------------------------
    path("creator/<str:ident>/metadata", views.creator_view_metadata, name="creator_view_metadata"),
    path("file/<str:ident>/metadata", views.file_view_metadata, name="file_view_metadata"),
    path("fileset/<str:ident>/metadata", views.fileset_view_metadata, name="fileset_view_metadata"),
    path("webcapture/<str:ident>/metadata",
         views.webcapture_view_metadata, name="webcapture_view_metadata"),
    path("work/<str:ident>/metadata", views.work_view_metadata, name="work_view_metadata"),

    # -- Stats / changelog ----------------------------------------------------
    path("changelog", views.changelog_view, name="changelog_view"),
    path("changelog/<int:index>", views.changelog_entry_view, name="changelog_entry_view"),
    path("stats", views.stats_page, name="stats_page"),
    path("stats.json", views.stats_json, name="stats_json"),

    # -- Coverage -------------------------------------------------------------
    path("coverage/search", views.coverage_search, name="coverage_search"),

    # -- Static pages ---------------------------------------------------------
    path("about", views.page_about, name="page_about"),
    path("guide", views.page_guide, name="page_guide"),

    # -- Entity views (must come after more specific sub-paths) ---------------
    path("container/<str:ident>", views.container_view, name="container_view"),
    path("creator/<str:ident>", views.creator_view, name="creator_view"),
    path("file/<str:ident>", views.file_view, name="file_view"),
    path("fileset/<str:ident>", views.fileset_view, name="fileset_view"),
    path("webcapture/<str:ident>", views.webcapture_view, name="webcapture_view"),
    path("work/<str:ident>", views.work_view, name="work_view"),
    path("release/<str:ident>", views.release_view, name="release_view"),

    # -- Underscore redirects (legacy URLs) -----------------------------------
    path("container_<str:ident>", views.container_underscore_view, name="container_underscore_view"),
    path("file_<str:ident>", views.file_underscore_view, name="file_underscore_view"),
    path("creator_<str:ident>", views.creator_underscore_view, name="creator_underscore_view"),
    path("release_<str:ident>", views.release_underscore_view, name="release_underscore_view"),
    path("webcapture_<str:ident>", views.webcapture_underscore_view,
         name="webcapture_underscore_view"),
    path("work_<str:ident>", views.work_underscore_view, name="work_underscore_view"),
    path("fileset_<str:ident>", views.fileset_underscore_view, name="fileset_underscore_view"),
    path("editgroup_<str:ident>", views.editgroup_underscore_view, name="editgroup_underscore_view"),
    path("editor_<str:ident>", views.editor_underscore_view, name="editor_underscore_view"),

    # -- Editgroup entity views -----------------------------------------------
    # TODO this feature dropped; decide on whether redirect or 404
    # path("editgroup/<str:editgroup_id>/container/<str:ident>", views.container_editgroup_view,
    #      name="container_editgroup_view"),
    # path("editgroup/<str:editgroup_id>/container/<str:ident>/metadata",
    #      views.container_editgroup_view_metadata, name="container_editgroup_view_metadata"),
    # path("editgroup/<str:editgroup_id>/creator/<str:ident>", views.creator_editgroup_view,
    #      name="creator_editgroup_view"),
    # path("editgroup/<str:editgroup_id>/creator/<str:ident>/metadata",
    #      views.creator_editgroup_view_metadata, name="creator_editgroup_view_metadata"),
    # path("editgroup/<str:editgroup_id>/file/<str:ident>", views.file_editgroup_view,
    #      name="file_editgroup_view"),
    # path("editgroup/<str:editgroup_id>/file/<str:ident>/metadata",
    #      views.file_editgroup_view_metadata, name="file_editgroup_view_metadata"),
    # path("editgroup/<str:editgroup_id>/fileset/<str:ident>", views.fileset_editgroup_view,
    #      name="fileset_editgroup_view"),
    # path("editgroup/<str:editgroup_id>/fileset/<str:ident>/metadata",
    #      views.fileset_editgroup_view_metadata, name="fileset_editgroup_view_metadata"),
    # path("editgroup/<str:editgroup_id>/webcapture/<str:ident>", views.webcapture_editgroup_view,
    #      name="webcapture_editgroup_view"),
    # path("editgroup/<str:editgroup_id>/webcapture/<str:ident>/metadata",
    #      views.webcapture_editgroup_view_metadata, name="webcapture_editgroup_view_metadata"),
    # path("editgroup/<str:editgroup_id>/release/<str:ident>", views.release_editgroup_view,
    #      name="release_editgroup_view"),
    # path("editgroup/<str:editgroup_id>/release/<str:ident>/metadata",
    #      views.release_editgroup_view_metadata, name="release_editgroup_view_metadata"),
    # path("editgroup/<str:editgroup_id>/release/<str:ident>/contribs",
    #      views.release_editgroup_view_contribs, name="release_editgroup_view_contribs"),
    # path("editgroup/<str:editgroup_id>/release/<str:ident>/references",
    #      views.release_editgroup_view_references, name="release_editgroup_view_references"),
    # path("editgroup/<str:editgroup_id>/work/<str:ident>",
    #      views.work_editgroup_view, name="work_editgroup_view"),
    # path("editgroup/<str:editgroup_id>/work/<str:ident>/metadata",
    #      views.work_editgroup_view_metadata, name="work_editgroup_view_metadata"),

    # -- Editgroup / editor views ---------------------------------------------
    # TODO this feature dropped; decide on whether redirect or 404
    # path("editgroup/<str:ident>/diff", views.editgroup_diff_view, name="editgroup_diff_view"),
    # path("editgroup/<str:ident>", views.editgroup_view, name="editgroup_view"),
    # path("editor/<str:ident>/editgroups", views.editor_editgroups, name="editor_editgroups"),
    # path("editor/<str:ident>/annotations", views.editor_annotations, name="editor_annotations"),
    # path("editor/<str:ident>", views.editor_view, name="editor_view"),
    # path("u/<str:username>", views.editor_username_redirect, name="editor_username_redirect"),


    # -- Revision views -------------------------------------------------------
    # TODO: revisions are dropped; these should redirect to entity detail pages.
    # path("container/rev/<str:rev_id>", views.container_revision_view,
    #      name="container_revision_view"),
    # path("container/rev/<str:rev_id>/metadata", views.container_revision_view_metadata,
    #      name="container_revision_view_metadata"),
    # path("creator/rev/<str:rev_id>", views.creator_revision_view,
    #      name="creator_revision_view"),
    # path("creator/rev/<str:rev_id>/metadata", views.creator_revision_view_metadata,
    #      name="creator_revision_view_metadata"),
    # path("file/rev/<str:rev_id>", views.file_revision_view,
    #      name="file_revision_view"),
    # path("file/rev/<str:rev_id>/metadata", views.file_revision_view_metadata,
    #      name="file_revision_view_metadata"),
    # path("fileset/rev/<str:rev_id>", views.fileset_revision_view,
    #      name="fileset_revision_view"),
    # path("fileset/rev/<str:rev_id>/metadata", views.fileset_revision_view_metadata,
    #      name="fileset_revision_view_metadata"),
    # path("webcapture/rev/<str:rev_id>", views.webcapture_revision_view,
    #      name="webcapture_revision_view"),
    # path("webcapture/rev/<str:rev_id>/metadata", views.webcapture_revision_view_metadata,
    #      name="webcapture_revision_view_metadata"),
    # path("release/rev/<str:rev_id>", views.release_revision_view,
    #      name="release_revision_view"),
    # path("release/rev/<str:rev_id>/metadata", views.release_revision_view_metadata,
    #      name="release_revision_view_metadata"),
    # path("release/rev/<str:rev_id>/contribs", views.release_revision_view_contribs,
    #      name="release_revision_view_contribs"),
    # path("release/rev/<str:rev_id>/references", views.release_revision_view_references,
    #      name="release_revision_view_references"),
    # path("work/rev/<str:rev_id>", views.work_revision_view,
    #      name="work_revision_view"),
    # path("work/rev/<str:rev_id>/metadata", views.work_revision_view_metadata,
    #      name="work_revision_view_metadata"),

    # -- Entity history -------------------------------------------------------
    # TODO deprecated, decide on redirect or 404
    # path("container/<str:ident>/history", views.container_history, name="container_history"),
    # path("creator/<str:ident>/history", views.creator_history, name="creator_history"),
    # path("file/<str:ident>/history", views.file_history, name="file_history"),
    # path("fileset/<str:ident>/history", views.fileset_history, name="fileset_history"),
    # path("webcapture/<str:ident>/history", views.webcapture_history, name="webcapture_history"),
    # path("release/<str:ident>/history", views.release_history, name="release_history"),
    # path("work/<str:ident>/history", views.work_history, name="work_history"),

]
