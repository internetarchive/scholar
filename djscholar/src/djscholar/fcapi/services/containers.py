from uuid import UUID

from django.db.models import Q, QuerySet

from djscholar.fcapi import models as m
from djscholar.fcapi.fcid import fcid2uuid
from djscholar.fcapi.services import (
    EntityNotFound, _entity_schema_metadata, check_hidden, first_match, visible,
)

LOOKUP_FIELDS = {"issnl", "issne", "issnp", "wikidata_qid"}


def get(ident: UUID, include_hidden: bool = False) -> m.Container:
    try:
        entity = m.Container.objects.get(id=ident)
    except m.Container.DoesNotExist:
        raise EntityNotFound("container", f"no container with id {ident}")
    return check_hidden(entity, "container", include_hidden)


def lookup(id_type: str, id_value: str, include_hidden: bool = False) -> m.Container:
    if id_type == "legacy_ident":
        return get(fcid2uuid(id_value), include_hidden)

    if id_type == "issn":
        results = m.Container.objects.filter(
            Q(issnl=id_value) | Q(issne=id_value) | Q(issnp=id_value)
        )
        return first_match(results, "container",
                           f"no container found with issn of {id_value}",
                           include_hidden)

    if id_type not in LOOKUP_FIELDS:
        raise ValueError(f"unsupported container lookup type: {id_type}")

    results = m.Container.objects.filter(**{id_type: id_value})
    return first_match(results, "container",
                       f"no container found with {id_type} of {id_value}",
                       include_hidden)


def get_by_legacy_rev(rev: UUID, include_hidden: bool = False) -> m.Container:
    try:
        entity = m.Container.objects.get(legacy_rev=rev)
    except m.Container.DoesNotExist:
        raise EntityNotFound("container", f"no container with legacy_rev {rev}")
    return check_hidden(entity, "container", include_hidden)


def schema_metadata(entity: m.Container) -> dict:
    return _entity_schema_metadata(entity)


def get_releases(ident: UUID, include_hidden: bool = False) -> QuerySet:
    return visible(m.Release.objects.filter(container__id=ident), include_hidden)
