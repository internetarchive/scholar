import datetime

import pytest

from djscholar.ftsearch.models import DailyAccessStat
from djscholar.ftsearch.views import _classify_referrer, _record_access


class TestClassifyReferrer:
    def test_none_is_direct(self):
        assert _classify_referrer(None) == "direct"

    def test_empty_string_is_direct(self):
        assert _classify_referrer("") == "direct"

    def test_scholar_google_com(self):
        assert _classify_referrer("https://scholar.google.com/") == "google_scholar"

    def test_scholar_google_regional_tld(self):
        # scholar.google.co.uk, scholar.google.de, etc. should all bucket the same
        assert _classify_referrer("https://scholar.google.co.uk/foo") == "google_scholar"
        assert _classify_referrer("https://scholar.google.de/") == "google_scholar"

    def test_scholar_google_case_insensitive(self):
        assert _classify_referrer("https://SCHOLAR.GOOGLE.COM/abc") == "google_scholar"

    def test_plain_google_not_folded_into_scholar(self):
        # ordering in _REFERRER_BUCKETS matters: scholar.google.* is checked
        # first so www.google.com never gets tagged google_scholar
        assert _classify_referrer("https://www.google.com/search?q=x") == "google"

    def test_non_google_origin(self):
        assert _classify_referrer("https://duckduckgo.com/") == "other"

    def test_malformed_referrer_buckets_other(self):
        # A Referer header was present (not direct) but we can't extract a
        # hostname, so "other" is the honest label.
        assert _classify_referrer("not-a-url") == "other"

    def test_classifies_on_host_not_path(self):
        # Make sure "google." in a path/query doesn't leak into the bucket.
        assert _classify_referrer("https://example.com/?ref=google.com") == "other"


@pytest.mark.django_db
class TestRecordAccess:
    def test_creates_row_with_default_bucket_when_no_referrer(self):
        _record_access("wayback")
        row = DailyAccessStat.objects.get(
            date=datetime.date.today(),
            access_type="wayback",
            referrer_bucket="direct",
        )
        assert row.count == 1

    def test_none_referrer_buckets_direct(self):
        _record_access("wayback", None)
        assert DailyAccessStat.objects.filter(
            referrer_bucket="direct"
        ).count() == 1

    def test_increments_existing_row(self):
        _record_access("wayback", None)
        _record_access("wayback", None)
        _record_access("wayback", None)
        row = DailyAccessStat.objects.get(
            access_type="wayback", referrer_bucket="direct",
        )
        assert row.count == 3

    def test_google_scholar_referrer_gets_own_row(self):
        _record_access("wayback", "https://scholar.google.com/search?q=x")
        assert DailyAccessStat.objects.filter(
            access_type="wayback", referrer_bucket="google_scholar",
        ).count() == 1

    def test_different_buckets_are_separate_rows(self):
        _record_access("wayback", None)
        _record_access("wayback", "https://scholar.google.com/")
        _record_access("wayback", "https://www.google.com/")
        _record_access("wayback", "https://duckduckgo.com/")

        today = datetime.date.today()
        by_bucket = dict(
            DailyAccessStat.objects.filter(date=today, access_type="wayback")
            .values_list("referrer_bucket", "count")
        )
        assert by_bucket == {
            "direct": 1,
            "google_scholar": 1,
            "google": 1,
            "other": 1,
        }

    def test_different_access_types_are_separate_rows(self):
        _record_access("wayback", "https://scholar.google.com/")
        _record_access("ia_file", "https://scholar.google.com/")

        rows = DailyAccessStat.objects.filter(referrer_bucket="google_scholar")
        assert rows.count() == 2
        assert {r.access_type for r in rows} == {"wayback", "ia_file"}

    def test_same_bucket_repeated_calls_do_not_duplicate_rows(self):
        for _ in range(5):
            _record_access("ia_file", "https://scholar.google.com/")
        rows = DailyAccessStat.objects.filter(
            access_type="ia_file", referrer_bucket="google_scholar",
        )
        assert rows.count() == 1
        assert rows.first().count == 5
