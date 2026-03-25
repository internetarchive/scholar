from uuid import UUID

from django.db.models import QuerySet

from djscholar.fcapi import models as m
from djscholar.fcapi.fcid import fcid2uuid
from djscholar.fcapi.services import EntityNotFound


def get(ident: UUID) -> m.Fileset:
    try:
        return m.Fileset.objects.get(id=ident)
    except m.Fileset.DoesNotExist:
        raise EntityNotFound("fileset", f"no fileset with id {ident}")


def lookup(id_type: str, id_value: str) -> m.Fileset:
    if id_type == "legacy_ident":
        return get(fcid2uuid(id_value))
    raise ValueError(f"unsupported fileset lookup type: {id_type}")


def get_release(ident: UUID) -> m.Release | None:
    fs = get(ident)
    return fs.release


def get_files(ident: UUID) -> QuerySet:
    return m.FilesetFile.objects.filter(fileset_id=ident)


def get_urls(ident: UUID) -> QuerySet:
    return m.FilesetURL.objects.filter(fileset_id=ident)
