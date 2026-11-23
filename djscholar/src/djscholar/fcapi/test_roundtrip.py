"""PUT of exactly what GET returned must leave the entity unchanged.

Clients doing read-modify-write (trawler filling blank fields, for one) rely on
this: GET an entity, change one key, PUT the whole body back. Anything a GET
emits that a PUT then drops, renames or rewrites is silent data loss.
"""
from datetime import date
from http import HTTPStatus
import json

from djscholar.fcapi.test_views import (
    EntityCRUDTestCase,
    ContainerFactory,
    CreatorFactory,
    FileFactory,
    FileURLFactory,
    ReleaseAbstractFactory,
    ReleaseContribFactory,
    ReleaseExtIdFactory,
    ReleaseFactory,
    ReleaseRefFactory,
    WebcaptureCDXFactory,
    WebcaptureFactory,
    WebcaptureURLFactory,
    WorkFactory,
    client,
)


def _canon(v):
    """Order-insensitive form of a GET body, minus the server-maintained
    `updated` timestamps (top level and nested)."""
    if isinstance(v, dict):
        return {k: _canon(x) for k, x in v.items() if k != "updated"}
    if isinstance(v, list):
        return sorted((_canon(x) for x in v), key=lambda x: json.dumps(x, sort_keys=True, default=str))
    return v


class RoundTripTestCase(EntityCRUDTestCase):
    def roundtrip(self, base, ident):
        before = client.get(f"{base}/{ident}")
        self.assertEqual(before.status_code, HTTPStatus.OK, before.content)

        put = client.put(base, data=json.dumps(before.data, default=str),
                         headers=self.auth_headers | {"Content-Type": "application/json"})
        self.assertIn(put.status_code, (HTTPStatus.OK, HTTPStatus.CREATED), put.content)
        self.assertEqual(put.status_code, HTTPStatus.OK,
                         "PUT of an existing entity reported it as newly created")

        after = client.get(f"{base}/{ident}")
        self.assertEqual(after.status_code, HTTPStatus.OK, after.content)
        self.maxDiff = None
        self.assertEqual(_canon(after.data), _canon(before.data))
        return before.data, after.data


class TestReleaseRoundTrip(RoundTripTestCase):
    def test_release_with_everything(self):
        r = ReleaseFactory.create(
            title="t", original_title="ot", subtitle="st", release_type="thesis",
            release_stage="published", release_date=date(2020, 1, 2), release_year=2020,
            volume="1", issue="2", pages="3-4", number="5", version="v1",
            publisher="p", language="pt", license_slug="CC-BY",
            source="test", extra={"etd": {"key": "oai:x", "subjects": ["a", "b"]}},
            refs=[{"index": 0, "title": "a raw ref", "extra": {"doi": "10.1/x"}}],
        )
        ReleaseExtIdFactory.create(release=r, id_type="doi")
        ReleaseExtIdFactory.create(release=r, id_type="hdl")
        creator = CreatorFactory.create()
        ReleaseContribFactory.create(release=r, creator=creator, position=0,
                                     role="author", extra={"seq": "first"})
        ReleaseContribFactory.create(release=r, position=1, role="author")
        ReleaseAbstractFactory.create(release=r, language="en")
        for _ in range(3):
            ReleaseRefFactory.create(release=r, target_release=ReleaseFactory.create())

        self.roundtrip("/release", r.id)

    def test_omitted_children_are_cleared(self):
        """PUT is wholesale: a child list missing from the body means "none".
        This is the trap for clients whose model doesn't carry every child
        list (trawler's fatcat2.Release has no `citations`)."""
        r = ReleaseFactory.create()
        ReleaseRefFactory.create(release=r, target_release=ReleaseFactory.create())
        body = client.get(f"/release/{r.id}").data
        self.assertEqual(len(body["citations"]), 1)
        del body["citations"]
        put = client.put("/release", data=json.dumps(body, default=str),
                         headers=self.auth_headers | {"Content-Type": "application/json"})
        self.assertEqual(put.status_code, HTTPStatus.OK, put.content)
        self.assertEqual(client.get(f"/release/{r.id}").data["citations"], [])

    def test_release_minimal(self):
        r = ReleaseFactory.create(container=None)
        self.roundtrip("/release", r.id)


class TestFileRoundTrip(RoundTripTestCase):
    def test_file_with_urls_and_releases(self):
        f = FileFactory.create(source="test", extra={"k": "v"}, size_bytes=10)
        FileURLFactory.create(file=f, rel="webarchive")
        FileURLFactory.create(file=f, rel="web")
        r1 = ReleaseFactory.create()
        r2 = ReleaseFactory.create()
        f.releases.add(r1, r2)
        self.roundtrip("/file", f.id)


class TestContainerRoundTrip(RoundTripTestCase):
    def test_container(self):
        c = ContainerFactory.create(name="n", publisher="p", extra={"k": "v"})
        self.roundtrip("/container", c.id)


class TestCreatorRoundTrip(RoundTripTestCase):
    def test_creator(self):
        c = CreatorFactory.create(extra={"k": "v"})
        self.roundtrip("/creator", c.id)


class TestWorkRoundTrip(RoundTripTestCase):
    def test_work(self):
        w = WorkFactory.create(extra={"k": "v"})
        self.roundtrip("/work", w.id)


class TestWebcaptureRoundTrip(RoundTripTestCase):
    def test_webcapture(self):
        wc = WebcaptureFactory.create()
        WebcaptureCDXFactory.create(webcapture=wc)
        WebcaptureURLFactory.create(webcapture=wc)
        self.roundtrip("/webcapture", wc.id)


class TestOmittedTimestamps(RoundTripTestCase):
    """What happens when a PUT body leaves out created/updated."""

    def _put_without(self, base, ident, *keys):
        body = client.get(f"{base}/{ident}").data
        for k in keys:
            del body[k]
        return client.put(base, data=json.dumps(body, default=str),
                          headers=self.auth_headers | {"Content-Type": "application/json"})

    def test_release_without_created(self):
        r = ReleaseFactory.create()
        put = self._put_without("/release", r.id, "created", "updated")
        self.assertEqual(put.status_code, HTTPStatus.OK, put.content)

    def test_file_without_created(self):
        f = FileFactory.create()
        put = self._put_without("/file", f.id, "created", "updated")
        self.assertEqual(put.status_code, HTTPStatus.OK, put.content)
