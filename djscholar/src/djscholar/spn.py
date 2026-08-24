"""Thin read-only client for the Wayback SavePageNow (SPN) API.

Only the /status/user endpoint is implemented: the /stats page reports how
saturated our SPN capture slots are. The credentials are the same ones the
trawler crawls with (see trawler/spn/spnclient for the fuller Go client).
"""

import json
import logging
import urllib.request
from dataclasses import dataclass

from django.conf import settings
from django.core.cache import cache

logger = logging.getLogger(__name__)

# SPN is a shared, rate-limited service and /stats is a public page, so cache
# the answer briefly. The TTL is short because this is meant to be a
# of-the-moment reading, not a trend.
_USER_STATUS_CACHE_KEY = "spn:user_status"
_USER_STATUS_TTL = 30
# A failed lookup is cached too, so an SPN outage doesn't mean a blocking
# 10-second timeout on every single /stats request.
_USER_STATUS_FAIL_TTL = 120
_USER_STATUS_UNAVAILABLE = "__unavailable__"

# SPN status is a nice-to-have on a page full of other stats; don't let it hold
# a request open for long.
_TIMEOUT = 10


@dataclass(frozen=True)
class UserStatus:
    """Capture-slot usage for the account whose keys we hold.

    SPN reports the free slots ("available") and the in-flight captures
    ("processing") rather than a configured maximum, so the account's slot
    ceiling is the sum of the two.
    """

    available: int
    processing: int

    @property
    def in_use(self) -> int:
        return self.processing

    @property
    def max_slots(self) -> int:
        return self.available + self.processing

    @property
    def saturation_pct(self) -> float | None:
        if self.max_slots <= 0:
            return None
        return round(self.in_use / self.max_slots * 100, 1)


def _fetch_user_status() -> UserStatus | None:
    """GET /status/user from SPN. Returns None if it can't be read."""
    if not settings.SPN_ACCESS_KEY or not settings.SPN_SECRET_KEY:
        return None

    url = settings.SPN_ENDPOINT.rstrip("/") + "/status/user"
    req = urllib.request.Request(url, headers={
        "User-Agent": "djscholar-spn-status",
        "Accept": "application/json",
        "Authorization": "LOW {}:{}".format(
            settings.SPN_ACCESS_KEY, settings.SPN_SECRET_KEY,
        ),
    })

    try:
        with urllib.request.urlopen(req, timeout=_TIMEOUT) as resp:
            payload = json.loads(resp.read())
    except Exception:
        logger.warning("SPN user status request failed", exc_info=True)
        return None

    return _parse_user_status(payload)


def _parse_user_status(payload) -> UserStatus | None:
    """Turn a /status/user response body into a UserStatus.

    SPN answers some errors with a 200 and a {"message": ...} body, so a
    well-formed response is one that actually carries both slot counts.
    """
    if not isinstance(payload, dict):
        logger.warning("SPN user status was not an object: %r", payload)
        return None

    available = payload.get("available")
    processing = payload.get("processing")
    if not isinstance(available, int) or not isinstance(processing, int):
        logger.warning("SPN user status lacked slot counts: %r", payload)
        return None

    return UserStatus(available=available, processing=processing)


def get_user_status() -> UserStatus | None:
    """SPN capture-slot usage, cached. Returns None when unavailable."""
    cached = cache.get(_USER_STATUS_CACHE_KEY)
    if cached is not None:
        return None if cached == _USER_STATUS_UNAVAILABLE else cached

    status = _fetch_user_status()
    if status is None:
        cache.set(_USER_STATUS_CACHE_KEY, _USER_STATUS_UNAVAILABLE,
                  _USER_STATUS_FAIL_TTL)
    else:
        cache.set(_USER_STATUS_CACHE_KEY, status, _USER_STATUS_TTL)
    return status
