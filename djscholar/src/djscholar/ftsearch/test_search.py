from djscholar.ftsearch.views import (
    DEFAULT_ACCESS_FILTER,
    DEFAULT_DATE_FILTER,
    DEFAULT_PAGE_SIZE,
    DEFAULT_TYPE_FILTER,
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
