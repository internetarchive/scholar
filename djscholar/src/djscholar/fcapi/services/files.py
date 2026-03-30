from uuid import UUID

from django.db.models import QuerySet

from djscholar.fcapi import models as m
from djscholar.fcapi.fcid import fcid2uuid
from djscholar.fcapi.services import EntityNotFound, _entity_schema_metadata

LOOKUP_FIELDS = {"sha1", "sha256", "md5"}


def get(ident: UUID) -> m.File:
    try:
        return m.File.objects.get(id=ident)
    except m.File.DoesNotExist:
        raise EntityNotFound("file", f"no file with id {ident}")


def lookup(id_type: str, id_value: str) -> m.File:
    if id_type == "legacy_ident":
        return get(fcid2uuid(id_value))

    if id_type not in LOOKUP_FIELDS:
        raise ValueError(f"unsupported file lookup type: {id_type}")

    id_value = id_value.lower()
    results = m.File.objects.filter(**{id_type: id_value})
    if not results.exists():
        raise EntityNotFound("file",
                             f"no file found with {id_type} of {id_value}")
    return results[0]


def get_by_legacy_rev(rev: UUID) -> m.File:
    try:
        return m.File.objects.get(legacy_rev=rev)
    except m.File.DoesNotExist:
        raise EntityNotFound("file", f"no file with legacy_rev {rev}")


def schema_metadata(entity: m.File) -> dict:
    return _entity_schema_metadata(entity)


def get_releases(ident: UUID) -> QuerySet:
    return m.Release.objects.filter(releasefile__file_id=ident)


def find_access_url(work_id: UUID) -> str | None:
    """Find the best access URL for a work's files.

    Searches all files associated with a work's releases and returns the best
    available URL, preferring wayback > webarchive > other.

    Used by:
    - fcapi fulltext redirect endpoint
    - ftsearch access redirect fallback
    """
    releases = m.Release.objects.filter(work_id=work_id)
    wayback_url = ""
    webarchive_url = ""
    other_url = ""

    for rel in releases:
        files = m.File.objects.filter(releasefile__release_id=rel.id)
        for f in files:
            for u in f.urls.all():
                if "web.archive.org" in u.url:
                    wayback_url = u.url
                elif u.rel == "webarchive":
                    webarchive_url = u.url
                else:
                    other_url = u.url

    return wayback_url or webarchive_url or other_url or None


def get_work_access_urls(work_id: UUID) -> list[str]:
    """Return all file access URLs for a work's releases.

    Returns a flat list of URL strings across all files for all releases
    of the given work. Callers can apply their own matching logic.
    """
    urls = []
    releases = m.Release.objects.filter(work_id=work_id)
    for rel in releases:
        files = m.File.objects.filter(releasefile__release_id=rel.id)
        for f in files:
            for u in f.urls.all():
                urls.append(u.url)
    return urls
