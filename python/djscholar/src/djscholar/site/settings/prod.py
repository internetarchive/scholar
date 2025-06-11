import os

SECRET_KEY = os.environ["DJANGO_SECRET_KEY"]
DEBUG = False
ALLOWED_HOSTS = [".scholar.archive.org"]

DATABASES = {
    # NB at this time we have no plans for scholar's web frontend to need
    # postgresql. However, we'll likely add a database for whatever replaces
    # sandcrawler. At that time 'default' will not be a very good name for this
    # database but it's fine for now.
    'default': {
        'ENGINE': 'django.db.backends.postgresql',
        'USER': 'fatcat',
        'PASSWORD': os.environ["FATCAT_DB_PASSWORD"],
        'NAME': 'fatcat',
        'HOST': 'pg.scholar.archive.org',
    }
}
