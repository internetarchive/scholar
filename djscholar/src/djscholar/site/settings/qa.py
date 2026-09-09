import os

import sentry_sdk
from sentry_sdk.integrations.django import DjangoIntegration

from .base import *
# Named explicitly as well as by the star above: it's a function, not a
# setting, and ruff can't see it through the wildcard.
from .base import altcha_hmac_key

SECRET_KEY = os.environ["DJANGO_SECRET_KEY"]
DEBUG = False
ALLOWED_HOSTS = ["localhost", ".scholar-qa.archive.org"]

# The verify page is the first browser POST form in the app. TLS terminates at
# the load balancer and SECURE_PROXY_SSL_HEADER isn't set, so Django compares
# the browser's https Origin against an http host and rejects the post unless
# the origin is trusted outright. prod already does this.
CSRF_TRUSTED_ORIGINS = ['https://scholar-qa.archive.org']

SEARCH_VERIFY_COOKIE_SECURE = True

# base.py derived this from its placeholder SECRET_KEY; redo it now that the
# real one is in hand.
ALTCHA_HMAC_KEY = os.environ.get("ALTCHA_HMAC_KEY") or altcha_hmac_key(SECRET_KEY)

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
    }
}

sentry_sdk.init(
    dsn="https://a24be9d783154fd88ce7c386e7165d2b@sentry.archive-it.org/28",
    integrations=[DjangoIntegration()],
    environment="qa",

    # Set traces_sample_rate to 1.0 to capture 100%
    # of transactions for performance monitoring.
    # We recommend adjusting this value in production.
    traces_sample_rate=1.0,

    # If you wish to associate users to errors (assuming you are using
    # django.contrib.auth) you may enable sending PII data.
    send_default_pii=True
)

LOGGING = {
    'version': 1,
    'disable_existing_loggers': False,
    'formatters': {
        'verbose': {
            'format': '%(asctime)s %(levelname)s [%(name)s:%(lineno)s] %(module)s %(process)d %(thread)d %(message)s'
        }
    },
    'handlers': {
        'gunicorn': {
            'level': 'ERROR',
            'class': 'logging.handlers.RotatingFileHandler',
            'formatter': 'verbose',
            'filename': '/var/log/djscholar-gunicorn-error.log',
            'maxBytes': 1024 * 1024 * 100,  # 100 mb
        }
    },
    'loggers': {
        'django': {
            'level': 'ERROR',
            'handlers': ['gunicorn'],
            'propagate': True,
        },
        'django.request': {
            'level': 'ERROR',
            'handlers': ['gunicorn'],
            'propagate': False,
        },
    }
}

STATIC_ROOT = "/var/www/djscholar/static"
