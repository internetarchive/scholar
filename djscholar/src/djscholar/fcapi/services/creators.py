from uuid import UUID

from django.db.models import QuerySet

from djscholar.fcapi import models as m
from djscholar.fcapi.fcid import fcid2uuid
from djscholar.fcapi.services import (
    EntityNotFound, _entity_schema_metadata, check_hidden, first_match, visible,
)

LOOKUP_FIELDS = {"orcid"}


def get(ident: UUID, include_hidden: bool = False) -> m.Creator:
    try:
        entity = m.Creator.objects.get(id=ident)
    except m.Creator.DoesNotExist:
        raise EntityNotFound("creator", f"no creator with id {ident}")
    return check_hidden(entity, "creator", include_hidden)


def lookup(id_type: str, id_value: str, include_hidden: bool = False) -> m.Creator:
    if id_type == "legacy_ident":
        return get(fcid2uuid(id_value), include_hidden)

    if id_type not in LOOKUP_FIELDS:
        raise ValueError(f"unsupported creator lookup type: {id_type}")

    results = m.Creator.objects.filter(**{id_type: id_value})
    return first_match(results, "creator",
                       f"no creator found with {id_type} of {id_value}",
                       include_hidden)


def get_by_legacy_rev(rev: UUID, include_hidden: bool = False) -> m.Creator:
    try:
        entity = m.Creator.objects.get(legacy_rev=rev)
    except m.Creator.DoesNotExist:
        raise EntityNotFound("creator", f"no creator with legacy_rev {rev}")
    return check_hidden(entity, "creator", include_hidden)


def schema_metadata(entity: m.Creator) -> dict:
    return _entity_schema_metadata(entity)


def get_releases(ident: UUID, include_hidden: bool = False) -> QuerySet:
    return visible(m.Release.objects.filter(contribs__creator_id=ident), include_hidden)
