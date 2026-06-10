from uuid import UUID

from django.db.models import Q, QuerySet

from djscholar.fcapi import models as m
from djscholar.fcapi.fcid import fcid2uuid
from djscholar.fcapi.services import EntityNotFound, _entity_schema_metadata


def get(ident: UUID) -> m.Release:
    try:
        # select_related the container so callers that render it (most release
        # views) don't trigger a second lazy FK query per request.
        return m.Release.objects.select_related("container").get(id=ident)
    except m.Release.DoesNotExist:
        raise EntityNotFound("release", f"no release with id {ident}")


def lookup(id_type: str, id_value: str) -> m.Release:
    if id_type == "legacy_ident":
        return get(fcid2uuid(id_value))

    results = m.Release.objects.filter(
        extids__id_type=id_type,
        extids__id_value=id_value,
    )
    if not results.exists():
        raise EntityNotFound("release",
                             f"no release found with {id_type} of {id_value}")
    return results[0]


def get_by_legacy_rev(rev: UUID) -> m.Release:
    try:
        return m.Release.objects.get(legacy_rev=rev)
    except m.Release.DoesNotExist:
        raise EntityNotFound("release", f"no release with legacy_rev {rev}")


def schema_metadata(entity: m.Release) -> dict:
    return _entity_schema_metadata(entity)


def get_container(ident: UUID) -> m.Container:
    results = m.Container.objects.filter(release__id=ident)
    if not results.exists():
        raise EntityNotFound("container",
                             f"release {ident} has no associated container")
    return results[0]


def get_work(ident: UUID) -> m.Work:
    release = get(ident)
    try:
        return m.Work.objects.get(id=release.work_id)
    except m.Work.DoesNotExist:
        raise EntityNotFound("work", f"release {ident} has no associated work")


def get_files(ident: UUID) -> QuerySet:
    # FileSchema embeds each file's releases as full ReleaseSchema, and
    # ReleaseSchema in turn embeds extids/contribs/abstracts/citations. Without
    # prefetching those nested collections, serializing each embedded release
    # fires a query per relation (the N+1 Sentry flagged). Prefetch them so the
    # whole response is a fixed handful of queries regardless of file/release
    # count.
    return m.File.objects.filter(
        releasefile__release_id=ident
    ).prefetch_related(
        "urls",
        "releases",
        "releases__extids",
        "releases__contribs",
        "releases__abstracts",
        "releases__citations",
    )


def get_contribs(ident: UUID) -> QuerySet:
    return (
        m.ReleaseContrib.objects.filter(release_id=ident)
        .select_related("creator")
        .order_by("position")
    )


def get_authors(ident: UUID) -> list[m.ReleaseContrib]:
    """Return only author contribs (role='author' or NULL), sorted by position."""
    return list(
        m.ReleaseContrib.objects.filter(
            Q(role__in=["author", ""]) | Q(role__isnull=True),
            release_id=ident,
        )
        .select_related("creator")
        .order_by("position")
    )


def authors_from_contribs(
    contribs: list[m.ReleaseContrib],
) -> list[m.ReleaseContrib]:
    """The author-role subset of an already-fetched contribs list.

    In-memory equivalent of get_authors(), for callers that have already
    loaded all of a release's contribs and want to avoid a second query.
    """
    return [c for c in contribs if c.role in ("author", "") or c.role is None]


def get_extids(ident: UUID) -> dict[str, str]:
    """Return external IDs as a {id_type: id_value} dict."""
    return dict(
        m.ReleaseExtId.objects.filter(release_id=ident)
        .values_list("id_type", "id_value")
    )


def get_abstracts(ident: UUID) -> QuerySet:
    return m.ReleaseAbstract.objects.filter(release_id=ident)


def get_webcaptures(ident: UUID) -> QuerySet:
    return (
        m.Webcapture.objects.filter(release_id=ident)
        .prefetch_related("urls", "cdx_lines")
    )


def get_bulk(idents: list[UUID]) -> dict[UUID, m.Release]:
    """Fetch multiple releases by UUID in a single query.

    Returns a dict mapping UUID -> Release for all found releases.
    Missing UUIDs are silently omitted.
    """
    if not idents:
        return {}
    releases = m.Release.objects.filter(id__in=idents)
    return {r.id: r for r in releases}
