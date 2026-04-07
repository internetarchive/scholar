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


def handle_abstracts(old_conn: psycopg.Connection,
                     new_conn: psycopg.Connection,
                     cache: diskcache.Cache,
                     rid: uuid.UUID) -> None:
    with old_conn.cursor() as old_cur, new_conn.cursor() as new_cur:
        abstracts = old_cur.execute(RELEASE_ABSTRACTS_GET, [rid]).fetchall()
        for ab in abstracts:
            new_cur.execute("""
                INSERT INTO fcapi_releaseabstract (
                release_id, sha1, mimetype, language, content)
                VALUES (%s, %s, %s, %s, %s)
            """, [rid, ab["sha1"], ab["mimetype"], ab["language"], ab["content"]])
        logger.info(f"{str(rid)}: inserted {len(abstracts)} abstracts")


def handle(old_conn: psycopg.Connection,
           new_conn: psycopg.Connection,
           cache: diskcache.Cache,
           line: str) -> None:
    rid = uuid.UUID(line.strip())
    if cache.get(str(rid)) is not None:
        if cache.get(str(rid) + "_abstracts") is None:
            logger.info(f"{str(rid)}: needs abstracts")
            handle_abstracts(old_conn, new_conn, cache, rid)
            cache.set(str(rid) + "_abstracts", True)
            logger.info(f"{str(rid)}: abstracts added")
        logger.info(f"{rid}: found in cache, skipping ahead")
        return

    with old_conn.cursor() as old_cur, new_conn.cursor() as new_cur:
        row = new_cur.execute("""
          SELECT 1 as found FROM fcapi_release
          WHERE id = %s""", [rid]).fetchone()
        if row is not None:
            cache.set(str(rid), "done")
            logger.info(f"{rid}: found in db, skipping")
            return

        logger.info(f"{rid}: fetching old release")

        release = old_cur.execute(RELEASE_GET, [rid]).fetchone()
        if release is None:
            logger.warn(f"{rid}: not found in fatcat1")
            return

        wid = release["work_id"]
        work = old_cur.execute(WORK_GET, [wid]).fetchone()
        if work is None:
            logger.warn(f"{rid}: work {wid} not found in fatcat1")
            return

        con_id = release["container_id"]
        container = None
        if con_id:
            container = old_cur.execute(CONTAINER_GET, [con_id]).fetchone()
            if container is None:
                logger.warn(f"{rid}: container {con_id} not found in fatcat1")
                return

        legacy_rev = release["legacy_rev"]

        extids = old_cur.execute(RELEASE_EXTIDS_GET, [legacy_rev]).fetchall()
        contribs = old_cur.execute(RELEASE_CONTRIBS_GET, [legacy_rev]).fetchall()
        refs = old_cur.execute(RELEASE_REFS_GET, [legacy_rev]).fetchall()
        files = old_cur.execute(RELEASE_FILES_GET, [rid]).fetchall()

        creators = {}
        for contrib in contribs:
            cid = contrib.get("creator_id")
            if not cid or cid in creators:
                continue
            creator = old_cur.execute(CREATOR_GET, [cid]).fetchone()
            if creator:
                creators[cid] = creator

        file_urls = {}
        for f in files:
            file_urls[f["id"]] = old_cur.execute(FILE_URLS_GET, [f["id"]]).fetchall()

        logger.info(f"{rid}: inserting ({len(contribs)} contribs, {len(creators)} creators, "
                    f"{len(extids)} extids, {len(refs)} refs, {len(files)} files)")

        with new_conn.transaction():
            new_cur.execute("""
              INSERT INTO fcapi_work (id, legacy_rev, source, extra)
              VALUES (%(id)s, %(legacy_rev)s, %(source)s, %(extra)s)
              ON CONFLICT (id) DO NOTHING
            """, {**work, "extra": Jsonb(work["extra"]) if work["extra"] is not None else None})

            if container:
                new_cur.execute("""
                  INSERT INTO fcapi_container (
                    id, legacy_rev, name, extra, container_type, publisher,
                    issnl, issne, issnp, wikidata_qid, source)
                  VALUES (
                    %(id)s, %(legacy_rev)s, %(name)s, %(extra)s, %(container_type)s,
                    %(publisher)s, %(issnl)s, %(issne)s, %(issnp)s,
                    %(wikidata_qid)s, %(source)s)
                  ON CONFLICT (id) DO NOTHING
                """, {**container,
                      "extra": Jsonb(
                          container["extra"]) if container["extra"] is not None else None})

            for creator in creators.values():
                new_cur.execute("""
                  INSERT INTO fcapi_creator (
                    id, legacy_rev, display_name, given_name, surname,
                    orcid, source, extra)
                  VALUES (
                    %(id)s, %(legacy_rev)s, %(display_name)s, %(given_name)s,
                    %(surname)s, %(orcid)s, %(source)s, %(extra)s)
                  ON CONFLICT (id) DO NOTHING
                """, {**creator,
                      "extra": Jsonb(creator["extra"]) if creator["extra"] is not None else None})

            new_cur.execute("""
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
                new_cur.execute("""
                  INSERT INTO fcapi_releaseextid (release_id, id_type, id_value)
                  VALUES (%s, %s, %s)
                """, [rid, extid["id_type"], extid["id_value"]])

            for col in LEGACY_EXTID_COLS:
                val = release.get(f"legacy_{col}")
                if val:
                    new_cur.execute("""
                      INSERT INTO fcapi_releaseextid (release_id, id_type, id_value)
                      VALUES (%s, %s, %s)
                    """, [rid, col, str(val)])

            for contrib in contribs:
                new_cur.execute("""
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
                new_cur.execute("""
                  INSERT INTO fcapi_releaseref (position, release_id, target_release_id)
                  VALUES (%s, %s, %s)
                """, [ref["position"], rid, ref["target_release_id"]])

            for f in files:
                inserted = new_cur.execute("""
                  INSERT INTO fcapi_file (
                    id, legacy_rev, source, size_bytes, sha1, sha256,
                    mimetype, md5, extra)
                  VALUES (
                    %(id)s, %(legacy_rev)s, %(source)s, %(size_bytes)s,
                    %(sha1)s, %(sha256)s, %(mimetype)s, %(md5)s, %(extra)s)
                  ON CONFLICT (id) DO NOTHING
                  RETURNING id
                """, {**f,
                      "extra": Jsonb(f["extra"]) if f["extra"] is not None else None}).fetchone()

                new_cur.execute("""
                  INSERT INTO fcapi_releasefile (release_id, file_id)
                  VALUES (%s, %s)
                  ON CONFLICT (file_id, release_id) DO NOTHING
                """, [rid, f["id"]])

                if inserted is not None:
                    for url in file_urls[f["id"]]:
                        new_cur.execute("""
                          INSERT INTO fcapi_fileurl (rel, url, file_id)
                          VALUES (%s, %s, %s)
                        """, [url["rel"], url["url"], f["id"]])

            handle_abstracts(old_conn, new_conn, cache, rid)
            cache.set(str(rid) + "_abstracts", True)

    cache.set(str(rid), "done")
    logger.info(f"{rid}: done")
    return


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

    args = parser.parse_args()
    old_conn = psycopg.connect(args.old_db_url, row_factory=dict_row)
    new_conn = psycopg.connect(args.new_db_url, row_factory=dict_row, autocommit=True)
    cache = diskcache.Cache("fcpatch")

    for line in sys.stdin:
        handle(old_conn, new_conn, cache, line)


if __name__ == "__main__":
    main()
