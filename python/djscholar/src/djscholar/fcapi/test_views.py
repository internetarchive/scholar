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

from djscholar.fcapi.models import Container, Release, ReleaseExtId, Work
from djscholar.fcapi.views import v2api, ContainerSchema
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
    # TODO handle ext ids. can i just have a post-create hook or something?
    # need to generate a REI for each of the id_types

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

    def test_lookup(self):
        # TODO for this to work, we need entries in ReleaseExtId
        pass

    def test_get(self):
        response = client.get(f"/release/{self.entity.id}")
        self.assertEqual(response.status_code, 200)
        self.assertEqual(response.data["id"], str(self.entity.id))

    def test_get_container(self):
        # TODO
        pass

    def test_get_work(self):
        # TODO
        pass

    def test_create(self):
        # TODO
        pass

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
