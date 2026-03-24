import base64
import uuid

# copypasta from original fatcat


def fcid2uuid(fcid: str) -> str:
    """
    Converts a fatcat identifier (base32 encoded string) to a uuid.UUID object
    """
    b = fcid.split("_")[-1].upper().encode("utf-8")
    assert len(b) == 26
    raw_bytes = base64.b32decode(b + b"======")
    return str(uuid.UUID(bytes=raw_bytes)).lower()


def uuid2fcid(u: uuid.UUID) -> str:
    """
    Converts a uuid.UUID object to a fatcat identifier (base32 encoded string)
    """
    raw = u.bytes
    return base64.b32encode(raw)[:26].lower().decode("utf-8")


def resolve_ident(ident: str) -> uuid.UUID:
    """
    Accept either a legacy fatcat ident (26-char base32, optionally prefixed
    with 'entity_') or a plain UUID string, and return a uuid.UUID.

    Raises ValueError if the ident cannot be parsed as either form.
    """
    if len(ident) == 26:
        return uuid.UUID(fcid2uuid(ident))

    return uuid.UUID(ident)
