import io
import json
from unittest import mock

import pytest
from django.core.cache import cache

from djscholar import spn


def _http_response(payload):
    """A stand-in for what urlopen() hands back (a context manager w/ read())."""
    body = payload if isinstance(payload, bytes) else json.dumps(payload).encode()
    return io.BytesIO(body)


@pytest.fixture(autouse=True)
def clear_spn_cache():
    cache.delete(spn._USER_STATUS_CACHE_KEY)
    yield
    cache.delete(spn._USER_STATUS_CACHE_KEY)


class TestUserStatus:
    def test_max_slots_is_available_plus_processing(self):
        status = spn.UserStatus(available=3, processing=2)
        assert status.max_slots == 5

    def test_in_use_is_processing(self):
        assert spn.UserStatus(available=3, processing=2).in_use == 2

    def test_saturation_pct(self):
        assert spn.UserStatus(available=3, processing=1).saturation_pct == 25.0

    def test_saturation_pct_fully_saturated(self):
        assert spn.UserStatus(available=0, processing=4).saturation_pct == 100.0

    def test_saturation_pct_idle(self):
        assert spn.UserStatus(available=4, processing=0).saturation_pct == 0.0

    def test_saturation_pct_none_when_no_slots(self):
        # SPN shouldn't report a zero-slot account, but don't divide by zero.
        assert spn.UserStatus(available=0, processing=0).saturation_pct is None


class TestParseUserStatus:
    def test_parses_slot_counts(self):
        status = spn._parse_user_status({"available": 5, "processing": 1})
        assert status == spn.UserStatus(available=5, processing=1)

    def test_ignores_extra_keys(self):
        status = spn._parse_user_status(
            {"available": 5, "processing": 1, "message": "hi"}
        )
        assert status == spn.UserStatus(available=5, processing=1)

    def test_message_only_body_is_none(self):
        # SPN answers some errors with a 200 and only a message.
        assert spn._parse_user_status({"message": "You need to be logged in"}) is None

    def test_missing_processing_is_none(self):
        assert spn._parse_user_status({"available": 5}) is None

    def test_non_int_counts_are_none(self):
        assert spn._parse_user_status({"available": "5", "processing": "1"}) is None

    def test_non_object_body_is_none(self):
        assert spn._parse_user_status([1, 2, 3]) is None


@pytest.fixture
def spn_credentials(settings):
    settings.SPN_ENDPOINT = "https://web.archive.org/save"
    settings.SPN_ACCESS_KEY = "key"
    settings.SPN_SECRET_KEY = "secret"
    return settings


class TestFetchUserStatus:
    def test_fetches_and_parses(self, spn_credentials):
        with mock.patch.object(spn.urllib.request, "urlopen") as urlopen:
            urlopen.return_value = _http_response({"available": 4, "processing": 2})
            status = spn._fetch_user_status()
        assert status == spn.UserStatus(available=4, processing=2)

    def test_request_targets_status_user_with_auth(self, spn_credentials):
        with mock.patch.object(spn.urllib.request, "urlopen") as urlopen:
            urlopen.return_value = _http_response({"available": 4, "processing": 2})
            spn._fetch_user_status()
        req = urlopen.call_args.args[0]
        assert req.full_url == "https://web.archive.org/save/status/user"
        assert req.get_header("Authorization") == "LOW key:secret"

    def test_endpoint_trailing_slash_does_not_double_up(self, spn_credentials):
        spn_credentials.SPN_ENDPOINT = "https://web.archive.org/save/"
        with mock.patch.object(spn.urllib.request, "urlopen") as urlopen:
            urlopen.return_value = _http_response({"available": 1, "processing": 0})
            spn._fetch_user_status()
        req = urlopen.call_args.args[0]
        assert req.full_url == "https://web.archive.org/save/status/user"

    def test_transport_error_is_none(self, spn_credentials):
        with mock.patch.object(spn.urllib.request, "urlopen") as urlopen:
            urlopen.side_effect = OSError("connection refused")
            assert spn._fetch_user_status() is None

    def test_malformed_json_is_none(self, spn_credentials):
        with mock.patch.object(spn.urllib.request, "urlopen") as urlopen:
            urlopen.return_value = _http_response(b"<html>502</html>")
            assert spn._fetch_user_status() is None

    def test_no_credentials_skips_the_request(self, spn_credentials):
        spn_credentials.SPN_ACCESS_KEY = ""
        spn_credentials.SPN_SECRET_KEY = ""
        with mock.patch.object(spn.urllib.request, "urlopen") as urlopen:
            assert spn._fetch_user_status() is None
        urlopen.assert_not_called()

    def test_partial_credentials_skip_the_request(self, spn_credentials):
        spn_credentials.SPN_SECRET_KEY = ""
        with mock.patch.object(spn.urllib.request, "urlopen") as urlopen:
            assert spn._fetch_user_status() is None
        urlopen.assert_not_called()


class TestGetUserStatus:
    def test_caches_successful_lookup(self):
        status = spn.UserStatus(available=4, processing=2)
        with mock.patch.object(spn, "_fetch_user_status", return_value=status) as f:
            assert spn.get_user_status() == status
            assert spn.get_user_status() == status
        assert f.call_count == 1

    def test_caches_failed_lookup(self):
        with mock.patch.object(spn, "_fetch_user_status", return_value=None) as f:
            assert spn.get_user_status() is None
            assert spn.get_user_status() is None
        assert f.call_count == 1

    def test_idle_status_is_not_mistaken_for_a_cache_miss(self):
        # An all-zeroes status is falsy-looking but real; it must still cache.
        status = spn.UserStatus(available=0, processing=0)
        with mock.patch.object(spn, "_fetch_user_status", return_value=status) as f:
            assert spn.get_user_status() == status
            assert spn.get_user_status() == status
        assert f.call_count == 1
