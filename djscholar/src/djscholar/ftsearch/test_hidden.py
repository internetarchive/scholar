from datetime import datetime
from unittest import mock
import zoneinfo

from django.test import Client, TestCase

import djscholar.fcapi.models as m
from djscholar.fcapi.fcid import uuid2fcid
from djscholar.fcapi.test_views import FileFactory, FileURLFactory, ReleaseFactory, WorkFactory

ORIGINAL = "https://example.com/paper.pdf"
WAYBACK = f"https://web.archive.org/web/20200101000000/{ORIGINAL}"


def _hide(entity):
    entity.hidden_when = datetime.now(zoneinfo.ZoneInfo("UTC"))
    entity.save()
    return entity


class HiddenScholarTestCase(TestCase):
    def setUp(self):
        self.client = Client()
        self.work = WorkFactory()
        self.ident = uuid2fcid(self.work.id)
        release = ReleaseFactory(work=self.work)
        self.file = FileFactory()
        m.ReleaseFile.objects.create(file=self.file, release=release)
        FileURLFactory(file=self.file, url=WAYBACK)
        # a fulltext doc that still points at the PDF, as a stale or
        # re-indexed one would
        self.es_doc = {"fulltext": {"access_type": "wayback", "access_url": WAYBACK}}

    def access(self):
        return self.client.get(f"/work/{self.ident}/access/wayback/{ORIGINAL}")

    def test_visible_work_redirects(self):
        with mock.patch("djscholar.ftsearch.views._get_es_doc", return_value=self.es_doc):
            self.assertEqual(self.access().status_code, 302)
        with mock.patch("djscholar.ftsearch.views._get_es_doc", return_value=None):
            self.assertEqual(self.access().status_code, 302)

    def test_hidden_work_is_gone_even_with_es_doc(self):
        _hide(self.work)
        with mock.patch("djscholar.ftsearch.views._get_es_doc", return_value=self.es_doc):
            response = self.access()
        self.assertEqual(response.status_code, 410)

        response = self.client.get(
            f"/work/{self.ident}/access/ia_file/item/paper.pdf")
        self.assertEqual(response.status_code, 410)

        with mock.patch("djscholar.es.client") as es_client:
            response = self.client.get(f"/work/{self.work.id}")
        self.assertEqual(response.status_code, 410)
        es_client.assert_not_called()

    def test_fallback_skips_hidden_file(self):
        _hide(self.file)
        with mock.patch("djscholar.ftsearch.views._get_es_doc", return_value=None):
            response = self.access()
        self.assertEqual(response.status_code, 404)
