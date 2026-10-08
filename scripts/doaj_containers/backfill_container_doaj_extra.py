# /// script
# requires-python = ">=3.11"
# dependencies = []
# ///
"""
Write extra.doaj onto fatcat2 containers for journals in DOAJ.

trawler marks a container in_doaj when it indexes one carrying extra.doaj.
Containers created from DOAJ's journal list get it; containers that already
existed, and that the journal import therefore left alone, may not. Without it
a re-index (trawler's IndexContainer) would undo backfill_container_in_doaj.py.
Run this first, then that.

It finds containers the same way that script does:
  1. normalizes each DOAJ CSV row's print and electronic ISSNs, and adds the
     ISSN-L each maps to in trawler's ISSN-L table
  2. finds fatcat_container ES docs whose `issns` contain any of those and,
     unless --all is passed, are not already in_doaj in ES
  3. for each, GETs the container from fatcat2. If extra.doaj is missing, sets
     it to what trawler's journal importer would have written ({"id": <DOAJ
     journal id>, "added_on": <date>}, from the CSV row) and PUTs the container
     back. fatcat2's PUT replaces the container wholesale, so it sends back
     everything it got with only extra.doaj added.

An existing extra.doaj is never touched; legacy containers carry one with
as_of, seal, etc., which is worth keeping. That also makes re-running safe.

Dry-run by default: GETs only, and reports what it would write. Pass --apply
to PUT. --limit caps how many containers are looked at, for trying a few
first.

Endpoints and API key come from ~/.config/trawler/config.toml, the same file
trawler uses. The CSV is fetched from https://doaj.org/csv unless --csv names
a local copy.
"""
import argparse
import base64
import uuid

from doaj_journals import (batches, container_query, es, http_json,
                           load_config, load_doaj)


def legacy_to_uuid(ident: str) -> uuid.UUID:
    """base32 legacy fatcat ident (the ES _id) -> UUID (mirrors fatcat2.fc2uuid)."""
    raw = base64.b32decode((ident + "======").upper())
    if len(raw) != 16:
        raise ValueError(f"{ident!r} decoded to {len(raw)} bytes, expected 16")
    return uuid.UUID(bytes=raw)


def find_containers(es_url: str, ix: str, issns: set[str], batch_size: int,
                    unmarked_only: bool) -> dict[str, list[str]]:
    """ES _id -> its issns, for every container doc matching any of issns."""
    out: dict[str, list[str]] = {}
    todo = batches(issns, batch_size)
    for n, batch in enumerate(todo, 1):
        res = es("POST", f"{es_url}/{ix}/_search", {
            "size": 10000,
            "track_total_hits": True,
            "_source": ["issns"],
            "query": container_query(batch, unmarked_only),
        })
        total = res["hits"]["total"]["value"]
        hits = res["hits"]["hits"]
        if total > len(hits):
            raise SystemExit(f"batch {n} matched {total} docs, more than one page; "
                             "lower --batch-size")
        for h in hits:
            out[h["_id"]] = h["_source"].get("issns") or []
        if n % 10 == 0 or n == len(todo):
            print(f"searching ES: batch {n}/{len(todo)}, {len(out)} containers", flush=True)
    return out


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--apply", action="store_true",
                    help="actually PUT to fatcat2 (default: dry-run, GET-only)")
    ap.add_argument("--csv", help="local DOAJ journal CSV (default: fetch from doaj.org)")
    ap.add_argument("--all", action="store_true",
                    help="check every matching container, not only those not yet in_doaj in ES")
    ap.add_argument("--limit", type=int, help="look at no more than this many containers")
    ap.add_argument("--batch-size", type=int, default=500,
                    help="ISSNs per ES request (default: 500)")
    args = ap.parse_args()

    cfg = load_config()
    fc2 = cfg["fatcat2"]["endpoint"].rstrip("/")
    key = cfg["fatcat2"].get("key", "")
    es_url = cfg["indexing"]["elasticsearch_url"].rstrip("/")
    ix = cfg["indexing"]["fatcat_container_ix"]
    if args.apply and not key:
        raise SystemExit("no fatcat2.key in config; --apply needs one")

    print("mode: " + ("APPLY (writing to fatcat2)" if args.apply
                       else "DRY-RUN (no changes; pass --apply to write)"))
    print(f"fatcat2: {fc2}")
    print(f"es:      {es_url}   index: {ix}\n")

    doaj = load_doaj(args.csv)
    issns = doaj.all_issns()
    by_issn = doaj.by_issn()
    print(f"csv rows: {doaj.rows}  without a valid ISSN: {doaj.no_issn}  ISSNs to match: {len(issns)}")

    containers = find_containers(es_url, ix, issns, args.batch_size, not args.all)
    idents = sorted(containers)
    if args.limit is not None:
        idents = idents[:args.limit]
    print(f"containers to check: {len(idents)}\n")

    written = has_doaj = missing = failures = 0
    for n, ident in enumerate(idents, 1):
        cid = legacy_to_uuid(ident)
        prefix = f"[{n}/{len(idents)}] {ident} {cid}"

        status, c = http_json("GET", f"{fc2}/container/{cid}")
        if status == 404:
            print(f"{prefix}: not in fatcat2")
            missing += 1
            continue
        if status != 200 or not isinstance(c, dict):
            print(f"{prefix}: GET -> {status}: {str(c)[:200]}")
            failures += 1
            continue

        extra = c.get("extra") or {}
        if "doaj" in extra:
            has_doaj += 1
            continue

        journal = next((by_issn[i] for i in containers[ident] if i in by_issn), None)
        if journal is None:
            # Can't happen: ES matched it on one of these ISSNs.
            print(f"{prefix}: no DOAJ journal for issns {containers[ident]}")
            failures += 1
            continue

        doaj_extra = journal.doaj_extra()
        c["extra"] = {**extra, "doaj": doaj_extra}
        if not args.apply:
            print(f"{prefix} {c.get('name')!r}: would set extra.doaj = {doaj_extra}")
            written += 1
            continue

        status, body = http_json("PUT", f"{fc2}/container", c, {"X-API-Key": key})
        if status != 200:
            print(f"{prefix}: PUT -> {status}: {str(body)[:200]}")
            failures += 1
            continue
        print(f"{prefix} {c.get('name')!r}: set extra.doaj = {doaj_extra}")
        written += 1

    print(f"\nchecked: {len(idents)}")
    print(f"{'written' if args.apply else 'would write'}: {written}")
    print(f"already had extra.doaj: {has_doaj}")
    print(f"not in fatcat2: {missing}")
    print(f"failures: {failures}")
    return 1 if failures else 0


if __name__ == "__main__":
    raise SystemExit(main())
