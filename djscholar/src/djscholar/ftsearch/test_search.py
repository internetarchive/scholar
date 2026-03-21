from djscholar.ftsearch.views import (
    DEFAULT_ACCESS_FILTER,
    DEFAULT_DATE_FILTER,
    DEFAULT_PAGE_SIZE,
    DEFAULT_SORT,
    DEFAULT_TYPE_FILTER,
    SORT_OPTIONS,
    _get_access_options,
    _rewrite_id_query,
    build_es_body,
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
        body = build_es_body(**_defaults())
        boosting = _get_boosting(body)
        qs = boosting["positive"]["query_string"]
        assert qs["fields"] == ["title^4", "biblio_all^3", "everything"]

    def test_has_quote_field_suffix(self):
        body = build_es_body(**_defaults())
        boosting = _get_boosting(body)
        qs = boosting["positive"]["query_string"]
        assert qs["quote_field_suffix"] == ".exact"

    def test_default_operator_and(self):
        body = build_es_body(**_defaults())
        boosting = _get_boosting(body)
        qs = boosting["positive"]["query_string"]
        assert qs["default_operator"] == "AND"

    def test_query_text_passed_through(self):
        body = build_es_body(**_defaults(q="bovine tuberculosis"))
        boosting = _get_boosting(body)
        qs = boosting["positive"]["query_string"]
        assert qs["query"] == "bovine tuberculosis"


class TestPoorMetadataDemotion:
    def test_boosting_present(self):
        body = build_es_body(**_defaults())
        boosting = _get_boosting(body)
        assert boosting is not None

    def test_negative_boost_value(self):
        body = build_es_body(**_defaults())
        boosting = _get_boosting(body)
        assert boosting["negative_boost"] == 0.5

    def test_negative_checks_missing_fields(self):
        body = build_es_body(**_defaults())
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
        body = build_es_body(**_defaults())
        filters = _get_filters(body)
        type_filters = [f for f in filters if "terms" in f and "biblio.release_type" in f["terms"]]
        assert len(type_filters) == 1
        assert set(type_filters[0]["terms"]["biblio.release_type"]) == {
            "article-journal", "paper-conference", "chapter", "article"
        }

    def test_reports(self):
        body = build_es_body(**_defaults(type_filter="reports"))
        filters = _get_filters(body)
        type_filters = [f for f in filters if "terms" in f and "biblio.release_type" in f["terms"]]
        assert type_filters[0]["terms"]["biblio.release_type"] == ["report", "standard"]

    def test_datasets(self):
        body = build_es_body(**_defaults(type_filter="datasets"))
        filters = _get_filters(body)
        type_filters = [f for f in filters if "terms" in f and "biblio.release_type" in f["terms"]]
        assert type_filters[0]["terms"]["biblio.release_type"] == ["dataset", "software"]

    def test_everything_no_type_filter(self):
        body = build_es_body(**_defaults(type_filter="everything"))
        filters = _get_filters(body)
        type_filters = [f for f in filters if "terms" in f and "biblio.release_type" in f.get("terms", {})]
        assert len(type_filters) == 0


class TestAccessFilter:
    def test_default_fulltext(self):
        body = build_es_body(**_defaults())
        filters = _get_filters(body)
        access_filters = [f for f in filters if "terms" in f and "access.access_type" in f["terms"]]
        assert len(access_filters) == 1
        assert set(access_filters[0]["terms"]["access.access_type"]) == {"wayback", "ia_file", "ia_sim"}

    def test_microfilm(self):
        body = build_es_body(**_defaults(access_filter="microfilm"))
        filters = _get_filters(body)
        access_filters = [f for f in filters if "term" in f and "access.access_type" in f["term"]]
        assert access_filters[0]["term"]["access.access_type"] == "ia_sim"

    def test_oa(self):
        body = build_es_body(**_defaults(access_filter="oa"))
        filters = _get_filters(body)
        oa_filters = [f for f in filters if "term" in f and "tags" in f["term"]]
        assert len(oa_filters) == 1
        assert oa_filters[0]["term"]["tags"] == "oa"

    def test_everything_no_access_filter(self):
        body = build_es_body(**_defaults(access_filter="everything"))
        filters = _get_filters(body)
        access_filters = [
            f for f in filters
            if ("terms" in f and "access.access_type" in f["terms"])
            or ("term" in f and "access.access_type" in f["term"])
            or ("term" in f and "tags" in f["term"])
        ]
        assert len(access_filters) == 0

    def test_non_fulltext_boosts_fulltext_access(self):
        body = build_es_body(**_defaults(access_filter="oa"))
        boosting = _get_boosting(body)
        positive = boosting["positive"]
        assert "bool" in positive
        assert "should" in positive["bool"]
        should = positive["bool"]["should"]
        access_types = should[0]["terms"]["access_type"]
        assert set(access_types) == {"ia_sim", "ia_file", "wayback"}

    def test_fulltext_no_should_boost(self):
        body = build_es_body(**_defaults(access_filter="fulltext"))
        boosting = _get_boosting(body)
        positive = boosting["positive"]
        assert "query_string" in positive


class TestDateFilter:
    def test_all_time_no_date_filter(self):
        body = build_es_body(**_defaults(date_filter="all_time"))
        filters = _get_filters(body)
        date_filters = [f for f in filters if "range" in f and "biblio.release_date" in f["range"]]
        assert len(date_filters) == 0

    def test_past_week(self):
        body = build_es_body(**_defaults(date_filter="past_week"))
        filters = _get_filters(body)
        date_filters = [f for f in filters if "range" in f and "biblio.release_date" in f["range"]]
        assert len(date_filters) == 1
        assert date_filters[0]["range"]["biblio.release_date"]["gte"] == "now-1w"

    def test_before_1931(self):
        body = build_es_body(**_defaults(date_filter="before_1931"))
        filters = _get_filters(body)
        date_filters = [f for f in filters if "range" in f and "biblio.release_date" in f["range"]]
        assert date_filters[0]["range"]["biblio.release_date"]["lt"] == "1931-01-01"

    def test_since_2000(self):
        body = build_es_body(**_defaults(date_filter="since_2000"))
        filters = _get_filters(body)
        date_filters = [f for f in filters if "range" in f and "biblio.release_date" in f["range"]]
        assert date_filters[0]["range"]["biblio.release_date"]["gte"] == "2000-01-01"


class TestCollapse:
    def test_collapse_on_collapse_key(self):
        body = build_es_body(**_defaults())
        assert body["collapse"]["field"] == "collapse_key"

    def test_inner_hits_present(self):
        body = build_es_body(**_defaults())
        assert body["collapse"]["inner_hits"]["name"] == "more_pages"
        assert body["collapse"]["inner_hits"]["size"] == 0


class TestPaginationAndHighlight:
    def test_offset_and_size(self):
        body = build_es_body(**_defaults(offset=50, page_size=10))
        assert body["from"] == 50
        assert body["size"] == 10

    def test_highlight_fields(self):
        body = build_es_body(**_defaults())
        fields = body["highlight"]["fields"]
        assert "abstracts.body" in fields
        assert "fulltext.body" in fields
        assert "fulltext.acknowledgement" in fields
        assert "fulltext.annex" in fields

    def test_highlight_does_not_require_field_match(self):
        body = build_es_body(**_defaults())
        assert body["highlight"]["require_field_match"] is False

    def test_highlight_uses_simplified_query(self):
        body = build_es_body(**_defaults(q="bovine tuberculosis"))
        hq = body["highlight"]["highlight_query"]["query_string"]
        assert hq["query"] == "bovine tuberculosis"
        assert "fields" not in hq


class TestSort:
    def test_default_is_relevancy(self):
        assert DEFAULT_SORT == "relevancy"

    def test_relevancy_no_sort_clause(self):
        body = build_es_body(**_defaults())
        assert "sort" not in body

    def test_newest(self):
        body = build_es_body(**_defaults(sort="newest"))
        assert body["sort"] == [{"biblio.release_date": {"order": "desc", "missing": "_last"}}]

    def test_oldest(self):
        body = build_es_body(**_defaults(sort="oldest"))
        assert body["sort"] == [{"biblio.release_date": {"order": "asc", "missing": "_last"}}]

    def test_relevancy_explicit(self):
        body = build_es_body(**_defaults(sort="relevancy"))
        assert "sort" not in body

    def test_sort_options_match_defaults(self):
        assert SORT_OPTIONS["relevancy"] is None


class TestNoFiltersEverything:
    def test_no_filter_clause_when_all_everything(self):
        body = build_es_body(**_defaults(
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
