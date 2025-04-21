from datetime import datetime
from typing import Callable
import zoneinfo

import factory
from faker.providers import file, misc, person
from django.contrib.auth.hashers import (
    make_password,
)
from django.contrib.auth.models import User
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

def lazy(generate: Callable) -> factory.LazyAttribute:
    return factory.LazyAttribute(lambda _: generate())

class ContainerFactory(DjangoModelFactory):
    issnl = factory.Faker("issn")
    issne = factory.Faker("issn")
    issnp = factory.Faker("issn")
    wikidata_qid = factory.Faker("wikidata_qid")
    updated = lazy(lambda: datetime.now(zoneinfo.ZoneInfo("UTC")))
    created = lazy(lambda: datetime.now(zoneinfo.ZoneInfo("UTC")))

    class Meta:
        model = m.Container


class WorkFactory(DjangoModelFactory):
    updated = lazy(lambda: datetime.now(zoneinfo.ZoneInfo("UTC")))
    created = lazy(lambda: datetime.now(zoneinfo.ZoneInfo("UTC")))

    class Meta:
        model = m.Work


class ReleaseExtIdFactory(DjangoModelFactory):
    id_value = factory.LazyAttribute(lambda s: factory.Faker(s.id_type))

    class Meta:
        model = m.ReleaseExtId


class ReleaseFactory(DjangoModelFactory):
    updated = lazy(lambda: datetime.now(zoneinfo.ZoneInfo("UTC")))
    created = lazy(lambda: datetime.now(zoneinfo.ZoneInfo("UTC")))
    work = factory.SubFactory(WorkFactory)
    container = factory.SubFactory(ContainerFactory)

    class Meta:
        model = m.Release


class FileFactory(DjangoModelFactory):
    updated = lazy(lambda: datetime.now(zoneinfo.ZoneInfo("UTC")))
    created = lazy(lambda: datetime.now(zoneinfo.ZoneInfo("UTC")))

    md5 = factory.Faker("md5")
    sha1 = factory.Faker("sha1")
    sha256 = factory.Faker("sha256")
    mimetype = factory.Faker("mime_type")

    class Meta:
        model = m.File


class CreatorFactory(DjangoModelFactory):
    updated = lazy(lambda: datetime.now(zoneinfo.ZoneInfo("UTC")))
    created = lazy(lambda: datetime.now(zoneinfo.ZoneInfo("UTC")))
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


class UserFactory(DjangoModelFactory):
    class Meta:
        model = User


class APIKeyFactory(DjangoModelFactory):
    class Meta:
        model = APIKey


# TODO test that authed routes are in fact enforcing auth

class APITest(TestCase):
    def setUp(self):
        user = User.objects.create_user(username="test", password="test")
        prefix = "prefix"
        key = "test_api_key"
        encoded = make_password(key)
        APIKey.objects.create(user=user, prefix=prefix, hashed_key=encoded)
        self.auth_headers = {"X-API-Key": f"{prefix}.{key}"}


class TestReleaseRoutes(APITest):
    def setUp(self):
        super().setUp()
        self.entity = ReleaseFactory.create()
        self.reis = []
        for id_type, _ in m.RELEASE_EXT_ID_TYPES:
            self.reis.append(ReleaseExtIdFactory.create(release=self.entity, id_type=id_type))

    def test_lookup(self):
        doi = "10.1111/xxxxxx.111.1111"
        response = client.get(f"/release/lookup?id_type=doi&id_value={doi}")
        self.assertEqual(response.status_code, 404)

        for rei in self.reis:
            response = client.get(
                    f"/release/lookup?id_type={rei.id_type}&id_value={rei.id_value}")
            self.assertEqual(response.status_code, 200)
            self.assertEqual(response.data["id"], str(self.entity.id))

        legacy_ident = uuid2fcid(self.entity.id)
        response = client.get(f"/release/lookup?id_type=legacy_ident&id_value={legacy_ident}")
        self.assertEqual(response.status_code, 200)
        self.assertEqual(response.data["id"], str(self.entity.id))

    def test_get(self):
        response = client.get(f"/release/{self.entity.id}")
        self.assertEqual(response.status_code, 200)
        self.assertEqual(response.data["id"], str(self.entity.id))

    def test_get_container(self):
        no_container = ReleaseFactory.build()
        no_container.container = None
        no_container.work.save()
        no_container.save()
        response = client.get(f"/release/{no_container.id}/container")
        self.assertEqual(response.status_code, 404)

        response = client.get(f"/release/{self.entity.id}/container")
        self.assertEqual(response.status_code, 200)
        self.assertEqual(response.data["id"], str(self.entity.container.id))

    def test_get_work(self):
        response = client.get(f"/release/{self.entity.id}/container")
        self.assertEqual(response.status_code, 200)
        self.assertEqual(response.data["id"], str(self.entity.container.id))

    def test_get_files(self):
        es = []
        for _ in range(4):
            e = FileFactory.create()
            e.releases.set([self.entity])
            es.append(e)

        response = client.get(f"/release/{self.entity.id}/files")
        self.assertEqual(response.status_code, 200)
        self.assertEqual(len(response.data), len(es))
        self.assertSetEqual(set([d['id'] for d in response.data]), set([str(e.id) for e in es]))

    def test_get_contribs(self):
        contribs = []
        for x in range(4):
            c = ReleaseContribFactory.build()
            if x % 2 == 0:
                c.creator = CreatorFactory.create()
            c.release = self.entity
            c.save()
            contribs.append(c)

        response = client.get(f"/release/{self.entity.id}/contribs")
        self.assertEqual(response.status_code, 200)
        self.assertEqual(len(response.data), len(contribs))
        self.assertSetEqual(set([d['raw_name'] for d in response.data]),
                            set([str(c.raw_name) for c in contribs]))

    def test_create(self):
        entity = ReleaseFactory.build()
        entity.container.save()
        entity.work.save()

        data = v.ReleaseSchema.from_orm(entity).model_dump_json()
        response = client.post("/release", data=data, headers=self.auth_headers)
        self.assertEqual(response.status_code, 201)

        es = m.Release.objects.filter(id=entity.id)
        self.assertEqual(len(es), 1)

        response = client.post("/release", data=data, headers=self.auth_headers)
        self.assertEqual(response.status_code, 400)

    def test_bulk_create(self):
        rin = []
        for _ in range(100):
            r = ReleaseFactory.build()
            r.work.save()
            r.container.save()
            rin.append(v.ReleaseSchema.from_orm(r))
        data = "["+",".join([r.model_dump_json() for r in rin])+"]"

        response = client.post("/releases", data=data, headers=self.auth_headers)
        self.assertEqual(response.status_code, 201)

        for r in rin:
            rs = m.Release.objects.filter(id=r.id)
            self.assertEqual(len(rs), 1)

    def test_update(self):
        entity = ReleaseFactory.build()
        entity.work.save()
        entity.container.save()
        data = v.ReleaseSchema.from_orm(entity).model_dump_json()
        response = client.put("/release", data=data, headers=self.auth_headers)
        self.assertEqual(response.status_code, 201)
        es = m.Release.objects.filter(id=entity.id)
        self.assertEqual(len(es), 1)

        new_title = "updated title"
        self.entity.title = new_title
        data = v.ReleaseSchema.from_orm(self.entity).model_dump_json()
        response = client.put("/release", data=data, headers=self.auth_headers)
        self.assertEqual(response.status_code, 200)

        es = m.Release.objects.filter(id=self.entity.id)
        self.assertEqual(len(es), 1)
        self.assertEqual(self.entity.title, new_title)

    def test_delete(self):
        unsaved = ReleaseFactory.build()
        response = client.delete(f"/release/{unsaved.id}")
        self.assertEqual(response.status_code, 401)

        response = client.delete(f"/release/{unsaved.id}", headers=self.auth_headers)
        self.assertEqual(response.status_code, 404)

        response = client.delete(f"/release/{self.entity.id}", headers=self.auth_headers)
        self.assertEqual(response.status_code, 200)
        self.assertEqual(response.data["id"], str(self.entity.id))

        response = client.delete(f"/release/{self.entity.id}", headers=self.auth_headers)
        self.assertEqual(response.status_code, 404)


class TestContainerRoutes(APITest):
    def setUp(self):
        super().setUp()
        self.entity = ContainerFactory.create()

    def test_get(self):
        response = client.get(f"/container/{self.entity.id}")
        self.assertEqual(response.status_code, 200)
        self.assertEqual(response.data["id"], str(self.entity.id))

    def test_lookup(self):
        issnl = "1111-2222"
        response = client.get(f"/container/lookup?id_type=issnl&id_value={issnl}")
        self.assertEqual(response.status_code, 404)

        keys = [("wikidata_qid", self.entity.wikidata_qid),
                ("issnl", self.entity.issnl),
                ("issne", self.entity.issne),
                ("issnp", self.entity.issnp),
                ]

        for id_type, id_value in keys:
            response = client.get(f"/container/lookup?id_type={id_type}&id_value={id_value}")
            self.assertEqual(response.status_code, 200)
            self.assertEqual(response.data['id'], str(self.entity.id))

        legacy_ident = uuid2fcid(self.entity.id)
        response = client.get(f"/container/lookup?id_type=legacy_ident&id_value={legacy_ident}")
        self.assertEqual(response.status_code, 200)
        self.assertEqual(response.data["id"], str(self.entity.id))

    def test_get_releases(self):
        c = ContainerFactory.create()
        response = client.get(f"/container/{c.id}/releases")
        self.assertEqual(response.status_code, 200)
        self.assertEqual(len(response.data), 0)

        rs = []
        for _ in range(4):
            r = ReleaseFactory.build()
            r.container = self.entity
            r.work = WorkFactory.create()
            r.save()
            rs.append(r)

        response = client.get(f"/container/{self.entity.id}/releases")
        self.assertEqual(response.status_code, 200)
        self.assertEqual(len(response.data), len(rs))
        self.assertSetEqual(set([d['id'] for d in response.data]), set([str(r.id) for r in rs]))

    def test_create(self):
        c = ContainerFactory.build()
        data = v.ContainerSchema.from_orm(c).model_dump_json()
        response = client.post("/container", data=data, headers=self.auth_headers)
        self.assertEqual(response.status_code, 201)

        cs = m.Container.objects.filter(id=c.id)
        self.assertEqual(len(cs), 1)

        response = client.post("/container", data=data, headers=self.auth_headers)
        self.assertEqual(response.status_code, 400)

    def test_bulk_create(self):
        cs = [v.ContainerSchema.from_orm(ContainerFactory.build()) for _ in range(100)]
        data = "["+",".join([c.model_dump_json() for c in cs])+"]"

        response = client.post("/containers", data=data, headers=self.auth_headers)
        self.assertEqual(response.status_code, 201)

        for c in cs:
            cs = m.Container.objects.filter(id=c.id)
            self.assertEqual(len(cs), 1)

    def test_update(self):
        entity = ContainerFactory.build()
        data = v.ContainerSchema.from_orm(entity).model_dump_json()
        response = client.put("/container", data=data, headers=self.auth_headers)
        self.assertEqual(response.status_code, 201)
        cs = m.Container.objects.filter(id=entity.id)
        self.assertEqual(len(cs), 1)

        new_name = "updated name"
        self.entity.name = new_name
        data = v.ContainerSchema.from_orm(self.entity).model_dump_json()
        response = client.put("/container", data=data, headers=self.auth_headers)
        self.assertEqual(response.status_code, 200)

        cs = m.Container.objects.filter(id=self.entity.id)
        self.assertEqual(len(cs), 1)
        self.assertEqual(self.entity.name, new_name)


    def test_delete(self):
        unsaved = ContainerFactory.build()
        response = client.delete(f"/container/{unsaved.id}")
        self.assertEqual(response.status_code, 401)

        response = client.delete(f"/container/{unsaved.id}", headers=self.auth_headers)
        self.assertEqual(response.status_code, 404)

        response = client.delete(f"/container/{self.entity.id}", headers=self.auth_headers)
        self.assertEqual(response.status_code, 200)
        self.assertEqual(response.data["id"], str(self.entity.id))

        response = client.delete(f"/container/{self.entity.id}", headers=self.auth_headers)
        self.assertEqual(response.status_code, 404)


class TestWorkRoutes(APITest):
    def setUp(self):
        super().setUp()
        self.entity = WorkFactory.create()

    def test_get(self):
        response = client.get(f"/work/{self.entity.id}")
        self.assertEqual(response.status_code, 200)
        self.assertEqual(response.data["id"], str(self.entity.id))

    def test_lookup(self):
        legacy_ident = uuid2fcid(self.entity.id)
        response = client.get(f"/work/lookup?id_type=legacy_ident&id_value={legacy_ident}")
        self.assertEqual(response.status_code, 200)
        self.assertEqual(response.data["id"], str(self.entity.id))

    def test_get_releases(self):
        rs = []
        for _ in range(4):
            r = ReleaseFactory.build()
            r.work = self.entity
            r.container = None
            r.save()
            rs.append(r)

        response = client.get(f"/work/{self.entity.id}/releases")
        self.assertEqual(response.status_code, 200)
        self.assertSetEqual(set([r['id'] for r in response.data]),
                            set([str(r.id) for r in rs]))

    def test_delete(self):
        unsaved = WorkFactory.build()
        response = client.delete(f"/work/{unsaved.id}")
        self.assertEqual(response.status_code, 401)

        response = client.delete(f"/work/{unsaved.id}", headers=self.auth_headers)
        self.assertEqual(response.status_code, 404)

        response = client.delete(f"/work/{self.entity.id}", headers=self.auth_headers)
        self.assertEqual(response.status_code, 200)
        self.assertEqual(response.data["id"], str(self.entity.id))

        response = client.delete(f"/work/{self.entity.id}", headers=self.auth_headers)
        self.assertEqual(response.status_code, 404)

    def test_update(self):
        entity = WorkFactory.build()
        data = v.WorkSchema.from_orm(entity).model_dump_json()

        response = client.put("/work", data=data)
        self.assertEqual(response.status_code, 401)

        response = client.put("/work", data=data, headers=self.auth_headers)
        self.assertEqual(response.status_code, 201)
        es = m.Work.objects.filter(id=entity.id)
        self.assertEqual(len(es), 1)

        new_reason = "hidden for test"
        self.entity.hidden_reason = new_reason
        hidden_when = datetime.now(zoneinfo.ZoneInfo("UTC"))
        self.entity.hidden_when = hidden_when
        data = v.WorkSchema.from_orm(self.entity).model_dump_json()
        response = client.put("/work", data=data, headers=self.auth_headers)
        self.assertEqual(response.status_code, 200)

        es = m.Work.objects.filter(id=self.entity.id)
        self.assertEqual(len(es), 1)
        self.assertEqual(self.entity.hidden_reason, new_reason)
        self.assertEqual(self.entity.hidden_when, hidden_when)


class TestCreatorRoutes(APITest):
    def setUp(self):
        super().setUp()
        self.entity = CreatorFactory.create()

    def test_get(self):
        response = client.get(f"/creator/{self.entity.id}")
        self.assertEqual(response.status_code, 200)
        self.assertEqual(response.data["id"], str(self.entity.id))

    def test_lookup(self):
        orcid = "abc123"
        response = client.get(f"/creator/lookup?id_type=orcid&id_value={orcid}")
        self.assertEqual(response.status_code, 404)

        self.entity.orcid = "TODO make an orcid generator"
        self.entity.save()

        keys = [("orcid", self.entity.orcid),
                ]

        for id_type, id_value in keys:
            response = client.get(f"/creator/lookup?id_type={id_type}&id_value={id_value}")
            self.assertEqual(response.status_code, 200)
            self.assertEqual(response.data['id'], str(self.entity.id))

        legacy_ident = uuid2fcid(self.entity.id)
        response = client.get(f"/creator/lookup?id_type=legacy_ident&id_value={legacy_ident}")
        self.assertEqual(response.status_code, 200)
        self.assertEqual(response.data["id"], str(self.entity.id))

    def test_get_releases(self):
        c = CreatorFactory.create()
        response = client.get(f"/creator/{c.id}/releases")
        self.assertEqual(response.status_code, 200)
        self.assertEqual(len(response.data), 0)

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

        response = client.get(f"/creator/{self.entity.id}/releases")
        self.assertEqual(response.status_code, 200)
        self.assertEqual(len(response.data), len(rs))
        self.assertSetEqual(set([d['id'] for d in response.data]), set([str(r.id) for r in rs]))

    def test_create(self):
        c = CreatorFactory.build()
        data = v.CreatorSchema.from_orm(c).model_dump_json()

        response = client.post("/creator", data=data)
        self.assertEqual(response.status_code, 401)

        response = client.post("/creator", data=data, headers=self.auth_headers)
        self.assertEqual(response.status_code, 201)

        cs = m.Creator.objects.filter(id=c.id)
        self.assertEqual(len(cs), 1)

        response = client.post("/creator", data=data, headers=self.auth_headers)
        self.assertEqual(response.status_code, 400)

    def test_bulk_create(self):
        cs = [v.CreatorSchema.from_orm(CreatorFactory.build()) for _ in range(100)]
        data = "["+",".join([c.model_dump_json() for c in cs])+"]"

        response = client.post("/creators", data=data, headers=self.auth_headers)
        self.assertEqual(response.status_code, 201)

        for c in cs:
            cs = m.Creator.objects.filter(id=c.id)
            self.assertEqual(len(cs), 1)

    def test_update(self):
        entity = CreatorFactory.build()
        data = v.CreatorSchema.from_orm(entity).model_dump_json()

        response = client.put("/creator", data=data)
        self.assertEqual(response.status_code, 401)

        response = client.put("/creator", data=data, headers=self.auth_headers)
        self.assertEqual(response.status_code, 201)
        cs = m.Creator.objects.filter(id=entity.id)
        self.assertEqual(len(cs), 1)

        new_surname = "updated name"
        self.entity.name = new_surname
        data = v.CreatorSchema.from_orm(self.entity).model_dump_json()
        response = client.put("/creator", data=data, headers=self.auth_headers)
        self.assertEqual(response.status_code, 200)

        cs = m.Creator.objects.filter(id=self.entity.id)
        self.assertEqual(len(cs), 1)
        self.assertEqual(self.entity.name, new_surname)

    def test_delete(self):
        unsaved = CreatorFactory.build()
        response = client.delete(f"/creator/{unsaved.id}")
        self.assertEqual(response.status_code, 401)

        response = client.delete(f"/creator/{unsaved.id}", headers=self.auth_headers)
        self.assertEqual(response.status_code, 404)

        response = client.delete(f"/creator/{self.entity.id}", headers=self.auth_headers)
        self.assertEqual(response.status_code, 200)
        self.assertEqual(response.data["id"], str(self.entity.id))

        response = client.delete(f"/creator/{self.entity.id}", headers=self.auth_headers)
        self.assertEqual(response.status_code, 404)

class TestFileRoutes(APITest):
    def setUp(self):
        super().setUp()
        self.entity = FileFactory.create()

    def test_get(self):
        response = client.get(f"/file/{self.entity.id}")
        self.assertEqual(response.status_code, 200)
        self.assertEqual(response.data["id"], str(self.entity.id))

    def test_lookup(self):
        sha1 = "abc123"
        response = client.get(f"/file/lookup?id_type=sha1&id_value={sha1}")
        self.assertEqual(response.status_code, 404)

        keys = [("sha1", self.entity.sha1),
                ("sha256", self.entity.sha256),
                ("md5", self.entity.md5),
                ]

        for id_type, id_value in keys:
            response = client.get(f"/file/lookup?id_type={id_type}&id_value={id_value}")
            self.assertEqual(response.status_code, 200)
            self.assertEqual(response.data['id'], str(self.entity.id))

        legacy_ident = uuid2fcid(self.entity.id)
        response = client.get(f"/file/lookup?id_type=legacy_ident&id_value={legacy_ident}")
        self.assertEqual(response.status_code, 200)
        self.assertEqual(response.data["id"], str(self.entity.id))

    def test_get_releases(self):
        f = FileFactory.create()
        response = client.get(f"/file/{f.id}/releases")
        self.assertEqual(response.status_code, 200)
        self.assertEqual(len(response.data), 0)

        rs = []
        for _ in range(4):
            r = ReleaseFactory.build()
            r.work = WorkFactory.create()
            r.container = None
            r.save()
            # TODO use this trick in creator releases test
            rs.append(r)

        self.entity.releases.set(rs)
        response = client.get(f"/file/{self.entity.id}/releases")
        self.assertEqual(response.status_code, 200)
        self.assertEqual(len(response.data), len(rs))
        self.assertSetEqual(set([d['id'] for d in response.data]), set([str(r.id) for r in rs]))

    def test_create(self):
        c = FileFactory.build()
        data = v.FileSchema.from_orm(c).model_dump_json()

        response = client.post("/file", data=data)
        self.assertEqual(response.status_code, 401)

        response = client.post("/file", data=data, headers=self.auth_headers)
        self.assertEqual(response.status_code, 201)

        cs = m.File.objects.filter(id=c.id)
        self.assertEqual(len(cs), 1)

        response = client.post("/file", data=data, headers=self.auth_headers)
        self.assertEqual(response.status_code, 400)

    def test_bulk_create(self):
        es = [v.FileSchema.from_orm(FileFactory.build()) for _ in range(100)]
        data = "["+",".join([e.model_dump_json() for e in es])+"]"

        response = client.post("/files", data=data, headers=self.auth_headers)
        self.assertEqual(response.status_code, 201)

        for f in es:
            self.assertEqual(m.File.objects.filter(id=f.id).count(), 1)

    def test_update(self):
        entity = FileFactory.build()
        data = v.FileSchema.from_orm(entity).model_dump_json()

        response = client.put("/file", data=data)
        self.assertEqual(response.status_code, 401)

        response = client.put("/file", data=data, headers=self.auth_headers)
        self.assertEqual(response.status_code, 201)

        self.assertEqual(m.File.objects.filter(id=entity.id).count(), 1)

        new_surname = "updated name"
        self.entity.name = new_surname
        data = v.FileSchema.from_orm(self.entity).model_dump_json()
        response = client.put("/file", data=data, headers=self.auth_headers)
        self.assertEqual(response.status_code, 200)

        self.assertEqual(m.File.objects.filter(id=entity.id).count(), 1)
        self.assertEqual(self.entity.name, new_surname)

    def test_delete(self):
        unsaved = FileFactory.build()
        response = client.delete(f"/file/{unsaved.id}")
        self.assertEqual(response.status_code, 401)

        response = client.delete(f"/file/{unsaved.id}", headers=self.auth_headers)
        self.assertEqual(response.status_code, 404)

        response = client.delete(f"/file/{self.entity.id}", headers=self.auth_headers)
        self.assertEqual(response.status_code, 200)
        self.assertEqual(response.data["id"], str(self.entity.id))

        response = client.delete(f"/file/{self.entity.id}", headers=self.auth_headers)
        self.assertEqual(response.status_code, 404)

class TestWebcaptureRoutes(APITest):
    def setUp(self):
        super().setUp()
        self.entity = FileFactory.create()

    def test_get(self):
        response = client.get(f"/file/{self.entity.id}")
        self.assertEqual(response.status_code, 200)
        self.assertEqual(response.data["id"], str(self.entity.id))
