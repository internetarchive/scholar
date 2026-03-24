from uuid import UUID

from django.db import transaction
from django.db.models import QuerySet

from djscholar.fcapi import models as m
from djscholar.fcapi.fcid import fcid2uuid
from djscholar.fcapi.services import EntityNotFound


def get(ident: UUID) -> m.Release:
    try:
        return m.Release.objects.get(id=ident)
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
    return m.File.objects.filter(
        releasefile__release_id=ident
    ).prefetch_related("releases", "urls")


def get_contribs(ident: UUID) -> QuerySet:
    return m.ReleaseContrib.objects.filter(release_id=ident)


def get_webcaptures(ident: UUID) -> QuerySet:
    return m.Webcapture.objects.filter(release_id=ident)
