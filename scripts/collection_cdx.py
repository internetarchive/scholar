#!/usr/bin/env -S uv run
# /// script
# requires-python = ">=3.13"
# dependencies = [
#   "internetarchive==5.5.0",
# ]
# ///

import shutil
import subprocess
import sys
import tempfile

import internetarchive as ia


def main(colname: str) -> None:
    if not is_clean_colname(colname):
        raise Exception("collection name does not look right...")

    tempdir = tempfile.mkdtemp()

    items = list(map(lambda m: m["identifier"],
                     ia.search_items(
                         query=f"collection:{colname} mediatype:web")))

    if len(items) == 0:
        raise Exception("no items found")

    print(f"found {len(items)} potential items")

    errors = []
    for item in items:
        try:
            ia.download(
                    item,
                    files=[item + ".cdx.gz"],
                    verbose=True,
                    destdir=tempdir,
                    no_directory=True,
                    retries=1000,
                    )
        except Exception as e:
            print(f"'{item} got error {e}", file=sys.stderr)
            errors.append(e)

    if len(errors) == len(items):
        raise Exception("all items failed to download")

    print("Merging and re-compressing all CDX files...")
    subprocess.run(
            f"zcat {tempdir}/*.cdx.gz | gzip > {tempdir}/combined.gz",
            shell=True)
    shutil.move(f"{tempdir}/combined.gz", f"{colname}.gz")


def is_clean_colname(colname: str) -> bool:
    return colname.replace("_", "").replace("-", "").replace(".", "").isalnum()


if __name__ == "__main__":
    if len(sys.argv) != 2:
        print("expected 1 argument (collection name)", file=sys.stderr)
        sys.exit(1)

    try:
        main(sys.argv[1])
    except Exception as e:
        print(f"failed: {e}", file=sys.stderr)
        sys.exit(3)
