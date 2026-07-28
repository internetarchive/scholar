# /// script
# requires-python = ">=3.11"
# dependencies = []
# ///
"""
One-off cleanup for the five periodic-ingest file records that were created with
bad (live-web) URLs instead of wayback replay URLs.

For each record it:
  1. decodes the fatcat_file ES _id (base32 legacy file ident) -> file UUID
     (mirrors fatcat2.fc2uuid / UuidToLegacy)
  2. GETs the file from fatcat2 and prints its sha1 + URLs, so you can confirm
     you're deleting the right (bad-URL) record before committing
  3. with --apply: DELETE {fatcat2}/file/{uuid}
     -> cascades to fcapi_releasefile + fcapi_fileurl (on_delete=CASCADE),
        leaves the release rows intact
  4. with --apply: DELETE {es}/{fatcat_file_ix}/_doc/{file_ix_id}
  5. with --apply: DELETE {es}/{fulltext_ix}/_doc/{ft_ix_id}

Dry-run by default (only the read-only GET in step 2 runs); pass --apply to
delete. Endpoints + API key are read from ~/.config/trawler/config.toml, the
same file trawler uses, so nothing sensitive lives in this script.

Records transcribed from delete_stuff.txt. file 1's _id had a stray trailing
"2026" (copy-paste error), trimmed here per confirmation.
"""
import argparse
import base64
import json
import tomllib
import uuid
from pathlib import Path
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen

CONFIG = Path.home() / ".config" / "trawler" / "config.toml"

# (label, fatcat_file ES _id [== base32 file ident], scholar_fulltext ES _id)
RECORDS = [
    ("file 1", "agpzmex55zyijkdkogfjtgwr4q", "work_riawipxrlffsdcnw7dytsnncqy"),
    ("file 2", "agpzmeyscb5thi7gwjafxtrsni", "work_c4st62nvy5gadfni3sy6ylyzdi"),
    ("file 3", "agpzmezgxf5ihanffc2ikr3gom", "work_pnr5qekcobeszgjzp5icvl7d4q"),
    ("file 4", "agpzmez5gv5btah4ldpj3tbgde", "work_cilcfqniujedhgerpdrvm6yk3q"),
    ("file 5", "agpzme6j2f6dzdawnmm5v2aoy4", "work_gdn3vxpem5bv5igkcvjdt3zi4q"),
]


def fcid_to_uuid(fcid: str) -> uuid.UUID:
    """base32 legacy fatcat ident -> UUID (mirrors fatcat2.fc2uuid)."""
    raw = base64.b32decode((fcid + "======").upper())
    if len(raw) != 16:
        raise ValueError(f"{fcid!r} decoded to {len(raw)} bytes, expected 16")
    return uuid.UUID(bytes=raw)


def http(method: str, url: str, headers: dict | None = None) -> tuple[int, str]:
    """Return (status, body). HTTP errors are returned, not raised."""
    r = Request(url, method=method, headers=headers or {})
    try:
        with urlopen(r) as resp:
            return resp.status, resp.read().decode("utf-8", "replace")
    except HTTPError as e:
        return e.code, e.read().decode("utf-8", "replace")
    except URLError as e:
        return 0, str(e)


def ok(status: int) -> str:
    # 200 = deleted/found; 404 = already gone. Both are acceptable outcomes.
    return "ok" if status in (200, 404) else "UNEXPECTED"


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--apply", action="store_true",
                    help="actually delete (default: dry-run, GET-only)")
    args = ap.parse_args()

    cfg = tomllib.loads(CONFIG.read_text())
    fc2 = cfg["fatcat2"]["endpoint"].rstrip("/")
    key = cfg["fatcat2"]["key"]
    es = cfg["indexing"]["elasticsearch_url"].rstrip("/")
    file_ix = cfg["indexing"]["fatcat_file_ix"]
    ft_ix = cfg["indexing"]["fulltext_ix"]

    print("mode: " + ("APPLY (deleting)" if args.apply
                       else "DRY-RUN (no changes; pass --apply to delete)"))
    print(f"fatcat2: {fc2}")
    print(f"es:      {es}   indices: {file_ix}, {ft_ix}\n")

    for label, file_id, ft_id in RECORDS:
        fid = fcid_to_uuid(file_id)
        print(f"== {label}: file_ix_id={file_id}  fid={fid}  ft_ix_id={ft_id}")

        # Sanity read: show what we're about to delete.
        status, body = http("GET", f"{fc2}/file/{fid}", {"X-API-Key": key})
        if status == 200:
            try:
                doc = json.loads(body)
                urls = [u.get("url") for u in doc.get("urls", [])]
                print(f"   fatcat2 file present: sha1={doc.get('sha1')} urls={urls}")
            except json.JSONDecodeError:
                print("   fatcat2 file present (unparsed body)")
        elif status == 404:
            print("   fatcat2 file already absent (404)")
        else:
            print(f"   WARNING: unexpected GET status {status}: {body[:200]}")

        if not args.apply:
            print("   [dry-run] would DELETE:")
            print(f"     DELETE {fc2}/file/{fid}")
            print(f"     DELETE {es}/{file_ix}/_doc/{file_id}")
            print(f"     DELETE {es}/{ft_ix}/_doc/{ft_id}\n")
            continue

        # 1. fatcat2 DB row (cascades to releasefile + fileurl).
        s, b = http("DELETE", f"{fc2}/file/{fid}", {"X-API-Key": key})
        print(f"   DELETE fatcat2 /file/{fid} -> {s} {ok(s)}"
              + ("" if ok(s) == "ok" else f" {b[:200]}"))

        # 2. ES fatcat_file doc.
        s, b = http("DELETE", f"{es}/{file_ix}/_doc/{file_id}")
        print(f"   DELETE es {file_ix}/_doc/{file_id} -> {s} {ok(s)}"
              + ("" if ok(s) == "ok" else f" {b[:200]}"))

        # 3. ES scholar_fulltext doc.
        s, b = http("DELETE", f"{es}/{ft_ix}/_doc/{ft_id}")
        print(f"   DELETE es {ft_ix}/_doc/{ft_id} -> {s} {ok(s)}"
              + ("" if ok(s) == "ok" else f" {b[:200]}"))
        print()

    print("done.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
