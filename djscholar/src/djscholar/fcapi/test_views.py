from datetime import datetime, timedelta
from http import HTTPStatus
from urllib.parse import quote_plus
from uuid import uuid4
import random
import zoneinfo

import dateutil
import factory
import surt
import time
from faker import Faker
from faker.providers import file, internet, misc, person, lorem
from django.contrib.auth.hashers import (
    make_password,
)
from django.contrib.auth.models import User
from django.db import connection
from django.test import TestCase
from factory.django import DjangoModelFactory
from ninja.testing import TestClient
from ninja_apikey.models import APIKey

from djscholar.fcapi.fcid import uuid2fcid
import djscholar.fcapi.models as m
import djscholar.fcapi.views as v
from djscholar.fcapi.faker_providers import ExtIDProvider

client = TestClient(v.v2api)
factory.Faker.add_provider(ExtIDProvider)
factory.Faker.add_provider(misc.Provider)
factory.Faker.add_provider(file.Provider)
factory.Faker.add_provider(person.Provider)
factory.Faker.add_provider(internet.Provider)
factory.Faker.add_provider(lorem.Provider)

# NB factory_boy uses faker in an annoying way and it means we can't use faker
# generators in LazyAttributes. this is a dumb hack for that.
realfaker = Faker()
realfaker.add_provider(ExtIDProvider)


class ContainerFactory(DjangoModelFactory):
    issnl = factory.Faker("issn")
    issne = factory.Faker("issn")
    issnp = factory.Faker("issn")
    wikidata_qid = factory.Faker("wikidata_qid")
    updated = factory.LazyFunction(lambda: datetime.now(zoneinfo.ZoneInfo("UTC")))
    created = factory.LazyFunction(lambda: datetime.now(zoneinfo.ZoneInfo("UTC")))

    class Meta:
        model = m.Container


class WorkFactory(DjangoModelFactory):
    updated = factory.LazyFunction(lambda: datetime.now(zoneinfo.ZoneInfo("UTC")))
    created = factory.LazyFunction(lambda: datetime.now(zoneinfo.ZoneInfo("UTC")))

    class Meta:
        model = m.Work


class ReleaseExtIdFactory(DjangoModelFactory):
    id_value = factory.LazyAttribute(lambda s: getattr(realfaker, s.id_type)())

    class Meta:
        model = m.ReleaseExtId


class ReleaseRefFactory(DjangoModelFactory):
    position = factory.LazyFunction(lambda: random.randint(0, 100))

    class Meta:
        model = m.ReleaseRef


class ReleaseAbstractFactory(DjangoModelFactory):
    mimetype = "text/plain"
    sha1 = factory.Faker("sha1")
    content = factory.Faker("paragraph", ext_word_list=["screw", "flanders"])

    class Meta:
        model = m.ReleaseAbstract


class ReleaseFactory(DjangoModelFactory):
    updated = factory.LazyFunction(lambda: datetime.now(zoneinfo.ZoneInfo("UTC")))
    created = factory.LazyFunction(lambda: datetime.now(zoneinfo.ZoneInfo("UTC")))
    work = factory.SubFactory(WorkFactory)
    container = factory.SubFactory(ContainerFactory)

    class Meta:
        model = m.Release


class FileFactory(DjangoModelFactory):
    updated = factory.LazyFunction(lambda: datetime.now(zoneinfo.ZoneInfo("UTC")))
    created = factory.LazyFunction(lambda: datetime.now(zoneinfo.ZoneInfo("UTC")))

    md5 = factory.Faker("md5")
    sha1 = factory.Faker("sha1")
    sha256 = factory.Faker("sha256")
    mimetype = factory.Faker("mime_type")

    class Meta:
        model = m.File


class FileURLFactory(DjangoModelFactory):
    file = factory.SubFactory(FileFactory)
    rel = "webarchive"
    url = factory.Faker("uri")

    class Meta:
        model = m.FileURL


class CreatorFactory(DjangoModelFactory):
    updated = factory.LazyFunction(lambda: datetime.now(zoneinfo.ZoneInfo("UTC")))
    created = factory.LazyFunction(lambda: datetime.now(zoneinfo.ZoneInfo("UTC")))
    given_name = factory.Faker("first_name")
    surname = factory.Faker("last_name")
    display_name = factory.LazyAttribute(lambda c: f"{c.given_name} {c.surname}")
    orcid = factory.Faker("orcid")
    class Meta:
        model = m.Creator


class ReleaseContribFactory(DjangoModelFactory):
    raw_name = f"{factory.Faker('first_name')} {factory.Faker('last_name')}"
    class Meta:
        model = m.ReleaseContrib


class WebcaptureFactory(DjangoModelFactory):
    updated = factory.LazyFunction(lambda: datetime.now(zoneinfo.ZoneInfo("UTC")))
    created = factory.LazyFunction(lambda: datetime.now(zoneinfo.ZoneInfo("UTC")))
    captured = factory.LazyFunction(lambda: datetime.now(zoneinfo.ZoneInfo("UTC")))
    original_url = factory.Faker("uri")
    release = factory.SubFactory(ReleaseFactory)

    class Meta:
        model = m.Webcapture


class WebcaptureCDXFactory(DjangoModelFactory):
    url = factory.Faker("uri")
    surt = factory.LazyAttribute(lambda c: surt.surt(c.url))
    mimetype = factory.Faker("mime_type")
    captured = factory.LazyFunction(lambda: datetime.now(zoneinfo.ZoneInfo("UTC")))
    sha1 = factory.Faker("sha1")
    sha256 = factory.Faker("sha256")
    status_code = HTTPStatus.OK
    size_bytes = 1

    class Meta:
        model = m.WebcaptureCDX


class WebcaptureURLFactory(DjangoModelFactory):
    rel = "warc"
    url = factory.Faker("uri")

    class Meta:
        model = m.WebcaptureURL


class UserFactory(DjangoModelFactory):
    class Meta:
        model = User


class APIKeyFactory(DjangoModelFactory):
    class Meta:
        model = APIKey


class EntityCRUDTestCase(TestCase):
    base = ""  # e.g. /release

    # NB I wanted to ensure auth on the CUD endpoints and thought it would be
    # nice to have that in a parent class. pytest, of course, finds the parent
    # class's tests. I'm being gross and just letting the parent functions run
    # as tests which is not what I want at all but I'm punting on it for now.

    @property
    def lookup(self):
        return f"{self.base}/lookup"

    @property
    def create(self):
        return self.base

    @property
    def bulk_create(self):
        # NB I hate this
        return self.base + "s"

    @property
    def get(self):
        return self.base

    @property
    def update(self):
        return self.base

    @property
    def delete(self):
        return self.base

    def test_create_auth(self):
        if self.base == "":
            return
        response = client.post(self.create)
        self.assertEqual(response.status_code, HTTPStatus.UNAUTHORIZED)

    def test_bulk_create_auth(self):
        if self.base == "":
            return
        response = client.post(self.bulk_create)
        self.assertEqual(response.status_code, HTTPStatus.UNAUTHORIZED)

    def test_update_auth(self):
        if self.base == "":
            return
        response = client.put(self.update)
        self.assertEqual(response.status_code, HTTPStatus.UNAUTHORIZED)

    def test_delete_auth(self):
        if self.base == "":
            return
        response = client.delete(f"{self.delete}/{uuid4()}")
        self.assertEqual(response.status_code, HTTPStatus.UNAUTHORIZED)

    def setUp(self):
        user = User.objects.create_user(username="test", password="test")
        prefix = "prefix"
        key = "test_api_key"
        encoded = make_password(key)
        APIKey.objects.create(user=user, prefix=prefix, hashed_key=encoded)
        self.auth_headers = {"X-API-Key": f"{prefix}.{key}"}


class TestReleaseRoutes(EntityCRUDTestCase):
    base = "/release"

    def setUp(self):
        super().setUp()
        self.entity = ReleaseFactory.create()
        self.reis = []
        for id_type, _ in m.RELEASE_EXT_ID_TYPES:
            self.reis.append(ReleaseExtIdFactory.create(release=self.entity, id_type=id_type))

    def test_lookup_fulltext(self):
        doi = "10.1111/xxxxxx.111.1111"
        response = client.get(f"{self.lookup}/fulltext?id_type=doi&id_value={doi}")
        self.assertEqual(response.status_code, HTTPStatus.NOT_FOUND)

        doi = quote_plus([rei for rei in self.reis if rei.id_type == "doi"][0].id_value)
        file = FileFactory.create()
        file.releases.set([self.entity])
        file.urls.set([FileURLFactory.create(rel="whatever")])
        file_no_urls = FileFactory.create()
        file_no_urls.releases.set([self.entity])

        # finds the only one
        response = client.get(f"{self.lookup}/fulltext?id_type=doi&id_value={doi}")
        self.assertEqual(response.status_code, HTTPStatus.FOUND)
        self.assertEqual(response.headers["Location"], file.urls.all()[0].url)

        # prefers webarchive
        file.urls.add(FileURLFactory.create(url="http://tilde.town/lol"))
        response = client.get(f"{self.lookup}/fulltext?id_type=doi&id_value={doi}")
        self.assertEqual(response.status_code, HTTPStatus.FOUND)
        self.assertEqual(response.headers["Location"], "http://tilde.town/lol")

        # prefers WBM
        file.urls.add(FileURLFactory.create(url="http://web.archive.org/lolfoobar"))
        response = client.get(f"{self.lookup}/fulltext?id_type=doi&id_value={doi}")
        self.assertEqual(response.status_code, HTTPStatus.FOUND)
        self.assertEqual(response.headers["Location"], "http://web.archive.org/lolfoobar")

    def test_lookup(self):
        doi = "10.1111/xxxxxx.111.1111"
        response = client.get(f"{self.lookup}?id_type=doi&id_value={doi}")
        self.assertEqual(response.status_code, HTTPStatus.NOT_FOUND)

        for rei in self.reis:
            v = quote_plus(rei.id_value)
            response = client.get(
                    f"{self.lookup}?id_type={rei.id_type}&id_value={v}")
            self.assertEqual(response.status_code, HTTPStatus.OK)
            self.assertEqual(response.data["id"], str(self.entity.id))

        legacy_ident = uuid2fcid(self.entity.id)
        response = client.get(f"{self.lookup}?id_type=legacy_ident&id_value={legacy_ident}")
        self.assertEqual(response.status_code, HTTPStatus.OK)
        self.assertEqual(response.data["id"], str(self.entity.id))

    def test_get(self):
        response = client.get(f"{self.get}/{self.entity.id}")
        self.assertEqual(response.status_code, HTTPStatus.OK)
        self.assertEqual(response.data["id"], str(self.entity.id))

    def test_get_container(self):
        no_container = ReleaseFactory.build()
        no_container.container = None
        no_container.work.save()
        no_container.save()
        response = client.get(f"{self.get}/{no_container.id}/container")
        self.assertEqual(response.status_code, HTTPStatus.NOT_FOUND)

        response = client.get(f"{self.get}/{self.entity.id}/container")
        self.assertEqual(response.status_code, HTTPStatus.OK)
        self.assertEqual(response.data["id"], str(self.entity.container.id))

    def test_get_work(self):
        response = client.get(f"{self.get}/{self.entity.id}/container")
        self.assertEqual(response.status_code, HTTPStatus.OK)
        self.assertEqual(response.data["id"], str(self.entity.container.id))

    def test_get_files(self):
        es = []
        for _ in range(4):
            e = FileFactory.create()
            e.releases.set([self.entity])
            es.append(e)

        response = client.get(f"{self.get}/{self.entity.id}/files")
        self.assertEqual(response.status_code, HTTPStatus.OK)
        self.assertEqual(response.data["count"], len(es))
        self.assertSetEqual(set([d['id'] for d in response.data["items"]]),
                            set([str(e.id) for e in es]))

    def test_get_contribs(self):
        contribs = []
        for x in range(4):
            c = ReleaseContribFactory.build()
            if x % 2 == 0:
                c.creator = CreatorFactory.create()
            c.release = self.entity
            c.save()
            contribs.append(c)

        response = client.get(f"{self.get}/{self.entity.id}/contribs")
        self.assertEqual(response.status_code, HTTPStatus.OK)
        self.assertEqual(response.data["count"], len(contribs))
        self.assertSetEqual(set([d['raw_name'] for d in response.data["items"]]),
                            set([str(c.raw_name) for c in contribs]))

    def test_get_webcaptures(self):
        webcaptures = []
        for _ in range(4):
            wc = v.WebcaptureSchema.from_orm(WebcaptureFactory.create(release_id=self.entity.id))
            wc.urls = WebcaptureURLFactory.create_batch(2, webcapture_id=wc.id)
            wc.cdx_lines = WebcaptureCDXFactory.create_batch(4, webcapture_id=wc.id)
            webcaptures.append(wc)

        response = client.get(f"{self.get}/{self.entity.id}/webcaptures")
        self.assertEqual(response.status_code, HTTPStatus.OK)

        self.assertEqual(response.data["count"], 4)
        self.assertEqual(len(response.data["items"][0]["urls"]), 2)
        self.assertEqual(len(response.data["items"][0]["cdx_lines"]), 4)

    def test_create_with_refs(self):
        entity = ReleaseFactory.build()
        entity.container.save()
        entity.work = None
        entity.refs = {"foo": "bar", "baz": [{"quux": "florp"}]}

        data = v.ReleaseSchema.from_orm(entity).model_dump_json()
        response = client.post(self.create, data=data, headers=self.auth_headers)
        self.assertEqual(response.status_code, HTTPStatus.CREATED)

        r = m.Release.objects.filter(id=entity.id)[0]
        self.assertTrue(r.refs == {"foo": "bar", "baz": [{"quux": "florp"}]})

    def test_create_with_work(self):
        entity = ReleaseFactory.build()
        entity.container.save()
        entity.work.save()

        data = v.ReleaseSchema.from_orm(entity).model_dump_json()

        response = client.post(self.create, data=data, headers=self.auth_headers)
        self.assertEqual(response.status_code, HTTPStatus.CREATED)

        es = m.Release.objects.filter(id=entity.id)
        self.assertEqual(len(es), 1)

        response = client.post("/release", data=data, headers=self.auth_headers)
        self.assertEqual(response.status_code, HTTPStatus.UNPROCESSABLE_ENTITY)

    def test_create_without_work(self):
        entity = ReleaseFactory.build()
        entity.container.save()
        entity.work = None

        data = v.ReleaseSchema.from_orm(entity).model_dump_json()
        response = client.post(self.create, data=data, headers=self.auth_headers)
        self.assertEqual(response.status_code, HTTPStatus.CREATED)

        r = m.Release.objects.filter(id=entity.id)[0]
        self.assertTrue(r.work is not None)

    def test_update_without_work(self):
        entity = ReleaseFactory.build()
        entity.container.save()
        entity.work.save()
        entity.save()

        rschema = v.ReleaseSchema.from_orm(entity)
        rschema.work_id = None

        data = rschema.model_dump_json()

        response = client.put(self.update, data=data, headers=self.auth_headers)
        self.assertEqual(response.status_code, HTTPStatus.UNPROCESSABLE_CONTENT)

    def test_upsert_without_work(self):
        entity = ReleaseFactory.build()
        entity.container.save()
        entity.work = None

        data = v.ReleaseSchema.from_orm(entity).model_dump_json()

        response = client.put(self.update, data=data, headers=self.auth_headers)
        self.assertEqual(response.status_code, HTTPStatus.CREATED)

        r = m.Release.objects.filter(id=entity.id)[0]
        self.assertTrue(r.work is not None)

    def test_bulk_create_without_work(self):
        rin = []
        for x in range(100):
            r = ReleaseFactory.build()
            if x % 2 == 0:
                r.work.save()
            else:
                r.work = None
            r.container.save()
            rin.append(v.ReleaseSchema.from_orm(r))
        data = "["+",".join([r.model_dump_json() for r in rin])+"]"

        response = client.post(self.bulk_create, data=data, headers=self.auth_headers)
        self.assertEqual(response.status_code, HTTPStatus.CREATED)

        for r in rin:
            rs = m.Release.objects.filter(id=r.id)
            self.assertEqual(len(rs), 1)

    def test_get_with_children(self):
        r = ReleaseFactory.create()
        r.work.save()
        r.container.save()

        extids = []
        extids.append(ReleaseExtIdFactory.create(release=r, id_type="doi"))
        extids.append(ReleaseExtIdFactory.create(release=r, id_type="pmcid"))
        contribs = ReleaseContribFactory.create_batch(4, release=r)
        abstracts = ReleaseAbstractFactory.create_batch(4, release=r)
        citations = []
        for _ in range(4):
            tr = ReleaseFactory.create()
            citations.append(ReleaseRefFactory.create(release=r, target_release=tr))

        response = client.get(f"{self.get}/{r.id}")
        self.assertEqual(response.status_code, HTTPStatus.OK)

        self.assertEqual(len(response.data["extids"]), len(extids))
        self.assertSetEqual(
                set([(e["id_type"], e["id_value"]) for e in response.data["extids"]]),
                set([(e.id_type, e.id_value) for e in extids]))

        self.assertEqual(len(response.data["contribs"]), len(contribs))
        self.assertSetEqual(
                set([c["raw_name"] for c in response.data["contribs"]]),
                set([c.raw_name for c in contribs]))

        self.assertEqual(len(response.data["citations"]), len(citations))
        self.assertSetEqual(
                set([c["target_release_id"] for c in response.data["citations"]]),
                set([str(c.target_release_id) for c in citations]))

        self.assertEqual(len(response.data["abstracts"]), len(abstracts))
        self.assertSetEqual(
                set([a["sha1"] for a in response.data["abstracts"]]),
                set([a.sha1 for a in abstracts]))

    def test_create_with_children(self):
        r = ReleaseFactory.build()
        r.work = None
        r.container.save()

        extids = []
        extids.append(ReleaseExtIdFactory.build(id_type="doi"))
        extids.append(ReleaseExtIdFactory.build(id_type="pmcid"))
        contribs = ReleaseContribFactory.build_batch(4)
        abstracts = ReleaseAbstractFactory.build_batch(4)
        citations = []
        for _ in range(4):
            tr = ReleaseFactory.create()
            citations.append(ReleaseRefFactory.build(target_release=tr))

        rschema = v.ReleaseSchema.from_orm(r)

        rschema.extids = [v.ReleaseExtIdSchema.from_orm(e) for e in extids]
        rschema.contribs = [v.ReleaseContribSchema.from_orm(c) for c in contribs]
        rschema.abstracts = [v.ReleaseAbstractSchema.from_orm(a) for a in abstracts]
        rschema.citations = [v.ReleaseRefSchema.from_orm(c) for c in citations]

        data = rschema.model_dump_json()

        response = client.post(self.create, data=data, headers=self.auth_headers)
        self.assertEqual(response.status_code, HTTPStatus.CREATED)

        response = client.get(f"{self.get}/{r.id}")
        self.assertEqual(response.status_code, HTTPStatus.OK)

        self.assertEqual(len(response.data["extids"]), len(extids))
        self.assertSetEqual(
                set([(e["id_type"], e["id_value"]) for e in response.data["extids"]]),
                set([(e.id_type, e.id_value) for e in extids]))

        self.assertEqual(len(response.data["contribs"]), len(contribs))
        self.assertSetEqual(
                set([c["raw_name"] for c in response.data["contribs"]]),
                set([c.raw_name for c in contribs]))

        self.assertEqual(len(response.data["citations"]), len(citations))
        self.assertSetEqual(
                set([c["target_release_id"] for c in response.data["citations"]]),
                set([str(c.target_release_id) for c in citations]))

        self.assertEqual(len(response.data["abstracts"]), len(abstracts))
        self.assertSetEqual(
                set([a["sha1"] for a in response.data["abstracts"]]),
                set([a.sha1 for a in abstracts]))

    def test_bulk_create_with_children(self):
        rschemas = []
        for _ in range(10):
            r = ReleaseFactory.build()
            r.work.save()
            r.container.save()

            extids = []
            extids.append(ReleaseExtIdFactory.build(id_type="doi"))
            extids.append(ReleaseExtIdFactory.build(id_type="pmcid"))
            contribs = ReleaseContribFactory.build_batch(4)
            abstracts = ReleaseAbstractFactory.build_batch(4)
            citations = []
            for _ in range(4):
                tr = ReleaseFactory.create()
                citations.append(ReleaseRefFactory.build(target_release=tr))

            rschema = v.ReleaseSchema.from_orm(r)

            rschema.extids = [v.ReleaseExtIdSchema.from_orm(e) for e in extids]
            rschema.contribs = [v.ReleaseContribSchema.from_orm(c) for c in contribs]
            rschema.abstracts = [v.ReleaseAbstractSchema.from_orm(a) for a in abstracts]
            rschema.citations = [v.ReleaseRefSchema.from_orm(c) for c in citations]

        rschemas.append(rschema)
        data = "["+",".join([r.model_dump_json() for r in rschemas])+"]"

        response = client.post(self.bulk_create, data=data, headers=self.auth_headers)
        self.assertEqual(response.status_code, HTTPStatus.CREATED)

        for r in rschemas:
            response = client.get(f"{self.get}/{r.id}")
            self.assertEqual(response.status_code, HTTPStatus.OK)

            self.assertEqual(len(response.data["extids"]), len(r.extids))
            self.assertSetEqual(
                    set([(e["id_type"], e["id_value"]) for e in response.data["extids"]]),
                    set([(e.id_type, e.id_value) for e in r.extids]))

            self.assertEqual(len(response.data["contribs"]), len(r.contribs))
            self.assertSetEqual(
                    set([c["raw_name"] for c in response.data["contribs"]]),
                    set([c.raw_name for c in r.contribs]))

            self.assertEqual(len(response.data["citations"]), len(r.citations))
            self.assertSetEqual(
                    set([c["target_release_id"] for c in response.data["citations"]]),
                    set([str(c.target_release_id) for c in r.citations]))

            self.assertEqual(len(response.data["abstracts"]), len(r.abstracts))
            self.assertSetEqual(
                    set([a["sha1"] for a in response.data["abstracts"]]),
                    set([a.sha1 for a in r.abstracts]))

    def test_update_with_children(self):
        r = ReleaseFactory.create()

        extids = []
        extids.append(ReleaseExtIdFactory.create(release=r, id_type="doi"))
        extids.append(ReleaseExtIdFactory.create(release=r, id_type="pmcid"))
        contribs = ReleaseContribFactory.create_batch(4, release=r)
        abstracts = ReleaseAbstractFactory.create_batch(4, release=r)
        citations = []
        for _ in range(4):
            citations.append(ReleaseRefFactory.build(
                target_release=ReleaseFactory.create()))

        r.title = "new title"
        extids[0] = ReleaseExtIdFactory.build(id_type="dblp")
        contribs[0] = ReleaseContribFactory.build()
        abstracts[0] = ReleaseAbstractFactory.build()
        citations[0] = ReleaseRefFactory.build(target_release=ReleaseFactory.create())

        rschema = v.ReleaseSchema.from_orm(r)
        rschema.extids = extids
        rschema.contribs = contribs
        rschema.abstracts = abstracts
        rschema.citations = citations

        data = rschema.model_dump_json()

        response = client.put(self.update, data=data, headers=self.auth_headers)
        self.assertEqual(response.status_code, HTTPStatus.OK)

        response = client.get(f"{self.get}/{r.id}")
        self.assertEqual(response.status_code, HTTPStatus.OK)

        self.assertEqual(response.data["title"], r.title)

        self.assertEqual(len(response.data["extids"]), len(extids))
        self.assertSetEqual(
                set([(e["id_type"], e["id_value"]) for e in response.data["extids"]]),
                set([(e.id_type, e.id_value) for e in extids]))

        self.assertEqual(len(response.data["contribs"]), len(contribs))
        self.assertSetEqual(
                set([c["raw_name"] for c in response.data["contribs"]]),
                set([c.raw_name for c in contribs]))

        self.assertEqual(len(response.data["citations"]), len(citations))
        self.assertSetEqual(
                set([c["target_release_id"] for c in response.data["citations"]]),
                set([str(c.target_release_id) for c in citations]))

        self.assertEqual(len(response.data["abstracts"]), len(abstracts))
        self.assertSetEqual(
                set([a["sha1"] for a in response.data["abstracts"]]),
                set([a.sha1 for a in abstracts]))

    def test_upsert_with_children(self):
        r = ReleaseFactory.build()
        r.work = None
        r.container.save()

        extids = []
        extids.append(ReleaseExtIdFactory.build(id_type="doi"))
        extids.append(ReleaseExtIdFactory.build(id_type="pmcid"))
        contribs = ReleaseContribFactory.build_batch(4)
        abstracts = ReleaseAbstractFactory.build_batch(4)
        citations = []
        for _ in range(4):
            tr = ReleaseFactory.create()
            citations.append(ReleaseRefFactory.build(target_release=tr))

        rschema = v.ReleaseSchema.from_orm(r)

        rschema.extids = [v.ReleaseExtIdSchema.from_orm(e) for e in extids]
        rschema.contribs = [v.ReleaseContribSchema.from_orm(c) for c in contribs]
        rschema.abstracts = [v.ReleaseAbstractSchema.from_orm(a) for a in abstracts]
        rschema.citations = [v.ReleaseRefSchema.from_orm(c) for c in citations]

        data = rschema.model_dump_json()

        response = client.put(self.update, data=data, headers=self.auth_headers)
        self.assertEqual(response.status_code, HTTPStatus.CREATED)

        response = client.get(f"{self.get}/{r.id}")
        self.assertEqual(response.status_code, HTTPStatus.OK)

        self.assertEqual(len(response.data["extids"]), len(extids))
        self.assertSetEqual(
                set([(e["id_type"], e["id_value"]) for e in response.data["extids"]]),
                set([(e.id_type, e.id_value) for e in extids]))

        self.assertEqual(len(response.data["contribs"]), len(contribs))
        self.assertSetEqual(
                set([c["raw_name"] for c in response.data["contribs"]]),
                set([c.raw_name for c in contribs]))

        self.assertEqual(len(response.data["citations"]), len(citations))
        self.assertSetEqual(
                set([c["target_release_id"] for c in response.data["citations"]]),
                set([str(c.target_release_id) for c in citations]))

        self.assertEqual(len(response.data["abstracts"]), len(abstracts))
        self.assertSetEqual(
                set([a["sha1"] for a in response.data["abstracts"]]),
                set([a.sha1 for a in abstracts]))

    def test_bulk_create(self):
        rin = []
        for _ in range(100):
            r = ReleaseFactory.build()
            r.work.save()
            r.container.save()
            rin.append(v.ReleaseSchema.from_orm(r))
        data = "["+",".join([r.model_dump_json() for r in rin])+"]"

        response = client.post(self.bulk_create, data=data, headers=self.auth_headers)
        self.assertEqual(response.status_code, HTTPStatus.CREATED)

        for r in rin:
            rs = m.Release.objects.filter(id=r.id)
            self.assertEqual(len(rs), 1)

    def test_update(self):
        entity = ReleaseFactory.build()
        entity.work.save()
        entity.container.save()

        data = v.ReleaseSchema.from_orm(entity).model_dump_json()
        response = client.put(self.update, data=data, headers=self.auth_headers)
        self.assertEqual(response.status_code, HTTPStatus.CREATED)
        es = m.Release.objects.filter(id=entity.id)
        self.assertEqual(len(es), 1)

        new_work = m.Work()
        new_work.save()

        rschema = v.ReleaseSchema.from_orm(self.entity)
        rschema.title = "updated title"
        rschema.work_id = new_work.id

        data = rschema.model_dump_json()
        response = client.put(self.update, data=data, headers=self.auth_headers)
        self.assertEqual(response.status_code, HTTPStatus.OK)

        es = m.Release.objects.filter(id=self.entity.id)
        self.assertEqual(len(es), 1)
        self.assertEqual(es[0].title, rschema.title)
        self.assertEqual(es[0].work.id, new_work.id)

    def test_delete(self):
        unsaved = ReleaseFactory.build()

        response = client.delete(f"{self.delete}/{unsaved.id}", headers=self.auth_headers)
        self.assertEqual(response.status_code, HTTPStatus.NOT_FOUND)

        response = client.delete(f"{self.delete}/{self.entity.id}", headers=self.auth_headers)
        self.assertEqual(response.status_code, HTTPStatus.OK)
        self.assertEqual(response.data["id"], str(self.entity.id))

        response = client.delete(f"{self.delete}/{self.entity.id}", headers=self.auth_headers)
        self.assertEqual(response.status_code, HTTPStatus.NOT_FOUND)


class TestContainerRoutes(EntityCRUDTestCase):
    base = "/container"

    def setUp(self):
        super().setUp()
        self.entity = ContainerFactory.create()

    def test_get(self):
        response = client.get(f"{self.get}/{self.entity.id}")
        self.assertEqual(response.status_code, HTTPStatus.OK)
        self.assertEqual(response.data["id"], str(self.entity.id))

    def test_lookup(self):
        issnl = "1111-2222"
        response = client.get(f"{self.lookup}?id_type=issnl&id_value={issnl}")
        self.assertEqual(response.status_code, HTTPStatus.NOT_FOUND)

        keys = [("wikidata_qid", self.entity.wikidata_qid),
                ("issnl", self.entity.issnl),
                ("issne", self.entity.issne),
                ("issnp", self.entity.issnp),
                ]

        for id_type, id_value in keys:
            response = client.get(
                    f"{self.lookup}?id_type={id_type}&id_value={id_value}")
            self.assertEqual(response.status_code, HTTPStatus.OK)
            self.assertEqual(response.data['id'], str(self.entity.id))

        legacy_ident = uuid2fcid(self.entity.id)
        response = client.get(
                f"{self.lookup}?id_type=legacy_ident&id_value={legacy_ident}")
        self.assertEqual(response.status_code, HTTPStatus.OK)
        self.assertEqual(response.data["id"], str(self.entity.id))

    def test_get_releases(self):
        c = ContainerFactory.create()
        response = client.get(f"{self.get}/{c.id}/releases")
        self.assertEqual(response.status_code, HTTPStatus.OK)
        self.assertEqual(response.data["count"], 0)

        rs = []
        for _ in range(4):
            r = ReleaseFactory.build()
            r.container = self.entity
            r.work = WorkFactory.create()
            r.save()
            rs.append(r)

        response = client.get(f"{self.get}/{self.entity.id}/releases")
        self.assertEqual(response.status_code, HTTPStatus.OK)
        self.assertEqual(response.data["count"], len(rs))
        self.assertSetEqual(set([d['id'] for d in response.data["items"]]),
                            set([str(r.id) for r in rs]))

    def test_create(self):
        c = ContainerFactory.build()
        data = v.ContainerSchema.from_orm(c).model_dump_json()
        response = client.post(self.create, data=data, headers=self.auth_headers)
        self.assertEqual(response.status_code, HTTPStatus.CREATED)

        cs = m.Container.objects.filter(id=c.id)
        self.assertEqual(len(cs), 1)

        response = client.post(self.create, data=data, headers=self.auth_headers)
        self.assertEqual(response.status_code, HTTPStatus.UNPROCESSABLE_ENTITY)

    def test_bulk_create(self):
        cs = [v.ContainerSchema.from_orm(ContainerFactory.build()) for _ in range(100)]
        data = "["+",".join([c.model_dump_json() for c in cs])+"]"

        response = client.post(self.bulk_create, data=data, headers=self.auth_headers)
        self.assertEqual(response.status_code, HTTPStatus.CREATED)

        for c in cs:
            cs = m.Container.objects.filter(id=c.id)
            self.assertEqual(len(cs), 1)

    def test_update(self):
        entity = ContainerFactory.build()
        data = v.ContainerSchema.from_orm(entity).model_dump_json()
        response = client.put(self.update, data=data, headers=self.auth_headers)
        self.assertEqual(response.status_code, HTTPStatus.CREATED)
        cs = m.Container.objects.filter(id=entity.id)
        self.assertEqual(len(cs), 1)

        new_name = "updated name"
        self.entity.name = new_name
        data = v.ContainerSchema.from_orm(self.entity).model_dump_json()
        response = client.put(self.update, data=data, headers=self.auth_headers)
        self.assertEqual(response.status_code, HTTPStatus.OK)

        cs = m.Container.objects.filter(id=self.entity.id)
        self.assertEqual(len(cs), 1)
        self.assertEqual(cs[0].name, new_name)


    def test_delete(self):
        unsaved = ContainerFactory.build()

        response = client.delete(f"{self.delete}/{unsaved.id}", headers=self.auth_headers)
        self.assertEqual(response.status_code, HTTPStatus.NOT_FOUND)

        response = client.delete(f"{self.delete}/{self.entity.id}", headers=self.auth_headers)
        self.assertEqual(response.status_code, HTTPStatus.OK)
        self.assertEqual(response.data["id"], str(self.entity.id))

        response = client.delete(f"{self.delete}/{self.entity.id}", headers=self.auth_headers)
        self.assertEqual(response.status_code, HTTPStatus.NOT_FOUND)


class TestWorkRoutes(EntityCRUDTestCase):
    base = "/work"

    def setUp(self):
        super().setUp()
        self.entity = WorkFactory.create()

    def test_create_auth(self):
        # works can only be created via release creation so there's no route for this
        response = client.post(self.create, headers=self.auth_headers)
        self.assertEqual(response.status_code, HTTPStatus.METHOD_NOT_ALLOWED)
        pass

    def test_bulk_create_auth(self):
        # works can only be created via release creation so there's no route for this
        response = client.post(self.bulk_create, headers=self.auth_headers)
        self.assertEqual(response.status_code, HTTPStatus.METHOD_NOT_ALLOWED)

    def test_get(self):
        response = client.get(f"{self.get}/{self.entity.id}")
        self.assertEqual(response.status_code, HTTPStatus.OK)
        self.assertEqual(response.data["id"], str(self.entity.id))

    def test_lookup(self):
        legacy_ident = uuid2fcid(self.entity.id)
        response = client.get(
                f"{self.lookup}?id_type=legacy_ident&id_value={legacy_ident}")
        self.assertEqual(response.status_code, HTTPStatus.OK)
        self.assertEqual(response.data["id"], str(self.entity.id))

    def test_get_releases(self):
        rs = []
        for _ in range(4):
            r = ReleaseFactory.build()
            r.work = self.entity
            r.container = None
            r.save()
            rs.append(r)

        response = client.get(f"{self.get}/{self.entity.id}/releases")
        self.assertEqual(response.status_code, HTTPStatus.OK)
        self.assertSetEqual(set([r['id'] for r in response.data["items"]]),
                            set([str(r.id) for r in rs]))

    def test_delete(self):
        unsaved = WorkFactory.build()

        response = client.delete(f"{self.delete}/{unsaved.id}", headers=self.auth_headers)
        self.assertEqual(response.status_code, HTTPStatus.NOT_FOUND)

        response = client.delete(f"{self.delete}/{self.entity.id}", headers=self.auth_headers)
        self.assertEqual(response.status_code, HTTPStatus.OK)
        self.assertEqual(response.data["id"], str(self.entity.id))

        response = client.delete(f"{self.delete}/{self.entity.id}", headers=self.auth_headers)
        self.assertEqual(response.status_code, HTTPStatus.NOT_FOUND)

    def test_update(self):
        entity = WorkFactory.build()
        data = v.WorkSchema.from_orm(entity).model_dump_json()

        response = client.put(self.update, data=data, headers=self.auth_headers)
        self.assertEqual(response.status_code, HTTPStatus.CREATED)
        es = m.Work.objects.filter(id=entity.id)
        self.assertEqual(len(es), 1)

        new_reason = "hidden for test"
        self.entity.hidden_reason = new_reason
        hidden_when = datetime.now(zoneinfo.ZoneInfo("UTC"))
        self.entity.hidden_when = hidden_when
        data = v.WorkSchema.from_orm(self.entity).model_dump_json()
        response = client.put(self.update, data=data, headers=self.auth_headers)
        self.assertEqual(response.status_code, HTTPStatus.OK)

        es = m.Work.objects.filter(id=self.entity.id)
        self.assertEqual(len(es), 1)
        self.assertEqual(es[0].hidden_reason, new_reason)
        self.assertEqual(es[0].hidden_when, hidden_when)


class TestCreatorRoutes(EntityCRUDTestCase):
    base = "/creator"

    def setUp(self):
        super().setUp()
        self.entity = CreatorFactory.create()

    def test_get(self):
        response = client.get(f"{self.get}/{self.entity.id}")
        self.assertEqual(response.status_code, HTTPStatus.OK)
        self.assertEqual(response.data["id"], str(self.entity.id))

    def test_lookup(self):
        orcid = "abc123"
        response = client.get(f"{self.lookup}?id_type=orcid&id_value={orcid}")
        self.assertEqual(response.status_code, HTTPStatus.NOT_FOUND)

        self.entity.orcid = "TODO make an orcid generator"
        self.entity.save()

        keys = [("orcid", self.entity.orcid),
                ]

        for id_type, id_value in keys:
            response = client.get(
                    f"{self.lookup}?id_type={id_type}&id_value={id_value}")
            self.assertEqual(response.status_code, HTTPStatus.OK)
            self.assertEqual(response.data['id'], str(self.entity.id))

        legacy_ident = uuid2fcid(self.entity.id)
        response = client.get(
                f"{self.lookup}?id_type=legacy_ident&id_value={legacy_ident}")
        self.assertEqual(response.status_code, HTTPStatus.OK)
        self.assertEqual(response.data["id"], str(self.entity.id))

    def test_get_releases(self):
        c = CreatorFactory.create()
        response = client.get(f"{self.get}/{c.id}/releases")
        self.assertEqual(response.status_code, HTTPStatus.OK)
        self.assertEqual(response.data["count"], 0)

        rs = []
        for _ in range(4):
            r = ReleaseFactory.build()
            r.work = WorkFactory.create()
            r.container = None
            r.save()
            rc = ReleaseContribFactory.build()
            rc.release = r
            rc.creator = self.entity
            rc.save()
            rs.append(r)

        response = client.get(f"{self.get}/{self.entity.id}/releases")
        self.assertEqual(response.status_code, HTTPStatus.OK)
        self.assertEqual(response.data["count"], len(rs))
        self.assertSetEqual(set([d['id'] for d in response.data["items"]]), set([str(r.id) for r in rs]))

    def test_create(self):
        c = CreatorFactory.build()
        data = v.CreatorSchema.from_orm(c).model_dump_json()

        response = client.post(self.create, data=data, headers=self.auth_headers)
        self.assertEqual(response.status_code, HTTPStatus.CREATED)

        cs = m.Creator.objects.filter(id=c.id)
        self.assertEqual(len(cs), 1)

        response = client.post(self.create, data=data, headers=self.auth_headers)
        self.assertEqual(response.status_code, HTTPStatus.BAD_REQUEST)

    def test_bulk_create(self):
        cs = [v.CreatorSchema.from_orm(CreatorFactory.build()) for _ in range(100)]
        data = "["+",".join([c.model_dump_json() for c in cs])+"]"

        response = client.post(self.bulk_create, data=data, headers=self.auth_headers)
        self.assertEqual(response.status_code, HTTPStatus.CREATED)

        for c in cs:
            cs = m.Creator.objects.filter(id=c.id)
            self.assertEqual(len(cs), 1)

    def test_update(self):
        entity = CreatorFactory.build()
        data = v.CreatorSchema.from_orm(entity).model_dump_json()

        response = client.put(self.update, data=data, headers=self.auth_headers)
        self.assertEqual(response.status_code, HTTPStatus.CREATED)
        cs = m.Creator.objects.filter(id=entity.id)
        self.assertEqual(len(cs), 1)

        new_surname = "updated name"
        self.entity.surname = new_surname
        data = v.CreatorSchema.from_orm(self.entity).model_dump_json()
        response = client.put(self.update, data=data, headers=self.auth_headers)
        self.assertEqual(response.status_code, HTTPStatus.OK)

        cs = m.Creator.objects.filter(id=self.entity.id)
        self.assertEqual(len(cs), 1)
        self.assertEqual(cs[0].surname, new_surname)

    def test_delete(self):
        unsaved = CreatorFactory.build()

        response = client.delete(f"{self.delete}/{unsaved.id}", headers=self.auth_headers)
        self.assertEqual(response.status_code, HTTPStatus.NOT_FOUND)

        response = client.delete(f"{self.delete}/{self.entity.id}", headers=self.auth_headers)
        self.assertEqual(response.status_code, HTTPStatus.OK)
        self.assertEqual(response.data["id"], str(self.entity.id))

        response = client.delete(f"{self.delete}/{self.entity.id}", headers=self.auth_headers)
        self.assertEqual(response.status_code, HTTPStatus.NOT_FOUND)

class TestFileRoutes(EntityCRUDTestCase):
    base = "/file"

    def setUp(self):
        super().setUp()
        self.entity = FileFactory.create()
        FileURLFactory.build_batch(4, file_id=self.entity.id)
        m.ReleaseFile(file=self.entity, release=ReleaseFactory.create()).save()

    def test_get(self):
        response = client.get(f"{self.get}/{self.entity.id}")
        fid = self.entity.id
        self.assertEqual(response.status_code, HTTPStatus.OK)
        self.assertEqual(response.data["id"], str(fid))
        self.assertSetEqual(set([u["url"] for u in response.data["urls"]]),
                            set([u.url for u in m.FileURL.objects.filter(file_id=fid)]))
        self.assertSetEqual(set([r["id"] for r in response.data["releases"]]),
                            set([str(r.id) for r in self.entity.releases.all()]))

    def test_lookup(self):
        sha1 = "abc123"
        response = client.get(f"{self.lookup}?id_type=sha1&id_value={sha1}")
        self.assertEqual(response.status_code, HTTPStatus.NOT_FOUND)

        keys = [("sha1", self.entity.sha1),
                ("sha256", self.entity.sha256),
                ("md5", self.entity.md5),
                ]

        for id_type, id_value in keys:
            response = client.get(
                    f"{self.lookup}?id_type={id_type}&id_value={id_value}")
            self.assertEqual(response.status_code, HTTPStatus.OK)
            self.assertEqual(response.data['id'], str(self.entity.id))

        for id_type, id_value in keys:
            val = id_value.upper()
            response = client.get(
                    f"{self.lookup}?id_type={id_type}&id_value={val}")
            self.assertEqual(response.status_code, HTTPStatus.OK)
            self.assertEqual(response.data['id'], str(self.entity.id))

        legacy_ident = uuid2fcid(self.entity.id)
        response = client.get(
                f"/file/lookup?id_type=legacy_ident&id_value={legacy_ident}")
        self.assertEqual(response.status_code, HTTPStatus.OK)
        self.assertEqual(response.data["id"], str(self.entity.id))

    def test_get_releases(self):
        f = FileFactory.create()
        response = client.get(f"{self.get}/{f.id}/releases")
        self.assertEqual(response.status_code, HTTPStatus.OK)
        self.assertEqual(response.data["count"], 0)

        rs = []
        for _ in range(4):
            r = ReleaseFactory.build()
            r.work = WorkFactory.create()
            r.container = None
            r.save()
            # TODO use this trick in creator releases test
            rs.append(r)

        self.entity.releases.set(rs)
        response = client.get(f"{self.get}/{self.entity.id}/releases")
        self.assertEqual(response.status_code, HTTPStatus.OK)
        self.assertEqual(response.data["count"], len(rs))
        self.assertSetEqual(
                set([d['id'] for d in response.data["items"]]),
                set([str(r.id) for r in rs]))

    def test_create(self):
        f = FileFactory.build()
        fs = v.FileSchema.from_orm(f)
        fs.urls = [
                v.FileURLSchema.from_orm(url) for url in FileURLFactory.build_batch(4, file_id=f.id)]
        fs.releases = [v.ReleaseSchema.from_orm(ReleaseFactory.create())]

        data = fs.model_dump_json()

        response = client.post(self.create, data=data, headers=self.auth_headers)
        self.assertEqual(response.status_code, HTTPStatus.CREATED)

        es = m.File.objects.filter(id=f.id)
        self.assertEqual(len(es), 1)
        e = es[0]
        self.assertEqual(e.releases.all()[0].id, fs.releases[0].id)
        self.assertSetEqual(set([u.url for u in e.urls.all()]), set([u.url for u in fs.urls]))

        response = client.post(self.create, data=data, headers=self.auth_headers)
        self.assertEqual(response.status_code, HTTPStatus.BAD_REQUEST)

    def test_bulk_create(self):
        files = []
        for _ in range(10):
            fs = v.FileSchema.from_orm(FileFactory.build())
            fs.urls = FileURLFactory.build_batch(4, file_id=fs.id)
            fs.releases = [v.ReleaseSchema.from_orm(ReleaseFactory.create())]
            files.append(fs)
        data = "["+",".join([fs.model_dump_json() for fs in files])+"]"

        response = client.post(self.bulk_create, data=data, headers=self.auth_headers)
        self.assertEqual(response.status_code, HTTPStatus.CREATED)

        for fs in files:
            es = m.File.objects.filter(id=fs.id)
            self.assertEqual(len(es), 1)
            e = es[0]
            self.assertEqual(e.releases.all()[0].id, fs.releases[0].id)
            self.assertSetEqual(set([u.url for u in e.urls.all()]), set([u.url for u in fs.urls]))

    def test_update(self):
        entity = FileFactory.build()
        data = v.FileSchema.from_orm(entity).model_dump_json()

        response = client.put(self.update, data=data, headers=self.auth_headers)
        self.assertEqual(response.status_code, HTTPStatus.CREATED)

        self.assertEqual(m.File.objects.filter(id=entity.id).count(), 1)

        new_size = 100

        self.entity.size_bytes = new_size
        self.entity.releases.set([ReleaseFactory.create()])
        self.entity.urls.set([FileURLFactory.create()])
        data = v.FileSchema.from_orm(self.entity).model_dump_json()
        response = client.put(self.update, data=data, headers=self.auth_headers)
        self.assertEqual(response.status_code, HTTPStatus.OK)

        es = m.File.objects.filter(id=self.entity.id)
        self.assertEqual(len(es), 1)
        self.assertEqual(es[0].size_bytes, new_size)
        self.assertEqual(es[0].releases.all()[0].id, self.entity.releases.all()[0].id)
        self.assertEqual(es[0].urls.all()[0].url, self.entity.urls.all()[0].url)

    def test_delete(self):
        unsaved = FileFactory.build()

        response = client.delete(
                f"{self.delete}/{unsaved.id}", headers=self.auth_headers)
        self.assertEqual(response.status_code, HTTPStatus.NOT_FOUND)

        response = client.delete(
                f"{self.delete}/{self.entity.id}", headers=self.auth_headers)
        self.assertEqual(response.status_code, HTTPStatus.OK)
        self.assertEqual(response.data["id"], str(self.entity.id))

        response = client.delete(
                f"{self.delete}/{self.entity.id}", headers=self.auth_headers)
        self.assertEqual(response.status_code, HTTPStatus.NOT_FOUND)

class TestWebcaptureRoutes(EntityCRUDTestCase):
    base = "/webcapture"

    def setUp(self):
        super().setUp()
        self.entity = WebcaptureFactory.create()
        WebcaptureCDXFactory.create_batch(10, webcapture_id=self.entity.id)
        WebcaptureURLFactory.create_batch(4, webcapture_id=self.entity.id)

    def test_get(self):
        response = client.get(f"{self.get}/{self.entity.id}")
        self.assertEqual(response.status_code, HTTPStatus.OK)
        self.assertEqual(response.data["id"], str(self.entity.id))
        self.assertEqual(len(response.data["urls"]), 4)
        self.assertEqual(len(response.data["cdx_lines"]), 10)

    def test_create(self):
        wc = WebcaptureFactory.build(release_id=ReleaseFactory.create().id)
        wcs = v.WebcaptureSchema.from_orm(wc)
        wcs.urls = [v.WebcaptureURLSchema.from_orm(url) for url in WebcaptureURLFactory.build_batch(4)]
        wcs.cdx_lines = [v.WebcaptureCDXSchema.from_orm(line) for line in WebcaptureCDXFactory.build_batch(10)]

        data = wcs.model_dump_json(by_alias=True)

        response = client.post(self.create, data=data, headers=self.auth_headers)
        self.assertEqual(response.status_code, HTTPStatus.CREATED)

        es = m.Webcapture.objects.filter(id=wc.id)
        self.assertEqual(len(es), 1)

        self.assertEqual(len(es[0].urls.all()), len(wcs.urls))
        self.assertEqual(len(es[0].cdx_lines.all()), len(wcs.cdx_lines))

        response = client.post(self.create, data=data, headers=self.auth_headers)
        self.assertEqual(response.status_code, HTTPStatus.BAD_REQUEST)

    def test_bulk_create(self):
        webcaptures = []
        for _ in range(10):
            wc = v.WebcaptureSchema.from_orm(WebcaptureFactory.build(release_id=ReleaseFactory.create().id))
            wc.urls = WebcaptureURLFactory.build_batch(4)
            wc.cdx_lines = WebcaptureCDXFactory.build_batch(10)
            webcaptures.append(wc)
        data = "["+",".join([wc.model_dump_json() for wc in webcaptures])+"]"

        response = client.post(self.bulk_create, data=data, headers=self.auth_headers)
        self.assertEqual(response.status_code, HTTPStatus.CREATED)

        for wc in webcaptures:
            self.assertEqual(m.Webcapture.objects.filter(id=wc.id).count(), 1)

    def test_lookup(self):
        legacy_ident = uuid2fcid(self.entity.id)
        response = client.get(
                f"{self.lookup}?id_type=legacy_ident&id_value={legacy_ident}")
        self.assertEqual(response.status_code, HTTPStatus.OK)
        self.assertEqual(response.data["id"], str(self.entity.id))

    def test_get_release(self):
        response = client.get(f"{self.get}/{self.entity.id}/release")
        self.assertEqual(response.status_code, HTTPStatus.OK)
        self.assertEqual(response.data["id"], str(self.entity.release_id))

    def test_update(self):
        wc = WebcaptureFactory.build(release_id=ReleaseFactory.create().id)
        wcs = v.WebcaptureSchema.from_orm(wc)
        wcs.urls = [v.WebcaptureURLSchema.from_orm(url) for url in WebcaptureURLFactory.build_batch(4)]
        wcs.cdx_lines = [v.WebcaptureCDXSchema.from_orm(line) for line in WebcaptureCDXFactory.build_batch(10)]

        data = wcs.model_dump_json()

        response = client.put(self.update, data=data, headers=self.auth_headers)
        self.assertEqual(response.status_code, HTTPStatus.CREATED)

        self.assertEqual(m.Webcapture.objects.filter(id=wc.id).count(), 1)

        new_reason = "some hidden reason"
        self.entity.hidden_reason = new_reason

        wcs = v.WebcaptureSchema.from_orm(self.entity)
        wcs.urls = [v.WebcaptureURLSchema.from_orm(url) for url in WebcaptureURLFactory.build_batch(2)]
        wcs.cdx_lines = [v.WebcaptureCDXSchema.from_orm(line)
                         for line in WebcaptureCDXFactory.build_batch(2)]

        data = wcs.model_dump_json()
        cdx_line_count = m.WebcaptureCDX.objects.all().count()
        url_count = m.WebcaptureURL.objects.all().count()
        response = client.put(self.update, data=data, headers=self.auth_headers)
        self.assertEqual(response.status_code, HTTPStatus.OK)


        es = m.Webcapture.objects.filter(id=self.entity.id)
        self.assertEqual(len(es), 1)
        self.assertEqual(es[0].hidden_reason, new_reason)
        self.assertEqual(len(es[0].urls.all()), len(wcs.urls))
        self.assertEqual(len(es[0].urls.all()), len(wcs.cdx_lines))
        self.assertEqual(cdx_line_count-8, m.WebcaptureCDX.objects.all().count())
        self.assertEqual(url_count-2, m.WebcaptureURL.objects.all().count())

    def test_delete(self):
        response = client.delete(f"{self.delete}/{self.entity.id}", headers=self.auth_headers)
        self.assertEqual(response.status_code, HTTPStatus.OK)
        self.assertEqual(response.data["id"], str(self.entity.id))

        self.assertEqual(m.Webcapture.objects.filter(id=self.entity.id).count(), 0)


class ChangelogTests(TestCase):
    def _run(self, model_name: str, model_factory: DjangoModelFactory) -> None:
        today = []
        for _ in range(3):
            today.append(model_factory.create())
            # i'm sorry
            time.sleep(0.1)

        past = model_factory.create_batch(3)
        past_ts = datetime.now() - timedelta(days=10)

        endpoint = f"/changelog/{model_name}s"

        # this sucks
        with connection.cursor() as cursor:
            for x in range(len(past)):
                past_ts = datetime.now() - timedelta(days=10, seconds=x)
                cursor.execute(f"UPDATE fcapi_{model_name} SET updated = %s WHERE id = %t",
                               [past_ts - timedelta(seconds=x), past[x].id])

        response = client.get(endpoint)
        self.assertEqual(response.status_code, HTTPStatus.OK)
        self.assertSetEqual(
                set([e["id"] for e in response.data["items"]]),
                set([str(e.id) for e in today]))

        prev = response.data["items"][0]
        for x in range(1, len(response.data["items"])):
            e = response.data["items"][x]
            p = dateutil.parser.isoparse(prev["updated"])
            d = dateutil.parser.isoparse(e["updated"])
            self.assertTrue(p > d)
            prev = e

        start = str(datetime.today()).split(' ')[0]

        response = client.get(f"{endpoint}?start={start}")
        self.assertEqual(response.status_code, HTTPStatus.OK)
        self.assertSetEqual(
                set([e["id"] for e in response.data["items"]]),
                set([str(e.id) for e in today]))

        response = client.get(f"{endpoint}?start={start}&window=1d")
        self.assertEqual(response.status_code, HTTPStatus.OK)
        self.assertSetEqual(
                set([e["id"] for e in response.data["items"]]),
                set([str(e.id) for e in today]))

        response = client.get(f"{endpoint}?start=1906-06-06&window=1d")
        self.assertEqual(response.status_code, HTTPStatus.OK)
        self.assertEqual(response.data["count"], 0)

        past_start = str(past_ts).split(" ")[0]
        response = client.get(f"{endpoint}?start={past_start}")
        self.assertEqual(response.status_code, HTTPStatus.OK)
        self.assertSetEqual(
                set([e["id"] for e in response.data["items"]]),
                set([str(e.id) for e in past]))

        past_start = str(past_ts).split(" ")[0]
        response = client.get(f"{endpoint}?start={past_start}&window=1d")
        self.assertEqual(response.status_code, HTTPStatus.OK)
        self.assertSetEqual(
                set([e["id"] for e in response.data["items"]]),
                set([str(e.id) for e in past]))

        response = client.get(f"{endpoint}?window=30d")
        self.assertEqual(response.status_code, HTTPStatus.OK)
        self.assertSetEqual(
                set([e["id"] for e in response.data["items"]]),
                set([str(e.id) for e in past]) | set([str(e.id) for e in today]))

    def test_release_changelog(self):
        self._run("release", ReleaseFactory)

    def test_creator_changelog(self):
        self._run("creator", CreatorFactory)

    def test_container_changelog(self):
        self._run("container", ContainerFactory)

    def test_work_changelog(self):
        self._run("work", WorkFactory)

    def test_file_changelog(self):
        self._run("file", FileFactory)

    def test_webcapture_changelog(self):
        self._run("webcapture", WebcaptureFactory)
