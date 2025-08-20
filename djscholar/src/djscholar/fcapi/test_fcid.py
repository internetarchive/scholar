import uuid

from djscholar.fcapi.fcid import fcid2uuid, uuid2fcid

def test_fcid() -> None:
    test_uuid = uuid.uuid4()
    assert str(test_uuid) == str(fcid2uuid(uuid2fcid(test_uuid)))
