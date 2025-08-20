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
