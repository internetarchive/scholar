from uuid import UUID

from django.db.models import QuerySet

from djscholar.fcapi import models as m
from djscholar.fcapi.fcid import fcid2uuid
from djscholar.fcapi.services import EntityNotFound, _entity_schema_metadata


def get(ident: UUID) -> m.Work:
    try:
        return m.Work.objects.get(id=ident)
    except m.Work.DoesNotExist:
        raise EntityNotFound("work", f"no work with id {ident}")


def lookup(id_type: str, id_value: str) -> m.Work:
    if id_type == "legacy_ident":
        return get(fcid2uuid(id_value))
    raise ValueError(f"unsupported work lookup type: {id_type}")


def get_by_legacy_rev(rev: UUID) -> m.Work:
    try:
        return m.Work.objects.get(legacy_rev=rev)
    except m.Work.DoesNotExist:
        raise EntityNotFound("work", f"no work with legacy_rev {rev}")


def schema_metadata(entity: m.Work) -> dict:
    return _entity_schema_metadata(entity)


def get_releases(ident: UUID) -> QuerySet:
    return m.Release.objects.filter(work_id=ident)
