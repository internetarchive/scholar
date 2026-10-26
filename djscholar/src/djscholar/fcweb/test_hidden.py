from datetime import datetime
from unittest import mock
import uuid
import zoneinfo

from django.test import Client, TestCase
from django.urls import reverse

import djscholar.fcapi.models as m
from djscholar.fcapi.test_views import (
    CreatorFactory, FileFactory, FileURLFactory, ReleaseContribFactory,
    ReleaseExtIdFactory, ReleaseFactory, WebcaptureFactory, WorkFactory,
)


def _hide(entity):
    entity.hidden_when = datetime.now(zoneinfo.ZoneInfo("UTC"))
    entity.hidden_reason = "takedown"
    entity.save()
    return entity


def _no_es():
    raise ConnectionError("no ES in tests")


@mock.patch("djscholar.es.client", _no_es)
class HiddenWebTestCase(TestCase):
    def setUp(self):
        self.client = Client()
        self.work = WorkFactory()
        self.rv = ReleaseFactory(work=self.work, title="Visible Paper")
        self.rh = _hide(ReleaseFactory(work=self.work, title="Secret Paper",
                                       legacy_rev=uuid.uuid4()))

    def get(self, name, **kwargs):
        return self.client.get(reverse(f"fcweb:{name}", kwargs=kwargs))

    def test_hidden_release_pages_are_gone(self):
        for name in ("release_view", "release_view_metadata", "release_view_contribs",
                     "release_bibtex", "release_save"):
            response = self.get(name, ident=str(self.rh.id))
            self.assertEqual(response.status_code, 410, name)
            self.assertNotIn("Secret Paper", response.content.decode(), name)
            self.assertNotIn("takedown", response.content.decode(), name)

    def test_hidden_release_does_not_fall_back_to_es(self):
        with mock.patch("djscholar.fcweb.views._release_view_es_context") as es_ctx:
            response = self.get("release_view", ident=str(self.rh.id))
        self.assertEqual(response.status_code, 410)
        es_ctx.assert_not_called()

    def test_lookup_and_revision_of_hidden_release_are_gone(self):
        ReleaseExtIdFactory(release=self.rh, id_type="doi", id_value="10.1/secret")
        response = self.client.get(reverse("fcweb:release_lookup"), {"doi": "10.1/secret"})
        self.assertEqual(response.status_code, 410)

        response = self.get("release_revision_view", rev_id=str(self.rh.legacy_rev))
        self.assertEqual(response.status_code, 410)

    def test_hidden_work_page_is_gone(self):
        _hide(self.work)
        self.assertEqual(self.get("work_view", ident=str(self.work.id)).status_code, 410)

    def test_work_page_skips_hidden_releases(self):
        response = self.get("work_view", ident=str(self.work.id))
        self.assertContains(response, "Visible Paper")
        self.assertNotContains(response, "Secret Paper")

    def test_release_page_skips_hidden_files(self):
        f = FileFactory()
        fh = _hide(FileFactory())
        m.ReleaseFile.objects.create(file=f, release=self.rv)
        m.ReleaseFile.objects.create(file=fh, release=self.rv)
        FileURLFactory(file=f, url="https://web.archive.org/web/1/visible.pdf")
        FileURLFactory(file=fh, url="https://web.archive.org/web/1/hidden.pdf")

        response = self.get("release_view", ident=str(self.rv.id))
        self.assertContains(response, "visible.pdf")
        self.assertNotContains(response, "hidden.pdf")

        self.assertEqual(self.get("file_view", ident=str(fh.id)).status_code, 410)

    def test_file_page_skips_hidden_releases(self):
        f = FileFactory()
        m.ReleaseFile.objects.create(file=f, release=self.rv)
        m.ReleaseFile.objects.create(file=f, release=self.rh)
        response = self.get("file_view", ident=str(f.id))
        self.assertContains(response, "Visible Paper")
        self.assertNotContains(response, "Secret Paper")

    def test_creator_page_skips_hidden_releases(self):
        creator = CreatorFactory()
        ReleaseContribFactory(release=self.rv, creator=creator, position=0)
        ReleaseContribFactory(release=self.rh, creator=creator, position=0)
        response = self.get("creator_view", ident=str(creator.id))
        self.assertContains(response, "Visible Paper")
        self.assertNotContains(response, "Secret Paper")

    def test_webcapture_page_hides_hidden_release(self):
        wc = WebcaptureFactory(release=self.rh)
        response = self.get("webcapture_view", ident=str(wc.id))
        self.assertEqual(response.status_code, 200)
        self.assertNotContains(response, "Secret Paper")

    def test_changelog_skips_hidden(self):
        response = self.client.get(reverse("fcweb:changelog_view"))
        self.assertContains(response, "Visible Paper")
        self.assertNotContains(response, "Secret Paper")
