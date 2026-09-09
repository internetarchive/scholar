
import os

import sentry_sdk
from sentry_sdk.integrations.django import DjangoIntegration

from .base import *
# Named explicitly as well as by the star above: it's a function, not a
# setting, and ruff can't see it through the wildcard.
from .base import altcha_hmac_key

SECRET_KEY = os.environ["DJANGO_SECRET_KEY"]
DEBUG = False
ALLOWED_HOSTS = ["localhost", ".scholar.archive.org"]

# base.py derived this from its placeholder SECRET_KEY; redo it now that the
# real one is in hand.
ALTCHA_HMAC_KEY = os.environ.get("ALTCHA_HMAC_KEY") or altcha_hmac_key(SECRET_KEY)

CSRF_TRUSTED_ORIGINS = ['https://scholar.archive.org']

SEARCH_VERIFY_COOKIE_SECURE = True

DATABASES = {
    # NB at this time we have no plans for scholar's web frontend to need
    # postgresql. However, we'll likely add a database for whatever replaces
    # sandcrawler. At that time 'default' will not be a very good name for this
    # database but it's fine for now.
    'default': {
        'ENGINE': 'django.db.backends.postgresql',
        'USER': 'fatcat',
        'PASSWORD': os.environ["FATCAT_DB_PASSWORD"],
        'NAME': 'fatcat2',
        'HOST': 'pg.scholar.archive.org',
        'DISABLE_SERVER_SIDE_CURSORS': True,
        # Reuse the client->pgbouncer connection across requests instead of
        # opening a fresh one each time (the two remote app nodes pay a
        # TCP+TLS+auth handshake per request otherwise). CONN_HEALTH_CHECKS
        # validates a reused connection at request start so a connection
        # dropped by pgbouncer/network doesn't surface as an error.
        'CONN_MAX_AGE': 300,
        'CONN_HEALTH_CHECKS': True,
    }
}

sentry_sdk.init(
    dsn="https://c318626d530b3320948be1032ae0546f@sentry.archive.org/52",
    integrations=[DjangoIntegration()],
    environment="production",

    # Set traces_sample_rate to 1.0 to capture 100%
    # of transactions for performance monitoring.
    # We recommend adjusting this value in production.
    traces_sample_rate=1.0,

    # If you wish to associate users to errors (assuming you are using
    # django.contrib.auth) you may enable sending PII data.
    send_default_pii=True
)

STATIC_ROOT = "/var/www/djscholar/static"

ES_INDEX = "scholar_fulltext"
