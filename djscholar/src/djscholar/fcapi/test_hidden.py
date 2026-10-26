from datetime import datetime
from http import HTTPStatus
import json
import zoneinfo

from django.contrib.auth.hashers import make_password
from django.contrib.auth.models import User
from django.test import TestCase
from ninja_apikey.models import APIKey

import djscholar.fcapi.models as m
import djscholar.fcapi.views as v
from djscholar.fcapi.test_views import (
    FileFactory, FileURLFactory, ReleaseContribFactory, ReleaseExtIdFactory,
    ReleaseFactory, WebcaptureFactory, WorkFactory, client,
)


def _now():
    return datetime.now(zoneinfo.ZoneInfo("UTC"))


def _hide(entity, reason="takedown"):
    entity.hidden_when = _now()
    entity.hidden_reason = reason
    entity.save()
    return entity


def _key(username: str, is_staff: bool) -> dict:
    user = User.objects.create_user(username=username, password="x", is_staff=is_staff)
    APIKey.objects.create(user=user, prefix=username, hashed_key=make_password("key"))
    return {"X-API-Key": f"{username}.key"}


class HiddenAPITestCase(TestCase):
    """
    A work with a visible release (rv) and a hidden one (rh). rv has a visible
    file (f, also attached to rh), a hidden file (fh) and a hidden webcapture.
    """

    def setUp(self):
        self.admin = _key("admin", is_staff=True)
        self.user = _key("user", is_staff=False)

        self.work = WorkFactory()
        self.rv = ReleaseFactory(work=self.work)
        self.rh = _hide(ReleaseFactory(work=self.work))
        self.f = FileFactory()
        self.fh = _hide(FileFactory())
        m.ReleaseFile.objects.create(file=self.f, release=self.rv)
        m.ReleaseFile.objects.create(file=self.f, release=self.rh)
        m.ReleaseFile.objects.create(file=self.fh, release=self.rv)
        self.wch = _hide(WebcaptureFactory(release=self.rv))

    def ids(self, response) -> set[str]:
        return {e["id"] for e in response.json()["items"]}

    def test_get_hidden_is_gone(self):
        for path in (f"/release/{self.rh.id}", f"/file/{self.fh.id}",
                     f"/webcapture/{self.wch.id}", f"/release/{self.rh.id}/work",
                     f"/release/{self.rh.id}/files", f"/release/{self.rh.id}/contribs",
                     f"/webcapture/{self.wch.id}/release"):
            response = client.get(path)
            self.assertEqual(response.status_code, HTTPStatus.GONE, path)
            self.assertNotIn("takedown", response.content.decode())

    def test_get_hidden_work_is_gone(self):
        _hide(self.work)
        self.assertEqual(client.get(f"/work/{self.work.id}").status_code, HTTPStatus.GONE)
        self.assertEqual(client.get(f"/release/{self.rv.id}/work").status_code,
                         HTTPStatus.GONE)

    def test_show_hidden_requires_admin(self):
        path = f"/release/{self.rh.id}?show_hidden=true"
        self.assertEqual(client.get(path).status_code, HTTPStatus.FORBIDDEN)
        self.assertEqual(client.get(path, headers=self.user).status_code, HTTPStatus.FORBIDDEN)
        self.assertEqual(client.get(path, headers={"X-API-Key": "admin.wrong"}).status_code,
                         HTTPStatus.FORBIDDEN)

        response = client.get(path, headers=self.admin)
        self.assertEqual(response.status_code, HTTPStatus.OK)
        self.assertEqual(response.json()["hidden_reason"], "takedown")

    def test_show_hidden_false_needs_no_auth(self):
        response = client.get(f"/release/{self.rv.id}?show_hidden=false")
        self.assertEqual(response.status_code, HTTPStatus.OK)

    def test_lists_skip_hidden(self):
        response = client.get(f"/work/{self.work.id}/releases")
        self.assertEqual(self.ids(response), {str(self.rv.id)})

        response = client.get(f"/work/{self.work.id}/releases?show_hidden=true",
                              headers=self.admin)
        self.assertEqual(self.ids(response), {str(self.rv.id), str(self.rh.id)})

        response = client.get(f"/file/{self.f.id}/releases")
        self.assertEqual(self.ids(response), {str(self.rv.id)})

        response = client.get(f"/release/{self.rv.id}/webcaptures")
        self.assertEqual(self.ids(response), set())

    def test_file_embeds_skip_hidden_releases(self):
        response = client.get(f"/release/{self.rv.id}/files")
        items = response.json()["items"]
        self.assertEqual([f["id"] for f in items], [str(self.f.id)])
        self.assertEqual([r["id"] for r in items[0]["releases"]], [str(self.rv.id)])

        response = client.get(f"/file/{self.f.id}")
        self.assertEqual([r["id"] for r in response.json()["releases"]], [str(self.rv.id)])

        response = client.get(f"/release/{self.rv.id}/files?show_hidden=true",
                              headers=self.admin)
        items = response.json()["items"]
        self.assertEqual({f["id"] for f in items}, {str(self.f.id), str(self.fh.id)})
        f = next(f for f in items if f["id"] == str(self.f.id))
        self.assertEqual({r["id"] for r in f["releases"]}, {str(self.rv.id), str(self.rh.id)})

    def test_lookup_prefers_visible(self):
        ReleaseExtIdFactory(release=self.rv, id_type="doi", id_value="10.1/shared")
        ReleaseExtIdFactory(release=self.rh, id_type="doi", id_value="10.1/shared")
        ReleaseExtIdFactory(release=self.rh, id_type="doi", id_value="10.1/hidden")

        response = client.get("/release/lookup?id_type=doi&id_value=10.1/shared")
        self.assertEqual(response.json()["id"], str(self.rv.id))

        response = client.get("/release/lookup?id_type=doi&id_value=10.1/hidden")
        self.assertEqual(response.status_code, HTTPStatus.GONE)

        response = client.get("/release/lookup?id_type=doi&id_value=10.1/hidden&show_hidden=true",
                              headers=self.admin)
        self.assertEqual(response.json()["id"], str(self.rh.id))

        response = client.get(f"/file/lookup?id_type=sha1&id_value={self.fh.sha1}")
        self.assertEqual(response.status_code, HTTPStatus.GONE)

    def test_fulltext_redirect_skips_hidden(self):
        ReleaseExtIdFactory(release=self.rv, id_type="doi", id_value="10.1/ft")
        FileURLFactory(file=self.fh, url="https://web.archive.org/web/1/hidden.pdf")
        FileURLFactory(file=self.f, rel="repository", url="https://example.com/visible.pdf")

        response = client.get("/release/lookup/fulltext?id_type=doi&id_value=10.1/ft")
        self.assertEqual(response.status_code, HTTPStatus.FOUND)
        self.assertEqual(response["Location"], "https://example.com/visible.pdf")

        _hide(self.f)
        response = client.get("/release/lookup/fulltext?id_type=doi&id_value=10.1/ft")
        self.assertEqual(response.status_code, HTTPStatus.NOT_FOUND)

    def test_put_keeps_hidden_unless_sent(self):
        ReleaseContribFactory(release=self.rh, position=0)
        payload = json.loads(v.ReleaseSchema.from_orm(self.rh).model_dump_json())
        del payload["hidden_when"]
        del payload["hidden_reason"]
        payload["title"] = "new title"

        response = client.put("/release", data=json.dumps(payload), headers=self.admin)
        self.assertEqual(response.status_code, HTTPStatus.OK)
        self.rh.refresh_from_db()
        self.assertEqual(self.rh.title, "new title")
        self.assertIsNotNone(self.rh.hidden_when)
        self.assertEqual(self.rh.hidden_reason, "takedown")

        payload["hidden_when"] = None
        payload["hidden_reason"] = None
        response = client.put("/release", data=json.dumps(payload), headers=self.admin)
        self.assertEqual(response.status_code, HTTPStatus.OK)
        self.rh.refresh_from_db()
        self.assertIsNone(self.rh.hidden_when)

    def test_put_file_keeps_hidden_unless_sent(self):
        payload = json.loads(v.FileSchema.from_orm(self.fh).model_dump_json())
        del payload["hidden_when"]
        del payload["hidden_reason"]
        response = client.put("/file", data=json.dumps(payload), headers=self.admin)
        self.assertEqual(response.status_code, HTTPStatus.OK)
        self.fh.refresh_from_db()
        self.assertIsNotNone(self.fh.hidden_when)

    def test_add_url_to_hidden_file_is_gone(self):
        response = client.post(f"/file/{self.fh.id}/url",
                               data=json.dumps({"url": "https://example.com/x.pdf"}),
                               headers=self.admin)
        self.assertEqual(response.status_code, HTTPStatus.GONE)
        self.assertFalse(self.fh.urls.exists())

    def test_changelog_skips_hidden(self):
        response = client.get("/changelog/releases")
        self.assertEqual(self.ids(response), {str(self.rv.id)})

        response = client.get("/changelog/releases?show_hidden=true", headers=self.admin)
        self.assertEqual(self.ids(response), {str(self.rv.id), str(self.rh.id)})

        response = client.get("/changelog/files")
        items = response.json()["items"]
        self.assertEqual([f["id"] for f in items], [str(self.f.id)])
        self.assertEqual([r["id"] for r in items[0]["releases"]], [str(self.rv.id)])
