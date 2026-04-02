"""
URL configuration for the ftsearch app.

These routes are ported from fatcat-scholar's src/scholar/web.py.
The view functions are stubs (in views.py) to be implemented.

The original FastAPI app also mounted these sub-routers:
  - fatcat web routes at /fatcat (from scholar.fatcat.web)
  - fatcat API routes at /api/fatcat (from scholar.fatcat.api)
Those will be handled separately.
"""

from django.urls import path

from djscholar.ftsearch import views

app_name = "ftsearch"

urlpatterns = [
    # Health checks
    path("_health/web", views.webhealth, name="webhealth"),
    path("_health", views.webhealth, name="health"),
    path("_health/search", views.searchhealth, name="searchhealth"),

    # Scholar web pages
    path("", views.home, name="home"),
    path("about", views.about, name="about"),
    path("help", views.help, name="help"),
    path("stats", views.stats, name="stats"),

    # Search
    path("random", views.random_paper, name="random_paper"),
    path("search", views.search, name="search"),

    # Work detail and access redirects
    path("work/<uuid:work_uuid>", views.work, name="work"),
    path("work/<str:work_ident>", views.work_legacy, name="work_legacy"),
    path(
        "work/<str:work_ident>/access/wayback/<path:url>",
        views.access_redirect_wayback,
        name="access_redirect_wayback",
    ),
    path(
        "work/<str:work_ident>/access/ia_file/<str:item>/<path:file_path>",
        views.access_redirect_ia_file,
        name="access_redirect_ia_file",
    ),

    # RSS feed
    # I'm not preserving this feature but leaving it as a reminder in case we
    # want to bring it back.
    # path("feed/rss", views.feed_rss, name="feed_rss"),


    # TODO let's see if we can serve these via nginx. the old app had proper
    # views for them.
    # path("favicon.ico", views.favicon, name="favicon"),
    # path("sitemap.xml", views.sitemap, name="sitemap"),
    # path("robots.txt", views.robots_txt, name="robots_txt"),
]
