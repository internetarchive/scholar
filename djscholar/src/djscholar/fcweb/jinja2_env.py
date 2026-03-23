"""Custom Jinja2 environment for the fcweb app.

Provides Django integration (url reverse, static files) as Jinja2 globals
so that the fatcat templates can use them naturally.
"""

from django.conf import settings
from django.templatetags.static import static
from django.urls import reverse

import jinja2


def environment(**options):
    env = jinja2.Environment(**options)
    env.globals.update(
        {
            "url": reverse,
            "static": static,
            "settings": settings,
        }
    )
    return env
