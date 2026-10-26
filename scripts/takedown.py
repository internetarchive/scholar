# /// script
# requires-python = ">=3.11"
# dependencies = []
# ///
"""
Take down a fatcat work: hide it and everything under it in fatcat2, and
remove it from Elasticsearch.

Given a work (UUID, legacy ident, or a scholar_fulltext _id like
work_<ident>), this:

  1. walks fatcat2 for the work's releases, each release's files and
     webcaptures
  2. sets hidden_when (now, UTC) and hidden_reason (--reason) on the work,
     every release, file and webcapture, by GETting each entity and PUTting it
     back with the hidden fields set. Entities already hidden are left alone,
     so a re-run keeps the original hidden_when.
  3. deletes from ES:
       fatcat_release    docs whose work_id is the work, plus each release by _id
       fatcat_file       each hidden file by _id
       scholar_fulltext  docs whose work_ident is the work, plus work_<ident>
       fatcat_ref        refs whose source_release_ident is one of the releases
                         (outbound citations). Refs from other releases that
                         cite this one are left; they belong to those releases.

A file that is also attached to a release outside this work is shared. Shared
files are not hidden and their fatcat_file doc is kept, since that would take
the file away from the other work too; pass --include-shared-files to hide
them anyway. Either way they are listed.

Dry-run by default: reads everything from fatcat2, counts the matching ES
docs, and prints what it would do. Pass --apply to write.

Endpoints and the fatcat2 API key come from ~/.config/trawler/config,
the same file trawler uses. The key's user must be staff in fatcat2, since
reads ask for show_hidden. The fatcat_ref index isn't in that file; it is
indexing.fatcat_ref_ix if present, else "fatcat_ref".

The PUT routes replace an entity wholesale, so a release's extids, contribs,
abstracts and citations, and a file's URLs and release links, are deleted and
recreated from the GET response. The content survives; their row ids don't.
"""
import argparse
import base64
import binascii
import datetime
import json
import uuid
from pathlib import Path
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen

import tomllib

CONFIG = Path.home() / ".config" / "trawler" / "config"

# Items per request to fatcat2's paginated list routes.
PAGE_SIZE = 100


def to_uuid(ident: str) -> uuid.UUID:
    """UUID, legacy base32 ident, or work_<ident> -> UUID."""
    try:
        return uuid.UUID(ident)
    except ValueError:
        pass
    b = ident.split("_")[-1].upper()
    try:
        raw = base64.b32decode(b + "======") if len(b) == 26 else b""
    except binascii.Error:
        raw = b""
    if len(raw) != 16:
        raise SystemExit(f"{ident!r} is neither a UUID nor a fatcat ident")
    return uuid.UUID(bytes=raw)


def to_legacy(u: uuid.UUID | str) -> str:
    """UUID -> legacy base32 ident (mirrors fcid.uuid2fcid)."""
    return base64.b32encode(uuid.UUID(str(u)).bytes).decode().lower().rstrip("=")


def http(method: str, url: str, body: bytes | None = None,
         headers: dict | None = None) -> tuple[int, str]:
    """(status, body). HTTP errors are returned, not raised."""
    h = {"Content-Type": "application/json"} | (headers or {})
    r = Request(url, method=method, data=body, headers=h)
    try:
        with urlopen(r, timeout=600) as resp:
            return resp.status, resp.read().decode("utf-8", "replace")
    except HTTPError as e:
        return e.code, e.read().decode("utf-8", "replace")
    except URLError as e:
        return 0, str(e)


class Fatcat2:
    def __init__(self, endpoint: str, key: str):
        self.endpoint = endpoint
        self.key = key

    def get(self, path: str) -> dict:
        # show_hidden so a re-run, or a partly hidden work, can still read
        # what's already hidden. It needs an admin (staff) API key.
        sep = "&" if "?" in path else "?"
        status, body = http("GET", f"{self.endpoint}{path}{sep}show_hidden=true",
                            headers={"X-API-Key": self.key})
        if status != 200:
            raise SystemExit(f"GET {path} -> {status}: {body[:300]}")
        return json.loads(body)

    def get_all(self, path: str) -> list[dict]:
        """Every item from a paginated list route."""
        items = []
        while True:
            page = self.get(f"{path}?limit={PAGE_SIZE}&offset={len(items)}")
            items += page["items"]
            if not page["items"] or len(items) >= page["count"]:
                return items

    def put(self, kind: str, entity: dict) -> tuple[int, str]:
        return http("PUT", f"{self.endpoint}/{kind}", json.dumps(entity).encode(),
                    {"X-API-Key": self.key})


def es_count(es: str, ix: str, query: dict) -> int:
    status, body = http("POST", f"{es}/{ix}/_count", json.dumps({"query": query}).encode())
    if status != 200:
        raise SystemExit(f"ES _count {ix} -> {status}: {body[:300]}")
    return json.loads(body)["count"]


def es_delete_by_query(es: str, ix: str, query: dict) -> tuple[bool, str]:
    status, body = http("POST", f"{es}/{ix}/_delete_by_query?conflicts=proceed&refresh=true",
                        json.dumps({"query": query}).encode())
    if status != 200:
        return False, f"{status}: {body[:300]}"
    res = json.loads(body)
    if res.get("failures"):
        return False, json.dumps(res["failures"])[:300]
    return True, f"deleted {res['deleted']}"


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("work", help="work UUID, legacy ident, or work_<ident>")
    ap.add_argument("--reason", required=True,
                    help="why it was taken down; stored as hidden_reason")
    ap.add_argument("--apply", action="store_true",
                    help="actually hide and delete (default: dry-run, reads only)")
    ap.add_argument("--include-shared-files", action="store_true",
                    help="also hide files attached to releases outside this work")
    args = ap.parse_args()

    cfg = tomllib.loads(CONFIG.read_text())
    fc2 = Fatcat2(cfg["fatcat2"]["endpoint"].rstrip("/"), cfg["fatcat2"]["key"])
    es = cfg["indexing"]["elasticsearch_url"].rstrip("/")
    release_ix = cfg["indexing"]["fatcat_release_ix"]
    file_ix = cfg["indexing"]["fatcat_file_ix"]
    ft_ix = cfg["indexing"]["fulltext_ix"]
    ref_ix = cfg["indexing"].get("fatcat_ref_ix", "fatcat_ref")

    work_id = to_uuid(args.work)
    work_legacy = to_legacy(work_id)

    print("mode: " + ("APPLY (hiding and deleting)" if args.apply
                       else "DRY-RUN (no changes; pass --apply to take down)"))
    print(f"fatcat2: {fc2.endpoint}")
    print(f"es:      {es}   indices: {release_ix}, {file_ix}, {ft_ix}, {ref_ix}")
    print(f"work:    {work_id} ({work_legacy})")
    print(f"reason:  {args.reason}\n")

    # 1. Walk the work.
    work = fc2.get(f"/work/{work_id}")
    releases = fc2.get_all(f"/work/{work_id}/releases")
    release_ids = {r["id"] for r in releases}

    files: dict[str, dict] = {}
    webcaptures: dict[str, dict] = {}
    for r in releases:
        for f in fc2.get_all(f"/release/{r['id']}/files"):
            files[f["id"]] = f
        for wc in fc2.get_all(f"/release/{r['id']}/webcaptures"):
            webcaptures[wc["id"]] = wc

    shared = {fid for fid, f in files.items()
              if {rel["id"] for rel in f["releases"]} - release_ids}

    print(f"releases ({len(releases)}):")
    for r in releases:
        print(f"  {r['id']} ({to_legacy(r['id'])}) {r['title'][:80]!r}")
    print(f"files ({len(files)}):")
    for fid, f in files.items():
        note = ""
        if fid in shared:
            others = sorted({rel["id"] for rel in f["releases"]} - release_ids)
            note = (" SHARED, hiding anyway" if args.include_shared_files
                    else " SHARED, skipping") + f" (also on releases {', '.join(others)})"
        print(f"  {fid} ({to_legacy(fid)}) sha1={f.get('sha1')}{note}")
    print(f"webcaptures ({len(webcaptures)}):")
    for wcid, wc in webcaptures.items():
        print(f"  {wcid} {wc.get('original_url')}")
    print()

    if not args.include_shared_files:
        for fid in shared:
            del files[fid]

    # 2. Hide in fatcat2.
    hidden_when = datetime.datetime.now(datetime.timezone.utc).isoformat()
    todo = ([("work", work)]
            + [("release", r) for r in releases]
            + [("file", f) for f in files.values()]
            + [("webcapture", wc) for wc in webcaptures.values()])
    failed = 0
    print("fatcat2:")
    for kind, entity in todo:
        if entity.get("hidden_when"):
            print(f"  {kind} {entity['id']}: already hidden {entity['hidden_when']}, leaving")
            continue
        if not args.apply:
            print(f"  [dry-run] would hide {kind} {entity['id']}")
            continue
        status, body = fc2.put(kind, entity | {"hidden_when": hidden_when,
                                                "hidden_reason": args.reason})
        if status == 200:
            print(f"  hid {kind} {entity['id']}")
        else:
            failed += 1
            print(f"  FAILED to hide {kind} {entity['id']}: {status} {body[:300]}")
    print()

    # 3. Remove from ES.
    release_legacy = [to_legacy(r) for r in release_ids]
    file_legacy = [to_legacy(f) for f in files]
    deletes = [
        (release_ix, {"bool": {"should": [{"term": {"work_id": work_legacy}},
                                          {"ids": {"values": release_legacy}}]}}),
        (file_ix, {"ids": {"values": file_legacy}}),
        (ft_ix, {"bool": {"should": [{"term": {"work_ident": work_legacy}},
                                     {"ids": {"values": [f"work_{work_legacy}"]}}]}}),
        (ref_ix, {"terms": {"source_release_ident": release_legacy}}),
    ]
    print("elasticsearch:")
    for ix, query in deletes:
        n = es_count(es, ix, query)
        if not args.apply:
            print(f"  [dry-run] would delete {n} from {ix}")
            continue
        if n == 0:
            print(f"  {ix}: nothing to delete")
            continue
        ok, msg = es_delete_by_query(es, ix, query)
        if not ok:
            failed += 1
        print(f"  {ix}: {msg}" if ok else f"  FAILED on {ix}: {msg}")

    print("\ndone." if not failed else f"\ndone, with {failed} failures; safe to re-run.")
    return 1 if failed else 0


if __name__ == "__main__":
    raise SystemExit(main())
