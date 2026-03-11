from .base import *

DEBUG = True

DATABASES = {
    # NB at this time we have no plans for scholar's web frontend to need
    # postgresql. However, we'll likely add a database for whatever replaces
    # sandcrawler. At that time 'default' will not be a very good name for this
    # database but it's fine for now.
    'default': {
        'ENGINE': 'django.db.backends.postgresql',
        'USER': 'fatcat',
        'PASSWORD': 'fatcat',
        'NAME': 'fatcat',
        'HOST': 'fatcat-postgres-17',
        'DISABLE_SERVER_SIDE_CURSORS': True,
    }
}

#STATIC_ROOT = "/var/www/djscholar/static"
