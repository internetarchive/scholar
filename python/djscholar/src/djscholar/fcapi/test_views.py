from datetime import datetime
from typing import Callable
import zoneinfo

import factory
from django.contrib.auth.hashers import (
    make_password,
)
from django.contrib.auth.models import User
from django.test import TestCase
from factory.django import DjangoModelFactory
from ninja.testing import TestClient
from ninja_apikey.models import APIKey

from djscholar.fcapi.models import Container, Release, ReleaseExtId, RELEASE_EXT_ID_TYPES, Work
from djscholar.fcapi.views import v2api, ContainerSchema, ReleaseSchema
from djscholar.fcapi.faker_providers import ExtIDProvider

client = TestClient(v2api)
factory.Faker.add_provider(ExtIDProvider)

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
        model = Container


class WorkFactory(DjangoModelFactory):
    class Meta:
        model = Work


class ReleaseExtIdFactory(DjangoModelFactory):
    id_value = factory.LazyAttribute(lambda s: factory.Faker(s.id_type))

    class Meta:
        model = ReleaseExtId


class ReleaseFactory(DjangoModelFactory):
    updated = lazy(lambda: datetime.now(zoneinfo.ZoneInfo("UTC")))
    created = lazy(lambda: datetime.now(zoneinfo.ZoneInfo("UTC")))
    work = factory.SubFactory(WorkFactory)
    container = factory.SubFactory(ContainerFactory)

    class Meta:
        model = Release


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
        for id_type, _ in RELEASE_EXT_ID_TYPES:
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

    def test_create(self):
        entity = ReleaseFactory.build()
        entity.container.save()
        entity.work.save()

        data = ReleaseSchema.from_orm(entity).model_dump_json()
        response = client.post("/release", data=data, headers=self.auth_headers)
        self.assertEqual(response.status_code, 201)

        es = Release.objects.filter(id=entity.id)
        self.assertEqual(len(es), 1)

        response = client.post("/release", data=data, headers=self.auth_headers)
        self.assertEqual(response.status_code, 400)

    def test_bulk_create(self):
        # TODO
        pass

    def test_update(self):
        # TODO
        pass

    def test_delete(self):
        # TODO
        pass


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
        data = ContainerSchema.from_orm(c).model_dump_json()
        response = client.post("/container", data=data, headers=self.auth_headers)
        self.assertEqual(response.status_code, 201)

        cs = Container.objects.filter(id=c.id)
        self.assertEqual(len(cs), 1)

        response = client.post("/container", data=data, headers=self.auth_headers)
        self.assertEqual(response.status_code, 400)

    def test_bulk_create(self):
        cs = [ContainerSchema.from_orm(ContainerFactory.build()) for _ in range(100)]
        data = "["+",".join([c.model_dump_json() for c in cs])+"]"

        response = client.post("/containers", data=data, headers=self.auth_headers)
        self.assertEqual(response.status_code, 201)

        for c in cs:
            cs = Container.objects.filter(id=c.id)
            self.assertEqual(len(cs), 1)

    def test_update(self):
        data = ContainerSchema.from_orm(ContainerFactory.build()).model_dump_json()
        response = client.put("/container", data=data, headers=self.auth_headers)
        self.assertEqual(response.status_code, 404)

        new_name = "updated name"
        self.entity.name = new_name
        data = ContainerSchema.from_orm(self.entity).model_dump_json()
        response = client.put("/container", data=data, headers=self.auth_headers)
        self.assertEqual(response.status_code, 200)

        cs = Container.objects.filter(id=self.entity.id)
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

        response = client.delete(f"/container/{self.entity.id}", headers=self.auth_headers)
        self.assertEqual(response.status_code, 404)
