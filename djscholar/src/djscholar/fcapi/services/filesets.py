from uuid import UUID

from django.db.models import QuerySet

from djscholar.fcapi import models as m
from djscholar.fcapi.fcid import fcid2uuid
from djscholar.fcapi.services import (
    EntityNotFound, _entity_schema_metadata, check_hidden,
)


def get(ident: UUID, include_hidden: bool = False) -> m.Fileset:
    try:
        entity = m.Fileset.objects.get(id=ident)
    except m.Fileset.DoesNotExist:
        raise EntityNotFound("fileset", f"no fileset with id {ident}")
    return check_hidden(entity, "fileset", include_hidden)


def lookup(id_type: str, id_value: str, include_hidden: bool = False) -> m.Fileset:
    if id_type == "legacy_ident":
        return get(fcid2uuid(id_value), include_hidden)
    raise ValueError(f"unsupported fileset lookup type: {id_type}")


def get_by_legacy_rev(rev: UUID, include_hidden: bool = False) -> m.Fileset:
    try:
        entity = m.Fileset.objects.get(legacy_rev=rev)
    except m.Fileset.DoesNotExist:
        raise EntityNotFound("fileset", f"no fileset with legacy_rev {rev}")
    return check_hidden(entity, "fileset", include_hidden)


def schema_metadata(entity: m.Fileset) -> dict:
    return _entity_schema_metadata(entity)


def get_release(ident: UUID, include_hidden: bool = False) -> m.Release | None:
    """The release, or EntityHidden if either it or this fileset is hidden."""
    fs = get(ident, include_hidden)
    if fs.release is None:
        return None
    return check_hidden(fs.release, "release", include_hidden)


def get_files(ident: UUID) -> QuerySet:
    return m.FilesetFile.objects.filter(fileset_id=ident)


def get_urls(ident: UUID) -> QuerySet:
    return m.FilesetURL.objects.filter(fileset_id=ident)
