
import os

import sentry_sdk
from sentry_sdk.integrations.django import DjangoIntegration

from .base import *

SECRET_KEY = os.environ["DJANGO_SECRET_KEY"]
DEBUG = False
ALLOWED_HOSTS = ["localhost", ".scholar.archive.org"]

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
