import argparse
import logging
import os
import sys
import uuid

import diskcache
import psycopg
from psycopg.rows import dict_row
from psycopg.types.json import Jsonb


# SELECT DISTINCT e.ident_id
# FROM release_edit e
# JOIN changelog c ON c.editgroup_id = e.editgroup_id
# WHERE c.timestamp >= '2025-06-01'
#   AND c.timestamp < '2026-04-01'

# i want each "step" of this program to be a full _release_. so, the required
# work/container (parents) and the tree of children (refs, contribs). due to
# how our ingestion has always worked, i consider file to be a child of
# release, so files, too (and their children).

# our input is the set of relead idents that appeared in the changelog between
# 2025-06-01 and 2026-04-01.

# +------------+------------+
# | entity     |  count     |
# |------------+------------+
# | release    | 32,834,765 |
# | work       | 31,594,730 |
# | file       | 703,258    |
# | container  | 1,392      |
# | webcapture | 9          |
# +------------+------------+

# for each release id:
# - get details
# - see if uuid already in db (cache this locally)
# - see if work is already in db, insert if not
# - see if container is already in db, insert if not
# - in transaction:
#  - create release
#  - insert file
#  - insert releaseextid (handling legacy as needed)
#  - insert releaseabstract
#  - insert releasecontrib
#  - insert releaseref

logger = logging.getLogger("fcpatch")
logger.setLevel(logging.INFO)
_handler = logging.StreamHandler(sys.stdout)
_handler.setFormatter(logging.Formatter("%(asctime)s [%(levelname)s] %(message)s"))
logger.addHandler(_handler)

SOURCE = "legacy_patch"

RELEASE_GET = f"""
  SELECT
      ri.id,
      rr.id AS legacy_rev,
      to_json(rr.extra_json) AS extra,
      '{SOURCE}' AS source,
      rr.title,
      rr.original_title,
      rr.subtitle,
      rr.release_type,
      rr.release_stage,
      rr.release_date,
      rr.release_year,
      rr.volume,
      rr.issue,
      rr.pages,
      rr.number,
      rr.version,
      rr.publisher,
      rr.language,
      rr.license_slug,
      rr.withdrawn_status,

      rr.work_ident_id AS work_id,
      rr.container_ident_id AS container_id,
      rr.doi AS legacy_doi,
      rr.pmid AS legacy_pmid,
      rr.pmcid AS legacy_pmcid,
      rr.wikidata_qid AS legacy_wikidata_qid,
      rr.core_id AS legacy_core_id,

      (SELECT to_json(refs_json)
       FROM refs_blob rb
       WHERE rb.sha1 = rr.refs_blob_sha1) AS refs
  FROM release_ident ri
  JOIN release_rev rr ON ri.rev_id = rr.id
  WHERE ri.id = %s
    AND ri.is_live = true
    AND ri.redirect_id IS NULL
"""

WORK_GET = f"""
  SELECT
    wi.id,
    wi.rev_id AS legacy_rev,
    '{SOURCE}' AS source,
    to_json(wr.extra_json) AS extra
  FROM work_ident wi
  JOIN work_rev wr ON wi.rev_id = wr.id
  WHERE wi.id = %s
    AND wi.redirect_id IS NULL
"""

CONTAINER_GET = f"""
  SELECT
      ci.id,
      ci.rev_id as legacy_rev,
      cr.name,
      to_json(cr.extra_json) AS extra,
      cr.container_type,
      cr.publisher,
      cr.issnl,
      cr.issne,
      cr.issnp,
      cr.wikidata_qid,
      '{SOURCE}' AS source
    FROM container_ident ci
    JOIN container_rev cr ON ci.rev_id = cr.id
    WHERE ci.id = %s
    AND coalesce(trim(cr.container_type), '') != 'test'
"""

RELEASE_EXTIDS_GET = """
  SELECT
    ei.extid_type AS id_type,
    ei.value AS id_value
  FROM release_rev_extid ei
  WHERE ei.release_rev = %s
"""

RELEASE_CONTRIBS_GET = """
  SELECT
    rc.raw_name,
    rc.given_name,
    rc.surname,
    rc.creator_ident_id AS creator_id,
    rc.role,
    rc.raw_affiliation,
    rc.index_val AS position,
    to_json(rc.extra_json) AS extra
  FROM release_contrib rc
  WHERE rc.release_rev = %s
"""

CREATOR_GET = f"""
  SELECT
    ci.id,
    ci.rev_id AS legacy_rev,
    cr.display_name,
    cr.given_name,
    cr.surname,
    cr.orcid,
    '{SOURCE}' AS source,
    to_json(cr.extra_json) AS extra
  FROM creator_ident ci
  JOIN creator_rev cr ON ci.rev_id = cr.id
  WHERE ci.id = %s
    AND ci.is_live = true
    AND ci.redirect_id IS NULL
"""

RELEASE_REFS_GET = """
  SELECT
    rr.index_val AS position,
    rr.target_release_ident_id AS target_release_id
  FROM release_ref rr
  WHERE rr.release_rev = %s
"""

RELEASE_FILES_GET = f"""
  SELECT
    fi.id,
    fi.rev_id AS legacy_rev,
    '{SOURCE}' AS source,
    fr.size_bytes,
    fr.sha1,
    fr.sha256,
    fr.mimetype,
    fr.md5,
    to_json(fr.extra_json) AS extra
  FROM file_ident fi
  JOIN file_rev fr ON fi.rev_id = fr.id
  JOIN file_rev_release frr ON fi.rev_id = frr.file_rev
  WHERE frr.target_release_ident_id = %s
    AND fi.is_live = true
    AND fi.redirect_id IS NULL
"""

FILE_URLS_GET = """
  SELECT
    fu.rel,
    fu.url
  FROM file_rev_url fu
  JOIN file_ident fi ON fi.rev_id = fu.file_rev
  WHERE fi.id = %s
    AND fi.is_live = true
    AND fi.redirect_id IS NULL
"""

RELEASE_ABSTRACTS_GET = """
  SELECT
    ra.abstract_sha1 AS sha1,
    ra.mimetype,
    ra.lang AS language,
    (SELECT content
     FROM abstracts a
     WHERE ra.abstract_sha1 = a.sha1) AS content
  FROM release_ident ri
  JOIN release_rev_abstract ra ON ri.rev_id = ra.release_rev
  WHERE ri.id = %s
    AND ri.is_live = true
    AND ri.redirect_id IS NULL
"""

LEGACY_EXTID_COLS = ["doi", "pmid", "pmcid", "wikidata_qid", "core_id"]


def get_release_data(old_conn: psycopg.Connection, rid: uuid.UUID) -> dict[str, any] | None:
    logger.info(f"{rid}: fetching old release")

    # phase 1: release must come first; everything else depends on it
    release = old_conn.execute(RELEASE_GET, [rid]).fetchone()
    if release is None:
        logger.warn(f"{rid}: not found in fatcat1")
        return None

    legacy_rev = release["legacy_rev"]
    wid = release["work_id"]
    con_id = release["container_id"]

    # phase 2: pipeline all independent queries (1 round-trip instead of 7)
    with old_conn.pipeline():
        work_cur = old_conn.execute(WORK_GET, [wid])
        container_cur = old_conn.execute(CONTAINER_GET, [con_id]) if con_id else None
        extids_cur = old_conn.execute(RELEASE_EXTIDS_GET, [legacy_rev])
        contribs_cur = old_conn.execute(RELEASE_CONTRIBS_GET, [legacy_rev])
        refs_cur = old_conn.execute(RELEASE_REFS_GET, [legacy_rev])
        files_cur = old_conn.execute(RELEASE_FILES_GET, [rid])
        abstracts_cur = old_conn.execute(RELEASE_ABSTRACTS_GET, [rid])

        work = work_cur.fetchone()
        container = container_cur.fetchone() if container_cur else None
        extids = extids_cur.fetchall()
        contribs = contribs_cur.fetchall()
        refs = refs_cur.fetchall()
        files = files_cur.fetchall()
        abstracts = abstracts_cur.fetchall()

    if work is None:
        logger.warn(f"{rid}: work {wid} not found in fatcat1")
        return None

    if con_id and container is None:
        logger.warn(f"{rid}: container {con_id} not found in fatcat1")
        return None

    # phase 3: pipeline per-contrib creator and per-file URL queries
    creator_ids = list({c["creator_id"] for c in contribs if c.get("creator_id")})
    file_ids = [f["id"] for f in files]

    creators = {}
    file_urls = {}

    if creator_ids or file_ids:
        with old_conn.pipeline():
            creator_curs = {cid: old_conn.execute(CREATOR_GET, [cid]) for cid in creator_ids}
            file_url_curs = {fid: old_conn.execute(FILE_URLS_GET, [fid]) for fid in file_ids}

            for cid, cur in creator_curs.items():
                creator = cur.fetchone()
                if creator:
                    creators[cid] = creator

            for fid, cur in file_url_curs.items():
                file_urls[fid] = cur.fetchall()

    logger.info(f"{rid}: fetched ({len(contribs)} contribs, {len(creators)} creators, "
                f"{len(extids)} extids, {len(refs)} refs, {len(abstracts)} "
                f"abstracts, {len(files)} files)")

    return {
            'release': release,
            'work': work,
            'container': container,
            'creators': creators,
            'extids': extids,
            'contribs': contribs,
            'files': files,
            'abstracts': abstracts,
            'refs': refs,
            'file_urls': file_urls,
            }


def insert_release_main(new_conn: psycopg.Connection,
                        data: dict[str, any]) -> list[tuple[psycopg.Cursor, uuid.UUID]]:
    """Insert everything except file URLs. Returns list of (cursor, file_id) for
    file inserts that used RETURNING, so the caller can check which files were
    newly inserted and pipeline the file URL inserts separately."""
    release = data['release']
    rid = release['id']
    work = data['work']
    container = data['container']
    creators = data['creators']
    extids = data['extids']
    files = data['files']
    abstracts = data['abstracts']
    refs = data['refs']
    contribs = data['contribs']

    new_conn.execute("""
        INSERT INTO fcapi_work (id, legacy_rev, source, extra)
        VALUES (%(id)s, %(legacy_rev)s, %(source)s, %(extra)s)
        ON CONFLICT (id) DO NOTHING
    """, {**work, "extra": Jsonb(work["extra"]) if work["extra"] is not None else None})

    if container:
        new_conn.execute("""
            INSERT INTO fcapi_container (
            id, legacy_rev, name, extra, container_type, publisher,
            issnl, issne, issnp, wikidata_qid, source)
            VALUES (
            %(id)s, %(legacy_rev)s, %(name)s, %(extra)s, %(container_type)s,
            %(publisher)s, %(issnl)s, %(issne)s, %(issnp)s,
            %(wikidata_qid)s, %(source)s)
            ON CONFLICT (id) DO NOTHING
        """, {**container, "extra": Jsonb(
            container["extra"]) if container["extra"] is not None else None})

    for creator in creators.values():
        new_conn.execute("""
            INSERT INTO fcapi_creator (
            id, legacy_rev, display_name, given_name, surname,
            orcid, source, extra)
            VALUES (
            %(id)s, %(legacy_rev)s, %(display_name)s, %(given_name)s,
            %(surname)s, %(orcid)s, %(source)s, %(extra)s)
            ON CONFLICT (id) DO NOTHING
        """, {**creator, "extra": Jsonb(creator["extra"]) if creator["extra"] is not None else None})

    new_conn.execute("""
        INSERT INTO fcapi_release (
        id, legacy_rev, extra, source, title, original_title, subtitle,
        release_type, release_stage, release_date, release_year,
        volume, issue, pages, number, version, publisher, language,
        license_slug, withdrawn_status, work_id, container_id,
        legacy_doi, legacy_pmid, legacy_pmcid, legacy_wikidata_qid,
        legacy_core_id, refs)
        VALUES (
        %(id)s, %(legacy_rev)s, %(extra)s, %(source)s, %(title)s,
        %(original_title)s, %(subtitle)s, %(release_type)s,
        %(release_stage)s, %(release_date)s, %(release_year)s,
        %(volume)s, %(issue)s, %(pages)s, %(number)s, %(version)s,
        %(publisher)s, %(language)s, %(license_slug)s,
        %(withdrawn_status)s, %(work_id)s, %(container_id)s,
        %(legacy_doi)s, %(legacy_pmid)s, %(legacy_pmcid)s,
        %(legacy_wikidata_qid)s, %(legacy_core_id)s, %(refs)s)
    """, {
        **release,
        "extra": Jsonb(release["extra"]) if release["extra"] is not None else None,
        "refs": Jsonb(release["refs"]) if release["refs"] is not None else None,
    })

    for extid in extids:
        new_conn.execute("""
            INSERT INTO fcapi_releaseextid (release_id, id_type, id_value)
            VALUES (%s, %s, %s)
        """, [rid, extid["id_type"], extid["id_value"]])

    for col in LEGACY_EXTID_COLS:
        val = release.get(f"legacy_{col}")
        if val:
            new_conn.execute("""
                INSERT INTO fcapi_releaseextid (release_id, id_type, id_value)
                VALUES (%s, %s, %s)
            """, [rid, col, str(val)])

    for contrib in contribs:
        new_conn.execute("""
            INSERT INTO fcapi_releasecontrib (
            release_id, raw_name, given_name, surname, creator_id,
            role, raw_affiliation, position, extra)
            VALUES (
            %(release_id)s, %(raw_name)s, %(given_name)s, %(surname)s,
            %(creator_id)s, %(role)s, %(raw_affiliation)s,
            %(position)s, %(extra)s)
        """, {
            **contrib,
            "release_id": rid,
            "extra": Jsonb(contrib["extra"]) if contrib["extra"] is not None else None,
        })

    for ref in refs:
        new_conn.execute("""
            INSERT INTO fcapi_releaseref (position, release_id, target_release_id)
            VALUES (%s, %s, %s)
        """, [ref["position"], rid, ref["target_release_id"]])

    file_curs = []
    for f in files:
        cur = new_conn.execute("""
            INSERT INTO fcapi_file (
            id, legacy_rev, source, size_bytes, sha1, sha256,
            mimetype, md5, extra)
            VALUES (
            %(id)s, %(legacy_rev)s, %(source)s, %(size_bytes)s,
            %(sha1)s, %(sha256)s, %(mimetype)s, %(md5)s, %(extra)s)
            ON CONFLICT (id) DO NOTHING
            RETURNING id
        """, {**f, "extra": Jsonb(f["extra"]) if f["extra"] is not None else None})
        file_curs.append((cur, f["id"]))

        new_conn.execute("""
            INSERT INTO fcapi_releasefile (release_id, file_id)
            VALUES (%s, %s)
            ON CONFLICT (file_id, release_id) DO NOTHING
        """, [rid, f["id"]])

    for ab in abstracts:
        new_conn.execute("""
            INSERT INTO fcapi_releaseabstract (
            release_id, sha1, mimetype, language, content)
            VALUES (%s, %s, %s, %s, %s)
        """, [rid, ab["sha1"], ab["mimetype"], ab["language"], ab["content"]])

    return file_curs


CHANGELOG_CONTAINER_IDS_GET = """
  SELECT DISTINCT rr.container_ident_id AS container_id
  FROM release_edit e
  JOIN changelog c ON c.editgroup_id = e.editgroup_id
  JOIN release_ident ri ON ri.id = e.ident_id
  JOIN release_rev rr ON ri.rev_id = rr.id
  WHERE c.timestamp >= %s
    AND c.timestamp < %s
    AND ri.is_live = true
    AND ri.redirect_id IS NULL
    AND rr.container_ident_id IS NOT NULL
"""

CONTAINERS_GET = f"""
  SELECT
      ci.id,
      ci.rev_id AS legacy_rev,
      cr.name,
      to_json(cr.extra_json) AS extra,
      cr.container_type,
      cr.publisher,
      cr.issnl,
      cr.issne,
      cr.issnp,
      cr.wikidata_qid,
      '{SOURCE}' AS source
    FROM container_ident ci
    JOIN container_rev cr ON ci.rev_id = cr.id
    WHERE ci.id = ANY(%s)
      AND coalesce(trim(cr.container_type), '') != 'test'
"""


def get_container_ids(old_conn: psycopg.Connection,
                      start: str, end: str) -> set[uuid.UUID]:
    """Return distinct container ident IDs referenced by the current revisions
    of releases that had edits in fatcat1's changelog between start (inclusive)
    and end (exclusive). Mirrors the release-selection SQL used to drive the
    per-release migration."""
    rows = old_conn.execute(
        CHANGELOG_CONTAINER_IDS_GET, [start, end]).fetchall()
    return {row["container_id"] for row in rows}


def migrate_containers(old_conn: psycopg.Connection,
                       new_conn: psycopg.Connection,
                       container_ids: list[uuid.UUID]) -> None:
    """Fetch all containers from fc1 and insert them into fc2 in a single
    transaction. Existing rows are left untouched."""
    if not container_ids:
        logger.info("no containers to migrate")
        return

    logger.info(f"fetching {len(container_ids)} containers from fc1")
    containers = old_conn.execute(
        CONTAINERS_GET, [list(container_ids)]).fetchall()
    logger.info(f"inserting {len(containers)} containers into fc2")

    with new_conn.transaction():
        for container in containers:
            new_conn.execute("""
                INSERT INTO fcapi_container (
                id, legacy_rev, name, extra, container_type, publisher,
                issnl, issne, issnp, wikidata_qid, source)
                VALUES (
                %(id)s, %(legacy_rev)s, %(name)s, %(extra)s, %(container_type)s,
                %(publisher)s, %(issnl)s, %(issne)s, %(issnp)s,
                %(wikidata_qid)s, %(source)s)
                ON CONFLICT (id) DO NOTHING
            """, {**container, "extra": Jsonb(
                container["extra"]) if container["extra"] is not None else None})


def insert_release_group(new_conn, batch_data):
    """Insert a group of releases (and their dependents) inside a single
    transaction and pipeline. The first bad row raises and rolls back the whole
    group, so callers wanting to isolate a bad release retry it on its own."""
    with new_conn.transaction(), new_conn.pipeline():
        # phase 1: buffer all inserts for the whole group (except file URLs)
        all_file_curs = []
        for bd in batch_data:
            file_curs = insert_release_main(new_conn, bd)
            all_file_curs.append((bd['file_urls'], file_curs))

        # phase 2: first fetch flushes the pipeline; check which files were new
        file_url_inserts = []
        for file_urls, file_curs in all_file_curs:
            for cur, fid in file_curs:
                if cur.fetchone() is not None:
                    file_url_inserts.extend(
                        (url["rel"], url["url"], fid) for url in file_urls.get(fid, []))

        # phase 3: buffer file URL inserts; flushed on pipeline exit
        for rel, url, fid in file_url_inserts:
            new_conn.execute("""
                INSERT INTO fcapi_fileurl (rel, url, file_id)
                VALUES (%s, %s, %s)
            """, [rel, url, fid])


def handle_batch(old_conn, new_conn, cache, batch):
    batch_data = [get_release_data(old_conn, rid) for rid in batch]
    releases = []
    for bd in batch_data:
        if bd is None:
            logger.warning("got nil batch data")
            continue
        releases.append(bd)
    if not releases:
        return

    # fast path: insert the whole batch in one transaction + pipeline (few
    # round-trips). a bad row (e.g. a legacy value too long for the fc2 schema)
    # aborts the pipeline and rolls the whole batch back, committing nothing.
    try:
        insert_release_group(new_conn, releases)
        return
    except psycopg.errors.StringDataRightTruncation:
        logger.info(
            f"batch of {len(releases)} hit an oversized column value; "
            "retrying releases individually to isolate it")

    # slow path: redo the batch one release at a time so the good ones still
    # land and the bad one(s) get logged by rid and skipped.
    for bd in releases:
        rid = bd['release']['id']
        try:
            insert_release_group(new_conn, [bd])
        except psycopg.errors.StringDataRightTruncation as e:
            logger.warning(f"{rid}: skipping release; value too long for fc2 column: {e}")


def run_releases(args) -> None:
    old_conn = psycopg.connect(args.old_db_url, row_factory=dict_row)
    new_conn = psycopg.connect(args.new_db_url, row_factory=dict_row, autocommit=True)
    # configure diskcache for basic set membership. letting it have an eviction
    # policy will lead to a huge WAL file.
    cache = diskcache.Cache("fcpatch", eviction_policy="none", size_limit=2**42)

    batch_len: int = int(args.batch)

    def flush(candidates: list[uuid.UUID]) -> None:
        if not candidates:
            return

        # one round-trip resolves the whole batch: which of these ids are
        # already in the new db? (replaces one SELECT per id.)
        with new_conn.cursor() as new_cur:
            rows = new_cur.execute("""
                SELECT id FROM fcapi_release
                WHERE id = ANY(%s)""", [candidates]).fetchall()
        present = {str(row["id"]) for row in rows}

        # a set comprehension dedupes as it filters: the same id can appear more
        # than once in the input, and thus more than once in a single batch. the
        # present-check only compares against what's already in the db, and the
        # diskcache isn't written until the end of this flush, so an in-batch
        # duplicate would otherwise survive into handle_batch and get INSERTed
        # twice -- a duplicate-key violation, since fcapi_release has no ON
        # CONFLICT guard. deduping here means each release is migrated once.
        to_migrate = {rid for rid in candidates if str(rid) not in present}
        logger.info(
            f"batch: {len(candidates)} candidates, {len(present)} already in db, "
            f"{len(to_migrate)} to migrate")
        if to_migrate:
            handle_batch(old_conn, new_conn, cache, to_migrate)

        # cache everything confirmed handled: already-present plus freshly
        # migrated. only reached if handle_batch didn't raise. one transaction
        # for the whole batch instead of a commit per key.
        with cache.transact():
            for rid in candidates:
                cache.set(str(rid), True)

    candidates: list[uuid.UUID] = []
    for line in sys.stdin:
        line = line.strip()
        # skip blank lines (e.g. the trailing newline of piped psql output) so
        # they don't get logged as bogus "corrupt rid:" warnings.
        if not line:
            continue
        try:
            rid = uuid.UUID(line)
        except ValueError:
            logger.warning(f"corrupt rid: {line!r}")
            continue

        # local set-membership check; the db existence check is batched below.
        if cache.get(str(rid)) is not None:
            continue

        candidates.append(rid)
        if len(candidates) == batch_len:
            flush(candidates)
            candidates = []

    flush(candidates)


def run_containers(args) -> None:
    old_conn = psycopg.connect(args.old_db_url, row_factory=dict_row)
    new_conn = psycopg.connect(args.new_db_url, row_factory=dict_row, autocommit=True)

    logger.info(f"finding containers for releases edited in [{args.start}, {args.end})")
    container_ids = get_container_ids(old_conn, args.start, args.end)
    logger.info(f"found {len(container_ids)} distinct container ids")
    migrate_containers(old_conn, new_conn, list(container_ids))


def main() -> None:
    parser = argparse.ArgumentParser(
            description="top off tool between fc1 and fc2")
    parser.add_argument(
        "--old-db-url",
        default=os.environ.get("FCPATCH_OLD_DB_URL", "postgresql:///fatcat_prod"),
        help="Connection URL for fc1 (default: $FCPATCH_OLD_DB_URL)")
    parser.add_argument(
        "--new-db-url",
        default=os.environ.get("FCPATCH_NEW_DB_URL", "postgresql:///fatcat2"),
        help="Connection URL for fc2 (default: $FCPATCH_NEW_DB_URL)")

    subparsers = parser.add_subparsers(dest="command", required=True)

    releases_parser = subparsers.add_parser(
        "releases",
        help="migrate releases (and their dependents) read as UUIDs from stdin")
    releases_parser.add_argument(
        "--batch",
        default=1,
        help="specify batch size; 1 is equivalent to no batching")
    releases_parser.set_defaults(func=run_releases)

    containers_parser = subparsers.add_parser(
        "containers",
        help="pre-migrate containers referenced by releases changed in a changelog window")
    containers_parser.add_argument(
        "--start", required=True,
        help="inclusive lower bound on changelog timestamp (e.g. 2025-06-01)")
    containers_parser.add_argument(
        "--end", required=True,
        help="exclusive upper bound on changelog timestamp (e.g. 2026-04-01)")
    containers_parser.set_defaults(func=run_containers)

    args = parser.parse_args()
    args.func(args)


if __name__ == "__main__":
    main()
