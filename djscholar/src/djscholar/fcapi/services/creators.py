from uuid import UUID

from django.db.models import QuerySet

from djscholar.fcapi import models as m
from djscholar.fcapi.fcid import fcid2uuid
from djscholar.fcapi.services import EntityNotFound

LOOKUP_FIELDS = {"orcid"}


def get(ident: UUID) -> m.Creator:
    try:
        return m.Creator.objects.get(id=ident)
    except m.Creator.DoesNotExist:
        raise EntityNotFound("creator", f"no creator with id {ident}")


def lookup(id_type: str, id_value: str) -> m.Creator:
    if id_type == "legacy_ident":
        return get(fcid2uuid(id_value))

    if id_type not in LOOKUP_FIELDS:
        raise ValueError(f"unsupported creator lookup type: {id_type}")

    results = m.Creator.objects.filter(**{id_type: id_value})
    if not results.exists():
        raise EntityNotFound("creator",
                             f"no creator found with {id_type} of {id_value}")
    return results[0]


def get_releases(ident: UUID) -> QuerySet:
    return m.Release.objects.filter(contribs__creator_id=ident)
