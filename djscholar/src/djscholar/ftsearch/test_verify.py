"""
Tests for the Altcha human check in front of fulltext search.

See ftsearch.views.verify for why the gate is a separate page rather than a
widget on the search form itself.
"""

import base64
import json
import urllib.parse

import altcha
import pytest
from django.core.cache import cache
from django.core.signing import get_cookie_signer
from django.test import Client, override_settings
from django.urls import reverse

from djscholar.ftsearch.views import VERIFY_COOKIE_NAME, VERIFY_COOKIE_SALT


def _solved_payload(max_number=None):
    """Fetch a challenge from the app and grind out a real solution for it."""
    response = Client().get(reverse("ftsearch:altcha_challenge"))
    challenge = json.loads(response.content)
    solution = altcha.solve_challenge(
        challenge=challenge["challenge"],
        salt=challenge["salt"],
        algorithm=challenge["algorithm"],
        max_number=max_number or challenge.get("maxnumber", 1000000),
    )
    assert solution is not None, "challenge was unsolvable"
    payload = {
        "algorithm": challenge["algorithm"],
        "challenge": challenge["challenge"],
        "number": solution.number,
        "salt": challenge["salt"],
        "signature": challenge["signature"],
        "took": solution.took,
    }
    return base64.b64encode(json.dumps(payload).encode()).decode()


def _verified_client():
    client = Client()
    # HttpResponse.set_signed_cookie salts with the cookie name plus the salt
    # we pass it, so mint the cookie the same way here.
    signer = get_cookie_signer(salt=VERIFY_COOKIE_NAME + VERIFY_COOKIE_SALT)
    client.cookies[VERIFY_COOKIE_NAME] = signer.sign("1")
    return client


class _StubES:
    """Stands in for the Elasticsearch client so search can return a page."""

    def search(self, **kwargs):
        return {"took": 3, "hits": {"total": {"value": 0, "relation": "eq"}, "hits": []}}


@pytest.fixture
def stub_es(monkeypatch):
    monkeypatch.setattr("djscholar.es.client", lambda: _StubES())


@pytest.fixture(autouse=True)
def clear_replay_cache():
    """Spent challenges live in the default cache; don't leak them between tests."""
    cache.clear()
    yield
    cache.clear()


class TestSearchGate:
    def test_unverified_search_redirects_to_verify(self):
        response = Client().get("/search", {"q": "cormorant"})
        assert response.status_code == 302
        assert response.url.startswith(reverse("ftsearch:verify"))

    def test_redirect_carries_the_whole_search_back(self):
        response = Client().get(
            "/search", {"q": "cormorant", "page": "3", "sort": "time_desc"}
        )
        next_url = urllib.parse.unquote(response.url.split("next=", 1)[1])
        query = urllib.parse.parse_qs(urllib.parse.urlparse(next_url).query)
        assert urllib.parse.urlparse(next_url).path == reverse("ftsearch:search")
        assert query["q"] == ["cormorant"]
        assert query["page"] == ["3"]
        assert query["sort"] == ["time_desc"]

    def test_posted_query_survives_as_a_get_param(self):
        response = Client().post("/search", {"q": "cormorant"})
        next_url = urllib.parse.unquote(response.url.split("next=", 1)[1])
        query = urllib.parse.parse_qs(urllib.parse.urlparse(next_url).query)
        assert query["q"] == ["cormorant"]

    def test_verified_browser_reaches_search(self, stub_es):
        response = _verified_client().get("/search", {"q": "cormorant"})
        assert response.status_code == 200

    def test_expired_verification_is_gated_again(self):
        client = _verified_client()
        with override_settings(SEARCH_VERIFY_MAX_AGE=-1):
            response = client.get("/search", {"q": "cormorant"})
        assert response.status_code == 302

    def test_tampered_cookie_is_gated_again(self):
        client = Client()
        client.cookies[VERIFY_COOKIE_NAME] = "1:not-a-real-signature"
        response = client.get("/search", {"q": "cormorant"})
        assert response.status_code == 302

    @override_settings(SEARCH_VERIFY_REQUIRED=False)
    def test_gate_can_be_turned_off(self, stub_es):
        response = Client().get("/search", {"q": "cormorant"})
        assert response.status_code == 200

    def test_empty_query_is_not_gated(self):
        # No query means no Elasticsearch call, so there is nothing to protect;
        # bouncing here would gate the homepage.
        response = Client().get("/search")
        assert response.status_code == 200


class TestVerifyView:
    def test_renders_the_widget(self):
        response = Client().get(reverse("ftsearch:verify"), {"next": "/search?q=x"})
        assert response.status_code == 200
        body = response.content.decode()
        assert "<altcha-widget" in body
        assert reverse("ftsearch:altcha_challenge") in body

    def test_offsite_next_falls_back_to_home(self):
        response = Client().get(
            reverse("ftsearch:verify"), {"next": "https://evil.example/steal"}
        )
        assert "evil.example" not in response.content.decode()

    def test_solving_sets_the_cookie_and_returns_to_the_search(self):
        client = Client()
        response = client.post(
            reverse("ftsearch:verify"),
            {"altcha": _solved_payload(), "next": "/search?q=cormorant"},
        )
        assert response.status_code == 302
        assert response.url == "/search?q=cormorant"
        assert VERIFY_COOKIE_NAME in response.cookies
        assert response.cookies[VERIFY_COOKIE_NAME]["httponly"]

    def test_solving_actually_gets_you_into_search(self, stub_es):
        client = Client()
        client.post(
            reverse("ftsearch:verify"),
            {"altcha": _solved_payload(), "next": "/search?q=cormorant"},
        )
        assert client.get("/search", {"q": "cormorant"}).status_code == 200

    def test_offsite_next_is_not_redirected_to_after_solving(self):
        response = Client().post(
            reverse("ftsearch:verify"),
            {"altcha": _solved_payload(), "next": "https://evil.example/steal"},
        )
        assert response.status_code == 302
        assert response.url == reverse("ftsearch:home")

    def test_garbage_solution_is_rejected(self):
        response = Client().post(
            reverse("ftsearch:verify"), {"altcha": "not-a-payload", "next": "/"}
        )
        assert response.status_code == 400
        assert VERIFY_COOKIE_NAME not in response.cookies

    def test_missing_solution_is_rejected(self):
        response = Client().post(reverse("ftsearch:verify"), {"next": "/"})
        assert response.status_code == 400
        assert VERIFY_COOKIE_NAME not in response.cookies

    def test_solution_cannot_be_replayed(self):
        payload = _solved_payload()
        first = Client().post(
            reverse("ftsearch:verify"), {"altcha": payload, "next": "/"}
        )
        assert first.status_code == 302

        second = Client().post(
            reverse("ftsearch:verify"), {"altcha": payload, "next": "/"}
        )
        assert second.status_code == 400
        assert VERIFY_COOKIE_NAME not in second.cookies

    def test_already_verified_browser_skips_the_puzzle(self):
        response = _verified_client().get(
            reverse("ftsearch:verify"), {"next": "/search?q=cormorant"}
        )
        assert response.status_code == 302
        assert response.url == "/search?q=cormorant"

    @override_settings(SEARCH_VERIFY_COOKIE_SECURE=True)
    def test_cookie_can_be_marked_secure(self):
        # Prod and qa set this; a silently non-Secure cookie there would be
        # invisible until someone read the headers.
        response = Client().post(
            reverse("ftsearch:verify"), {"altcha": _solved_payload(), "next": "/"}
        )
        assert response.cookies[VERIFY_COOKIE_NAME]["secure"]
        assert response.cookies[VERIFY_COOKIE_NAME]["samesite"] == "Lax"
