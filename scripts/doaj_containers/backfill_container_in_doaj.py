# /// script
# requires-python = ">=3.11"
# dependencies = []
# ///
"""
Backfill in_doaj=true on fatcat_container ES docs for journals in DOAJ.

trawler now sets in_doaj when it indexes a container created from DOAJ's
journal list, but containers that already existed are left alone by that
import and never re-indexed. This marks them, using DOAJ's journal CSV as the
source of truth.

For each CSV row it:
  1. normalizes the print and electronic ISSNs (mirrors trawler's
     issn.Normalize; malformed ISSNs and bad check digits are dropped)
  2. adds the ISSN-L each maps to in trawler's ISSN-L table, since legacy
     containers often carry only an ISSN-L
  3. matches container docs whose `issns` (ISSN-L, print and electronic)
     contain any of those, and which are not already in_doaj

Dry-run by default: counts what would change and touches nothing. Pass --apply
to set in_doaj=true via _update_by_query. Re-running is safe; docs already
marked are skipped by the query itself.

Only ever sets in_doaj to true. Containers for journals since dropped from
DOAJ keep whatever they have.

Run backfill_container_doaj_extra.py first: it writes extra.doaj onto these
containers in fatcat2, which is what keeps in_doaj set when trawler re-indexes
one.

The ES endpoint and index come from ~/.config/trawler/config.toml, the same
file trawler uses. The CSV is fetched from https://doaj.org/csv unless --csv
names a local copy.
"""
import argparse

from doaj_journals import batches, container_query, es, load_config, load_doaj


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--apply", action="store_true",
                    help="actually update ES (default: dry-run, counts only)")
    ap.add_argument("--csv", help="local DOAJ journal CSV (default: fetch from doaj.org)")
    ap.add_argument("--batch-size", type=int, default=500,
                    help="ISSNs per ES request (default: 500)")
    args = ap.parse_args()

    cfg = load_config()
    es_url = cfg["indexing"]["elasticsearch_url"].rstrip("/")
    ix = cfg["indexing"]["fatcat_container_ix"]

    print("mode: " + ("APPLY (updating ES)" if args.apply
                       else "DRY-RUN (no changes; pass --apply to update)"))
    print(f"es:   {es_url}   index: {ix}\n")

    doaj = load_doaj(args.csv)
    issns = doaj.all_issns()
    print(f"csv rows: {doaj.rows}  without a valid ISSN: {doaj.no_issn}  ISSNs to match: {len(issns)}")

    before = es("POST", f"{es_url}/{ix}/_count", {"query": {"term": {"in_doaj": True}}})["count"]
    print(f"containers already in_doaj: {before}\n")

    todo = batches(issns, args.batch_size)
    matched = pending = updated = failures = 0
    for n, batch in enumerate(todo, 1):
        m = es("POST", f"{es_url}/{ix}/_count", {"query": container_query(batch, False)})["count"]
        matched += m
        if not args.apply:
            p = es("POST", f"{es_url}/{ix}/_count", {"query": container_query(batch, True)})["count"]
            pending += p
            print(f"batch {n}/{len(todo)}: matched {m}, would mark {p}")
            continue

        res = es("POST", f"{es_url}/{ix}/_update_by_query?conflicts=proceed&refresh=true", {
            "query": container_query(batch, True),
            "script": {"source": "ctx._source.in_doaj = true", "lang": "painless"},
        })
        updated += res.get("updated", 0)
        failures += len(res.get("failures", []))
        print(f"batch {n}/{len(todo)}: matched {m}, marked {res.get('updated', 0)}"
              + (f", version conflicts {res['version_conflicts']}" if res.get("version_conflicts") else "")
              + (f", FAILURES {len(res['failures'])}" if res.get("failures") else ""))

    # NB matched counts can overlap between batches when one container's ISSNs
    # land in different batches; the marked counts cannot.
    print(f"\nmatched (may double count): {matched}")
    if args.apply:
        after = es("POST", f"{es_url}/{ix}/_count", {"query": {"term": {"in_doaj": True}}})["count"]
        print(f"marked: {updated}  failures: {failures}")
        print(f"containers in_doaj: {before} -> {after}")
    else:
        print(f"would mark (may double count): {pending}")
    return 1 if failures else 0


if __name__ == "__main__":
    raise SystemExit(main())
