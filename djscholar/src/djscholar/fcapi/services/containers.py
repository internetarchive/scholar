from uuid import UUID

from django.db.models import Q, QuerySet

from djscholar.fcapi import models as m
from djscholar.fcapi.fcid import fcid2uuid
from djscholar.fcapi.services import EntityNotFound

LOOKUP_FIELDS = {"issnl", "issne", "issnp", "wikidata_qid"}


def get(ident: UUID) -> m.Container:
    try:
        return m.Container.objects.get(id=ident)
    except m.Container.DoesNotExist:
        raise EntityNotFound("container", f"no container with id {ident}")


def lookup(id_type: str, id_value: str) -> m.Container:
    if id_type == "legacy_ident":
        return get(fcid2uuid(id_value))

    if id_type == "issn":
        results = m.Container.objects.filter(
            Q(issnl=id_value) | Q(issne=id_value) | Q(issnp=id_value)
        )
        if not results.exists():
            raise EntityNotFound("container",
                                 f"no container found with issn of {id_value}")
        return results[0]

    if id_type not in LOOKUP_FIELDS:
        raise ValueError(f"unsupported container lookup type: {id_type}")

    results = m.Container.objects.filter(**{id_type: id_value})
    if not results.exists():
        raise EntityNotFound("container",
                             f"no container found with {id_type} of {id_value}")
    return results[0]


def get_releases(ident: UUID) -> QuerySet:
    return m.Release.objects.filter(container__id=ident)
