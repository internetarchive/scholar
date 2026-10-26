from uuid import UUID

from django.db.models import QuerySet

from djscholar.fcapi import models as m
from djscholar.fcapi.fcid import fcid2uuid
from djscholar.fcapi.services import (
    EntityNotFound, _entity_schema_metadata, check_hidden, visible,
)


def get(ident: UUID, include_hidden: bool = False) -> m.Work:
    try:
        entity = m.Work.objects.get(id=ident)
    except m.Work.DoesNotExist:
        raise EntityNotFound("work", f"no work with id {ident}")
    return check_hidden(entity, "work", include_hidden)


def lookup(id_type: str, id_value: str, include_hidden: bool = False) -> m.Work:
    if id_type == "legacy_ident":
        return get(fcid2uuid(id_value), include_hidden)
    raise ValueError(f"unsupported work lookup type: {id_type}")


def get_by_legacy_rev(rev: UUID, include_hidden: bool = False) -> m.Work:
    try:
        entity = m.Work.objects.get(legacy_rev=rev)
    except m.Work.DoesNotExist:
        raise EntityNotFound("work", f"no work with legacy_rev {rev}")
    return check_hidden(entity, "work", include_hidden)


def schema_metadata(entity: m.Work) -> dict:
    return _entity_schema_metadata(entity)


def is_hidden(ident: UUID) -> bool:
    """Whether the work exists and is hidden."""
    return m.Work.objects.filter(id=ident, hidden_when__isnull=False).exists()


def get_releases(ident: UUID, include_hidden: bool = False) -> QuerySet:
    return visible(m.Release.objects.filter(work_id=ident), include_hidden)
