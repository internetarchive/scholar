from djscholar.ftsearch.views import (
    DEFAULT_ACCESS_FILTER,
    DEFAULT_DATE_FILTER,
    DEFAULT_PAGE_SIZE,
    DEFAULT_SORT,
    DEFAULT_TYPE_FILTER,
    SORT_OPTIONS,
    _build_result,
    _get_access_options,
    _rewrite_id_query,
    _build_es_body,
)


def _defaults(**overrides):
    kwargs = {
        "q": "test",
        "offset": 0,
        "page_size": DEFAULT_PAGE_SIZE,
        "date_filter": DEFAULT_DATE_FILTER,
        "type_filter": DEFAULT_TYPE_FILTER,
        "access_filter": DEFAULT_ACCESS_FILTER,
        "sort": DEFAULT_SORT,
    }
    kwargs.update(overrides)
    return kwargs


def _get_filters(body):
    """Extract the filter list from a query body, or empty list if none."""
    query = body["query"]
    if "bool" in query and "filter" in query["bool"]:
        f = query["bool"]["filter"]
        return f if isinstance(f, list) else [f]
    return []


def _get_boosting(body):
    """Extract the boosting clause from the query body."""
    query = body["query"]
    if "boosting" in query:
        return query["boosting"]
    if "bool" in query:
        must = query["bool"]["must"]
        if "boosting" in must:
            return must["boosting"]
    return None


class TestQueryStringConfig:
    def test_has_field_boosting(self):
        body = _build_es_body(**_defaults())
        boosting = _get_boosting(body)
        qs = boosting["positive"]["query_string"]
        assert qs["fields"] == ["title^4", "biblio_all^3", "everything"]

    def test_has_quote_field_suffix(self):
        body = _build_es_body(**_defaults())
        boosting = _get_boosting(body)
        qs = boosting["positive"]["query_string"]
        assert qs["quote_field_suffix"] == ".exact"

    def test_default_operator_and(self):
        body = _build_es_body(**_defaults())
        boosting = _get_boosting(body)
        qs = boosting["positive"]["query_string"]
        assert qs["default_operator"] == "AND"

    def test_query_text_passed_through(self):
        body = _build_es_body(**_defaults(q="bovine tuberculosis"))
        boosting = _get_boosting(body)
        qs = boosting["positive"]["query_string"]
        assert qs["query"] == "bovine tuberculosis"


class TestPoorMetadataDemotion:
    def test_boosting_present(self):
        body = _build_es_body(**_defaults())
        boosting = _get_boosting(body)
        assert boosting is not None

    def test_negative_boost_value(self):
        body = _build_es_body(**_defaults())
        boosting = _get_boosting(body)
        assert boosting["negative_boost"] == 0.5

    def test_negative_checks_missing_fields(self):
        body = _build_es_body(**_defaults())
        boosting = _get_boosting(body)
        negative = boosting["negative"]
        should_clauses = negative["bool"]["should"]
        checked_fields = set()
        for clause in should_clauses:
            field = clause["bool"]["must_not"]["exists"]["field"]
            checked_fields.add(field)
        assert checked_fields == {"year", "type", "stage", "biblio.container_name"}


class TestTypeFilter:
    def test_default_papers(self):
        body = _build_es_body(**_defaults())
        filters = _get_filters(body)
        type_filters = [f for f in filters if "terms" in f and "biblio.release_type" in f["terms"]]
        assert len(type_filters) == 1
        assert set(type_filters[0]["terms"]["biblio.release_type"]) == {
            "article-journal", "paper-conference", "chapter", "article"
        }

    def test_reports(self):
        body = _build_es_body(**_defaults(type_filter="reports"))
        filters = _get_filters(body)
        type_filters = [f for f in filters if "terms" in f and "biblio.release_type" in f["terms"]]
        assert type_filters[0]["terms"]["biblio.release_type"] == ["report", "standard"]

    def test_datasets(self):
        body = _build_es_body(**_defaults(type_filter="datasets"))
        filters = _get_filters(body)
        type_filters = [f for f in filters if "terms" in f and "biblio.release_type" in f["terms"]]
        assert type_filters[0]["terms"]["biblio.release_type"] == ["dataset", "software"]

    def test_everything_no_type_filter(self):
        body = _build_es_body(**_defaults(type_filter="everything"))
        filters = _get_filters(body)
        type_filters = [f for f in filters if "terms" in f and "biblio.release_type" in f.get("terms", {})]
        assert len(type_filters) == 0


class TestAccessFilter:
    def test_default_fulltext(self):
        body = _build_es_body(**_defaults())
        filters = _get_filters(body)
        access_filters = [f for f in filters if "terms" in f and "access.access_type" in f["terms"]]
        assert len(access_filters) == 1
        assert set(access_filters[0]["terms"]["access.access_type"]) == {"wayback", "ia_file", "ia_sim"}

    def test_microfilm(self):
        body = _build_es_body(**_defaults(access_filter="microfilm"))
        filters = _get_filters(body)
        access_filters = [f for f in filters if "term" in f and "access.access_type" in f["term"]]
        assert access_filters[0]["term"]["access.access_type"] == "ia_sim"

    def test_oa(self):
        body = _build_es_body(**_defaults(access_filter="oa"))
        filters = _get_filters(body)
        oa_filters = [f for f in filters if "term" in f and "tags" in f["term"]]
        assert len(oa_filters) == 1
        assert oa_filters[0]["term"]["tags"] == "oa"

    def test_everything_no_access_filter(self):
        body = _build_es_body(**_defaults(access_filter="everything"))
        filters = _get_filters(body)
        access_filters = [
            f for f in filters
            if ("terms" in f and "access.access_type" in f["terms"])
            or ("term" in f and "access.access_type" in f["term"])
            or ("term" in f and "tags" in f["term"])
        ]
        assert len(access_filters) == 0

    def test_non_fulltext_boosts_fulltext_access(self):
        body = _build_es_body(**_defaults(access_filter="oa"))
        boosting = _get_boosting(body)
        positive = boosting["positive"]
        assert "bool" in positive
        assert "should" in positive["bool"]
        should = positive["bool"]["should"]
        access_types = should[0]["terms"]["access_type"]
        assert set(access_types) == {"ia_sim", "ia_file", "wayback"}

    def test_fulltext_no_should_boost(self):
        body = _build_es_body(**_defaults(access_filter="fulltext"))
        boosting = _get_boosting(body)
        positive = boosting["positive"]
        assert "query_string" in positive


class TestDateFilter:
    def test_all_time_no_date_filter(self):
        body = _build_es_body(**_defaults(date_filter="all_time"))
        filters = _get_filters(body)
        date_filters = [f for f in filters if "range" in f and "biblio.release_date" in f["range"]]
        assert len(date_filters) == 0

    def test_past_week(self):
        body = _build_es_body(**_defaults(date_filter="past_week"))
        filters = _get_filters(body)
        date_filters = [f for f in filters if "range" in f and "biblio.release_date" in f["range"]]
        assert len(date_filters) == 1
        assert date_filters[0]["range"]["biblio.release_date"]["gte"] == "now-1w"

    def test_before_1931(self):
        body = _build_es_body(**_defaults(date_filter="before_1931"))
        filters = _get_filters(body)
        date_filters = [f for f in filters if "range" in f and "biblio.release_date" in f["range"]]
        assert date_filters[0]["range"]["biblio.release_date"]["lt"] == "1931-01-01"

    def test_since_2000(self):
        body = _build_es_body(**_defaults(date_filter="since_2000"))
        filters = _get_filters(body)
        date_filters = [f for f in filters if "range" in f and "biblio.release_date" in f["range"]]
        assert date_filters[0]["range"]["biblio.release_date"]["gte"] == "2000-01-01"


class TestCollapse:
    def test_collapse_on_collapse_key(self):
        body = _build_es_body(**_defaults())
        assert body["collapse"]["field"] == "collapse_key"

    def test_inner_hits_present(self):
        body = _build_es_body(**_defaults())
        assert body["collapse"]["inner_hits"]["name"] == "more_pages"
        assert body["collapse"]["inner_hits"]["size"] == 0


class TestPaginationAndHighlight:
    def test_offset_and_size(self):
        body = _build_es_body(**_defaults(offset=50, page_size=10))
        assert body["from"] == 50
        assert body["size"] == 10

    def test_highlight_fields(self):
        body = _build_es_body(**_defaults())
        fields = body["highlight"]["fields"]
        assert "abstracts.body" in fields
        assert "fulltext.body" in fields
        assert "fulltext.acknowledgement" in fields
        assert "fulltext.annex" in fields

    def test_highlight_does_not_require_field_match(self):
        body = _build_es_body(**_defaults())
        assert body["highlight"]["require_field_match"] is False

    def test_highlight_uses_simplified_query(self):
        body = _build_es_body(**_defaults(q="bovine tuberculosis"))
        hq = body["highlight"]["highlight_query"]["query_string"]
        assert hq["query"] == "bovine tuberculosis"
        assert "fields" not in hq


class TestSort:
    def test_default_is_relevancy(self):
        assert DEFAULT_SORT == "relevancy"

    def test_relevancy_no_sort_clause(self):
        body = _build_es_body(**_defaults())
        assert "sort" not in body

    def test_newest(self):
        body = _build_es_body(**_defaults(sort="newest"))
        assert body["sort"] == [{"biblio.release_date": {"order": "desc", "missing": "_last"}}]

    def test_oldest(self):
        body = _build_es_body(**_defaults(sort="oldest"))
        assert body["sort"] == [{"biblio.release_date": {"order": "asc", "missing": "_last"}}]

    def test_relevancy_explicit(self):
        body = _build_es_body(**_defaults(sort="relevancy"))
        assert "sort" not in body

    def test_sort_options_match_defaults(self):
        assert SORT_OPTIONS["relevancy"] is None


class TestNoFiltersEverything:
    def test_no_filter_clause_when_all_everything(self):
        body = _build_es_body(**_defaults(
            date_filter="all_time",
            type_filter="everything",
            access_filter="everything",
        ))
        # No bool wrapper with filters — just the boosting query directly
        assert "boosting" in body["query"]
        assert "filter" not in body["query"].get("bool", {})


class TestGetAccessOptions:
    def test_empty_source(self):
        assert _get_access_options({}) == []

    def test_fulltext_only(self):
        source = {
            "fulltext": {
                "access_type": "wayback",
                "access_url": "https://web.archive.org/web/20200101000000/https://example.com/paper.pdf",
            },
        }
        opts = _get_access_options(source)
        assert len(opts) == 1
        assert opts[0]["access_type"] == "wayback"

    def test_access_list_only(self):
        source = {
            "access": [
                {"access_type": "ia_file", "access_url": "https://archive.org/download/item/file.pdf"},
                {"access_type": "wayback", "access_url": "https://web.archive.org/web/20200101000000/https://example.com/a.pdf"},
            ],
        }
        opts = _get_access_options(source)
        assert len(opts) == 2
        assert opts[0]["access_type"] == "ia_file"
        assert opts[1]["access_type"] == "wayback"

    def test_fulltext_and_access_combined(self):
        source = {
            "fulltext": {
                "access_type": "wayback",
                "access_url": "https://web.archive.org/web/20200101000000/https://example.com/paper.pdf",
            },
            "access": [
                {"access_type": "ia_file", "access_url": "https://archive.org/download/item/file.pdf"},
            ],
        }
        opts = _get_access_options(source)
        assert len(opts) == 2
        # fulltext comes first
        assert opts[0]["access_type"] == "wayback"
        assert opts[1]["access_type"] == "ia_file"

    def test_fulltext_missing_access_type_skipped(self):
        source = {
            "fulltext": {"access_url": "https://example.com/paper.pdf"},
        }
        assert _get_access_options(source) == []

    def test_fulltext_missing_access_url_skipped(self):
        source = {
            "fulltext": {"access_type": "wayback"},
        }
        assert _get_access_options(source) == []

    def test_access_entry_missing_fields_skipped(self):
        source = {
            "access": [
                {"access_type": "wayback"},
                {"access_url": "https://example.com"},
                {"access_type": "ia_file", "access_url": "https://archive.org/download/item/file.pdf"},
            ],
        }
        opts = _get_access_options(source)
        assert len(opts) == 1
        assert opts[0]["access_type"] == "ia_file"

    def test_null_fulltext(self):
        source = {"fulltext": None, "access": []}
        assert _get_access_options(source) == []

    def test_null_access(self):
        source = {"fulltext": {}, "access": None}
        assert _get_access_options(source) == []


class TestRewriteIdQuery:
    # DOI
    def test_doi(self):
        assert _rewrite_id_query("10.1234/foo.bar") == 'doi:"10.1234/foo.bar"'

    def test_doi_long_prefix(self):
        assert _rewrite_id_query("10.12345/some/path") == 'doi:"10.12345/some/path"'

    def test_doi_url_https(self):
        assert _rewrite_id_query("https://doi.org/10.1234/foo") == 'doi:"10.1234/foo"'

    def test_doi_url_dx(self):
        assert _rewrite_id_query("https://dx.doi.org/10.1234/foo") == 'doi:"10.1234/foo"'

    def test_doi_url_http(self):
        assert _rewrite_id_query("http://doi.org/10.1234/foo") == 'doi:"10.1234/foo"'

    # PMCID
    def test_pmcid(self):
        assert _rewrite_id_query("PMC12345") == 'pmcid:"PMC12345"'

    def test_pmcid_lowercase(self):
        assert _rewrite_id_query("pmc12345") == 'pmcid:"pmc12345"'

    # arXiv new format
    def test_arxiv_new(self):
        assert _rewrite_id_query("2301.12345") == 'arxiv_id:"2301.12345"'

    def test_arxiv_new_with_version(self):
        assert _rewrite_id_query("2301.12345v2") == 'arxiv_id:"2301.12345v2"'

    def test_arxiv_short_id(self):
        assert _rewrite_id_query("0712.0473") == 'arxiv_id:"0712.0473"'

    # arXiv old format
    def test_arxiv_old(self):
        assert _rewrite_id_query("hep-ph/0301001") == 'arxiv_id:"hep-ph/0301001"'

    # Passthrough — regular queries should not be rewritten
    def test_plain_query(self):
        assert _rewrite_id_query("machine learning") == "machine learning"

    def test_number_not_rewritten(self):
        assert _rewrite_id_query("12345678") == "12345678"

    def test_existing_field_query(self):
        assert _rewrite_id_query('doi:"10.1234/foo"') == 'doi:"10.1234/foo"'


def _make_hit(biblio=None, fulltext=None, access=None, highlight=None, work_ident="abc123"):
    """Build a minimal ES hit dict for _build_result tests."""
    source = {"work_ident": work_ident, "biblio": biblio or {}}
    if fulltext is not None:
        source["fulltext"] = fulltext
    if access is not None:
        source["access"] = access
    hit = {"_source": source}
    if highlight is not None:
        hit["highlight"] = highlight
    return hit


class TestBuildResultBasic:
    def test_minimal_hit(self):
        result = _build_result(_make_hit())
        assert result["title"] == "(untitled)"
        assert result["authors"] == []
        assert result["year"] is None
        assert result["journal"] == ""
        assert result["work_ident"] == "abc123"
        assert result["ext_ids"] == []
        assert result["highlights"] == []

    def test_title_and_authors(self):
        result = _build_result(_make_hit(biblio={
            "title": "On Foo",
            "contrib_names": ["Alice", "Bob"],
            "release_year": 2023,
            "container_name": "Nature",
        }))
        assert result["title"] == "On Foo"
        assert result["authors"] == ["Alice", "Bob"]
        assert result["year"] == 2023
        assert result["journal"] == "Nature"

    def test_ext_ids_collected(self):
        result = _build_result(_make_hit(biblio={
            "doi": "10.1234/x",
            "pmid": "99",
            "arxiv_id": "2301.00001",
        }))
        assert "doi:10.1234/x" in result["ext_ids"]
        assert "pmid:99" in result["ext_ids"]
        assert "arxiv:2301.00001" in result["ext_ids"]

    def test_ext_ids_skip_missing(self):
        result = _build_result(_make_hit(biblio={"doi": "10.1234/x"}))
        assert len(result["ext_ids"]) == 1

    def test_release_stage(self):
        result = _build_result(_make_hit(biblio={"release_stage": "submitted"}))
        assert result["release_stage"] == "submitted"

    def test_fatcat_url(self):
        result = _build_result(_make_hit(biblio={"release_ident": "r1234"}))
        assert result["fatcat_url"] == "https://scholar.archive.org/fatcat/release/r1234"

    def test_fatcat_url_missing_ident(self):
        result = _build_result(_make_hit(biblio={}))
        assert result["fatcat_url"] == ""


class TestBuildResultSizeFormatting:
    def test_megabytes(self):
        result = _build_result(_make_hit(fulltext={"size_bytes": 2_500_000}))
        assert result["access_size"] == "2.5 MB"

    def test_kilobytes(self):
        result = _build_result(_make_hit(fulltext={"size_bytes": 45_000}))
        assert result["access_size"] == "45 kB"

    def test_small_bytes(self):
        result = _build_result(_make_hit(fulltext={"size_bytes": 500}))
        assert result["access_size"] == ""

    def test_no_size(self):
        result = _build_result(_make_hit(fulltext={}))
        assert result["access_size"] == ""

    def test_exactly_one_mb(self):
        result = _build_result(_make_hit(fulltext={"size_bytes": 1_000_000}))
        assert result["access_size"] == "1.0 MB"


class TestBuildResultCaptureYear:
    def test_wayback_url(self):
        result = _build_result(_make_hit(fulltext={
            "access_url": "https://web.archive.org/web/20210315120000/https://example.com/paper.pdf",
        }))
        assert result["capture_year"] == "2021"

    def test_non_wayback_url(self):
        result = _build_result(_make_hit(fulltext={
            "access_url": "https://archive.org/download/item/file.pdf",
        }))
        assert result["capture_year"] == ""

    def test_no_access_url(self):
        result = _build_result(_make_hit(fulltext={}))
        assert result["capture_year"] == ""


class TestBuildResultThumbnail:
    def test_thumbnail_rewrite(self):
        result = _build_result(_make_hit(fulltext={
            "thumbnail_url": "https://blobs.fatcat.wiki/thumb/abc.png",
        }))
        assert result["thumbnail_url"] == "https://scholar.archive.org/_s3/thumb/abc.png"

    def test_non_fatcat_thumbnail_unchanged(self):
        result = _build_result(_make_hit(fulltext={
            "thumbnail_url": "https://other.example.com/thumb.png",
        }))
        assert result["thumbnail_url"] == "https://other.example.com/thumb.png"

    def test_no_thumbnail(self):
        result = _build_result(_make_hit(fulltext={}))
        assert result["thumbnail_url"] == ""


class TestBuildResultHighlights:
    def test_snippets_collected(self):
        result = _build_result(_make_hit(highlight={
            "abstracts.body": ["fragment <em>one</em>"],
            "fulltext.body": ["fragment <em>two</em>"],
        }))
        assert len(result["highlights"]) == 2
        assert "<strong>one</strong>" in str(result["highlights"][0])
        assert "<strong>two</strong>" in str(result["highlights"][1])

    def test_html_in_snippet_escaped(self):
        result = _build_result(_make_hit(highlight={
            "fulltext.body": ["<script>alert(1)</script> <em>match</em>"],
        }))
        snippet = str(result["highlights"][0])
        assert "<script>" not in snippet
        assert "&lt;script&gt;" in snippet
        assert "<strong>match</strong>" in snippet

    def test_no_highlights(self):
        result = _build_result(_make_hit())
        assert result["highlights"] == []
