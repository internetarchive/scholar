"""Service layer for the fatcat API.

This package contains the business logic for entity operations. Both the HTTP
API views (fcapi/views.py) and other Django apps (ftsearch, fcweb) should use
these functions rather than querying models directly.

Functions return model instances or raise EntityNotFound.
"""

from django.db import models

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
