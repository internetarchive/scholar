import uuid

import pytest

from djscholar.fcapi.fcid import fcid2uuid, is_legacy_fcid, uuid2fcid

def test_fcid() -> None:
    test_uuid = uuid.uuid4()
    assert str(test_uuid) == str(fcid2uuid(uuid2fcid(test_uuid)))


def test_is_legacy_fcid() -> None:
    # a real fcid (round-tripped from a uuid) looks like a legacy ident
    fcid = uuid2fcid(uuid.uuid4())
    assert is_legacy_fcid(fcid)
    assert is_legacy_fcid(f"work_{fcid}")  # prefixed form

    # junk does not
    assert not is_legacy_fcid("")
    assert not is_legacy_fcid("foo")
    assert not is_legacy_fcid(str(uuid.uuid4()))  # a plain uuid is not an fcid
    assert not is_legacy_fcid(fcid[:-1])  # too short
    assert not is_legacy_fcid(fcid + "a")  # too long
    assert not is_legacy_fcid("1" * 26)  # right length, wrong alphabet (0/1/8/9)


def test_fcid2uuid_raises_value_error_on_junk() -> None:
    # malformed idents must raise ValueError (not AssertionError) so callers
    # can map them to a 400 rather than a 500
    for bad in ["", "foo", "1" * 26, str(uuid.uuid4())]:
        with pytest.raises(ValueError):
            fcid2uuid(bad)
