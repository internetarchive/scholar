import base64
import binascii
import re
import uuid

# copypasta from original fatcat

# A fatcat legacy identifier is the 26-character base32 (RFC 4648 alphabet,
# rendered lowercase) segment after the last underscore. Used to pre-filter
# junk before attempting a conversion.
_FCID_RE = re.compile(r"[a-z2-7]{26}", re.IGNORECASE)


def is_legacy_fcid(value: str) -> bool:
    """Return True if value looks like a fatcat legacy identifier.

    A True result guarantees fcid2uuid(value) will succeed, so callers can
    reject obvious junk (eg, crawler noise) with a 400 rather than letting a
    parse error bubble all the way up to a 500.
    """
    return bool(_FCID_RE.fullmatch(value.split("_")[-1]))


def fcid2uuid(fcid: str) -> str:
    """
    Converts a fatcat identifier (base32 encoded string) to a uuid.UUID object

    Raises ValueError if fcid is not a well-formed fatcat identifier.
    """
    b = fcid.split("_")[-1].upper().encode("utf-8")
    if len(b) != 26:
        raise ValueError(f"not a fatcat identifier: {fcid!r}")
    try:
        raw_bytes = base64.b32decode(b + b"======")
    except binascii.Error as e:
        raise ValueError(f"not a fatcat identifier: {fcid!r}") from e
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
