from django.test import TestCase
from factory.django import DjangoModelFactory
from ninja.testing import TestClient

from djscholar.fcapi.models import Container
from djscholar.fcapi.views import v2api

class ContainerFactory(DjangoModelFactory):
    class Meta:
        model = Container

class TestContainerRoutes(TestCase):
    def setUp(self):
        self.c = ContainerFactory.create()

    def test_get(self):
        client = TestClient(v2api)
        response = client.get(f"/container/{self.c.id}")
        self.assertEqual(response.status_code, 200)
