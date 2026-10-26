from uuid import UUID

from djscholar.fcapi import models as m
from djscholar.fcapi.fcid import fcid2uuid
from djscholar.fcapi.services import (
    EntityNotFound, _entity_schema_metadata, check_hidden,
)


def get(ident: UUID, include_hidden: bool = False) -> m.Webcapture:
    try:
        entity = m.Webcapture.objects.get(id=ident)
    except m.Webcapture.DoesNotExist:
        raise EntityNotFound("webcapture", f"no webcapture with id {ident}")
    return check_hidden(entity, "webcapture", include_hidden)


def lookup(id_type: str, id_value: str, include_hidden: bool = False) -> m.Webcapture:
    if id_type == "legacy_ident":
        return get(fcid2uuid(id_value), include_hidden)
    raise ValueError(f"unsupported webcapture lookup type: {id_type}")


def get_by_legacy_rev(rev: UUID, include_hidden: bool = False) -> m.Webcapture:
    try:
        entity = m.Webcapture.objects.get(legacy_rev=rev)
    except m.Webcapture.DoesNotExist:
        raise EntityNotFound("webcapture", f"no webcapture with legacy_rev {rev}")
    return check_hidden(entity, "webcapture", include_hidden)


def schema_metadata(entity: m.Webcapture) -> dict:
    return _entity_schema_metadata(entity)


def get_release(ident: UUID, include_hidden: bool = False) -> m.Release:
    """The release, or EntityHidden if either it or this webcapture is hidden."""
    wc = get(ident, include_hidden)
    return check_hidden(wc.release, "release", include_hidden)
