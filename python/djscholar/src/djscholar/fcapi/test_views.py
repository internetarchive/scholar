from django.contrib.auth.hashers import (
    make_password,
)
from django.contrib.auth.models import User
from django.test import TestCase
from factory.django import DjangoModelFactory
from ninja.testing import TestClient
from ninja_apikey.models import APIKey

from djscholar.fcapi.models import Container
from djscholar.fcapi.views import v2api

client = TestClient(v2api)

class ContainerFactory(DjangoModelFactory):
    class Meta:
        model = Container

class UserFactory(DjangoModelFactory):
    class Meta:
        model = User

class APIKeyFactory(DjangoModelFactory):
    class Meta:
        model = APIKey

class TestContainerRoutes(TestCase):
    def setUp(self):
        user = User.objects.create_user(username="test", password="test")
        encoded = make_password("whatever")
        self.user = user
        self.encoded = encoded

        api_key = APIKey.objects.create(user=user, prefix="prefix", hashed_key=encoded)
        self.api_user = UserFactory.create() 
        self.c = ContainerFactory.create()

    def test_get(self):
        response = client.get(f"/container/{self.c.id}")
        self.assertEqual(response.status_code, 200)

    def test_lookup(self):
        # TODO
        pass

    def test_get_releases(self):
        # TODO
        pass

    def test_create(self):
        # TODO
        pass

    def test_batch_create(self):
        # TODO
        pass

    def test_update(self):
        # TODO
        pass

    def test_delete(self):
        unsaved = ContainerFactory.build()
        # TODO pick up here, figure out auth
        response = client.delete(f"/container/{unsaved.id}",
                                 headers={"AUTHORIZATION": f"Bearer {self.encoded}"},
                                 # user=self.user,
                                 )
        self.assertEqual(response.status_code, 404)
        response = client.delete(f"/container/{self.c.id}")
        self.assertEqual(response.status_code, 200)
        response = client.delete(f"/container/{self.c.id}")
        self.assertEqual(response.status_code, 404)
