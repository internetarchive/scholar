"""Custom Jinja2 environment for the fcweb app.

Provides Django integration (url reverse, static files) as Jinja2 globals
so that the fatcat templates can use them naturally.
"""

from django.conf import settings
from django.templatetags.static import static
from django.urls import reverse

import jinja2


def _url(name, **kwargs):
    """Wrapper around Django's reverse() that accepts keyword arguments directly.

    In templates: {{ url('fcweb:release_view', ident='abc123') }}
    maps to: reverse('fcweb:release_view', kwargs={'ident': 'abc123'})
    """
    if kwargs:
        return reverse(name, kwargs=kwargs)
    return reverse(name)


def environment(**options):
    env = jinja2.Environment(**options)
    env.globals.update(
        {
            "url": _url,
            "static": static,
            "settings": settings,
        }
    )
    return env
