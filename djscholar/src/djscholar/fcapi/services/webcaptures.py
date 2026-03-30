from uuid import UUID

from djscholar.fcapi import models as m
from djscholar.fcapi.fcid import fcid2uuid
from djscholar.fcapi.services import EntityNotFound, _entity_schema_metadata


def get(ident: UUID) -> m.Webcapture:
    try:
        return m.Webcapture.objects.get(id=ident)
    except m.Webcapture.DoesNotExist:
        raise EntityNotFound("webcapture", f"no webcapture with id {ident}")


def lookup(id_type: str, id_value: str) -> m.Webcapture:
    if id_type == "legacy_ident":
        return get(fcid2uuid(id_value))
    raise ValueError(f"unsupported webcapture lookup type: {id_type}")


def get_by_legacy_rev(rev: UUID) -> m.Webcapture:
    try:
        return m.Webcapture.objects.get(legacy_rev=rev)
    except m.Webcapture.DoesNotExist:
        raise EntityNotFound("webcapture", f"no webcapture with legacy_rev {rev}")


def schema_metadata(entity: m.Webcapture) -> dict:
    return _entity_schema_metadata(entity)


def get_release(ident: UUID) -> m.Release:
    wc = get(ident)
    return wc.release
