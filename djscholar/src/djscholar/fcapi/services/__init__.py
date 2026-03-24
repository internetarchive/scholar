"""Service layer for the fatcat API.

This package contains the business logic for entity operations. Both the HTTP
API views (fcapi/views.py) and other Django apps (ftsearch, fcweb) should use
these functions rather than querying models directly.

Functions return model instances or raise EntityNotFound.
"""


class EntityNotFound(Exception):
    """Raised when an entity lookup finds no results."""

    def __init__(self, entity_type: str, message: str = ""):
        self.entity_type = entity_type
        self.message = message or f"{entity_type} not found"
        super().__init__(self.message)
