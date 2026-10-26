"""Service layer for the fatcat API.

This package contains the business logic for entity operations. Both the HTTP
API views (fcapi/views.py) and other Django apps (ftsearch, fcweb) should use
these functions rather than querying models directly.

Functions return model instances or raise EntityNotFound.

Hidden entities (hidden_when set, eg after a takedown) are left out of every
result unless the caller passes include_hidden=True. Fetching a single hidden
entity raises EntityHidden.
"""

from django.db import models
from django.db.models import F, QuerySet

_METADATA_SKIP_FIELDS = {"id", "extra", "legacy_rev", "source", "hidden_reason", "hidden_when"}


def _entity_schema_metadata(entity: models.Model) -> dict:
    """Extract model field values as an ordered dict for the metadata view.

    Skips internal fields (id, extra, legacy_rev, etc.) and None values.
    """
    result = {}
    for field in entity._meta.get_fields():
        if not hasattr(field, "attname"):
            continue
        name = field.attname
        if name in _METADATA_SKIP_FIELDS:
            continue
        value = getattr(entity, name)
        if value is None:
            continue
        result[name] = value
    return result


class EntityNotFound(Exception):
    """Raised when an entity lookup finds no results."""

    def __init__(self, entity_type: str, message: str = ""):
        self.entity_type = entity_type
        self.message = message or f"{entity_type} not found"
        super().__init__(self.message)


class EntityHidden(Exception):
    """Raised when a lookup finds only an entity that has been hidden.

    Deliberately not an EntityNotFound: callers that fall back to another
    source when the DB has no record (eg, fcweb's release ES fallback) must not
    do so for a hidden one.
    """

    def __init__(self, entity_type: str, ident):
        self.entity_type = entity_type
        self.ident = ident
        self.message = f"{entity_type} {ident} has been removed"
        super().__init__(self.message)


def visible(qs: QuerySet, include_hidden: bool = False) -> QuerySet:
    """qs without hidden entities, unless include_hidden."""
    return qs if include_hidden else qs.filter(hidden_when__isnull=True)


def check_hidden(entity, entity_type: str, include_hidden: bool = False):
    """entity, or EntityHidden if it is hidden and include_hidden is False."""
    if entity.hidden_when is not None and not include_hidden:
        raise EntityHidden(entity_type, entity.id)
    return entity


def first_match(qs: QuerySet, entity_type: str, message: str,
                include_hidden: bool = False):
    """The first entity in qs, preferring visible ones. Raises EntityNotFound
    if qs is empty, and EntityHidden if every match is hidden."""
    entity = qs.order_by(F("hidden_when").asc(nulls_first=True)).first()
    if entity is None:
        raise EntityNotFound(entity_type, message)
    return check_hidden(entity, entity_type, include_hidden)
