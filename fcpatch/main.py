"""
Incremental migration from fatcat_prod to fatcat2.

Discovers entities accepted within a date range via the old fatcat changelog
and edit tables, reads their current state, and inserts them into the new
database with ON CONFLICT DO NOTHING for idempotency.

Run with uv from the project root:
    uv run python main.py --from-date 2025-05-01 --to-date 2026-04-01
"""
import argparse
import dataclasses
import logging
import os
import sys

import psycopg
from psycopg.rows import dict_row
from psycopg.types.json import Json

SOURCE = "legacy_patch"
LEGACY_EXTID_COLS = ["doi", "pmid", "pmcid", "wikidata_qid", "core_id"]
ENTITY_TYPES = ["container", "creator", "work", "release", "file", "fileset", "webcapture"]
DEFAULT_BATCH_SIZE = 100

logger = logging.getLogger("fcpatch")
logger.setLevel(logging.INFO)
_handler = logging.StreamHandler(sys.stdout)
_handler.setFormatter(logging.Formatter("%(asctime)s [%(levelname)s] %(message)s"))
logger.addHandler(_handler)


@dataclasses.dataclass
class Stats:
    entity: str
    found: int = 0
    inserted: int = 0
    skipped: int = 0
    children: int = 0


# ---------------------------------------------------------------------------
# Discovery: find idents touched in the date range
# ---------------------------------------------------------------------------

def discover_idents(cur, entity_type: str, from_date: str, to_date: str) -> list[str]:
    cur.execute(f"""
        SELECT DISTINCT e.ident_id
        FROM {entity_type}_edit e
        JOIN changelog c ON c.editgroup_id = e.editgroup_id
        WHERE c.timestamp >= %(from_date)s
          AND c.timestamp < %(to_date)s
    """, {"from_date": from_date, "to_date": to_date})
    return [row["ident_id"] for row in cur.fetchall()]


# ---------------------------------------------------------------------------
# Read queries — old DB
# ---------------------------------------------------------------------------

READ_ENTITY_SQL = {
    "container": """
        SELECT
            ci.id,
            ci.rev_id AS legacy_rev,
            cr.name,
            cr.extra_json AS extra,
            cr.container_type,
            cr.publisher,
            cr.issnl,
            cr.issne,
            cr.issnp,
            cr.wikidata_qid
        FROM container_ident ci
        JOIN container_rev cr ON ci.rev_id = cr.id
        WHERE ci.id = ANY(%(idents)s)
          AND coalesce(trim(cr.container_type), '') != 'test'
    """,
    "creator": """
        SELECT
            ci.id,
            ci.rev_id AS legacy_rev,
            cr.display_name,
            cr.given_name,
            cr.surname,
            cr.orcid,
            cr.extra_json AS extra
        FROM creator_ident ci
        JOIN creator_rev cr ON ci.rev_id = cr.id
        WHERE ci.id = ANY(%(idents)s)
          AND ci.is_live = true
          AND ci.redirect_id IS NULL
    """,
    "work": """
        SELECT
            wi.id,
            wi.rev_id AS legacy_rev,
            wr.extra_json AS extra
        FROM work_ident wi
        JOIN work_rev wr ON wi.rev_id = wr.id
        WHERE wi.id = ANY(%(idents)s)
          AND wi.redirect_id IS NULL
    """,
    "release": """
        SELECT
            ri.id,
            rr.id AS legacy_rev,
            rr.extra_json AS extra,
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
            (SELECT refs_json
             FROM refs_blob rb
             WHERE rb.sha1 = rr.refs_blob_sha1) AS refs
        FROM release_ident ri
        JOIN release_rev rr ON ri.rev_id = rr.id
        WHERE ri.id = ANY(%(idents)s)
          AND ri.is_live = true
          AND ri.redirect_id IS NULL
    """,
    "file": """
        SELECT
            fi.rev_id AS legacy_rev,
            fi.id,
            fr.size_bytes,
            fr.sha1,
            fr.sha256,
            fr.mimetype,
            fr.md5,
            fr.extra_json AS extra
        FROM file_ident fi
        JOIN file_rev fr ON fi.rev_id = fr.id
        WHERE fi.id = ANY(%(idents)s)
          AND fi.is_live = true
          AND fi.redirect_id IS NULL
    """,
    "fileset": """
        SELECT
            fi.rev_id AS legacy_rev,
            fi.id,
            fr.extra_json AS extra,
            (SELECT target_release_ident_id
             FROM fileset_rev_release frr
             WHERE frr.fileset_rev = fi.rev_id) AS release_id
        FROM fileset_ident fi
        JOIN fileset_rev fr ON fi.rev_id = fr.id
        WHERE fi.id = ANY(%(idents)s)
          AND fi.is_live = true
          AND fi.redirect_id IS NULL
    """,
    "webcapture": """
        SELECT
            wi.rev_id AS legacy_rev,
            wi.id,
            wr.extra_json AS extra,
            wr.original_url,
            wr.timestamp AS captured,
            (SELECT target_release_ident_id
             FROM webcapture_rev_release
             WHERE webcapture_rev = wr.id) AS release_id
        FROM webcapture_ident wi
        JOIN webcapture_rev wr ON wi.rev_id = wr.id
        WHERE wi.id = ANY(%(idents)s)
          AND wi.is_live = true
          AND wi.redirect_id IS NULL
    """,
}

READ_CHILD_SQL = {
    "releaseextid": """
        SELECT
            ri.id AS release_id,
            ei.extid_type AS id_type,
            ei.value AS id_value
        FROM release_ident ri
        JOIN release_rev_extid ei ON ri.rev_id = ei.release_rev
        WHERE ri.id = ANY(%(idents)s)
          AND ri.is_live = true
          AND ri.redirect_id IS NULL
    """,
    "releaseabstract": """
        SELECT
            ri.id AS release_id,
            ra.abstract_sha1 AS sha1,
            ra.mimetype,
            ra.lang AS language,
            (SELECT content
             FROM abstracts a
             WHERE ra.abstract_sha1 = a.sha1) AS content
        FROM release_ident ri
        JOIN release_rev_abstract ra ON ri.rev_id = ra.release_rev
        WHERE ri.id = ANY(%(idents)s)
          AND ri.is_live = true
          AND ri.redirect_id IS NULL
    """,
    "releasecontrib": """
        SELECT
            ri.id AS release_id,
            rc.raw_name,
            rc.given_name,
            rc.surname,
            rc.creator_ident_id AS creator_id,
            rc.role,
            rc.raw_affiliation,
            rc.index_val AS position,
            rc.extra_json AS extra
        FROM release_ident ri
        JOIN release_contrib rc ON ri.rev_id = rc.release_rev
        WHERE ri.id = ANY(%(idents)s)
          AND ri.is_live = true
          AND ri.redirect_id IS NULL
    """,
    "releaseref": """
        SELECT
            rr.index_val AS position,
            ri.id AS release_id,
            rr.target_release_ident_id AS target_release_id
        FROM release_ident ri
        JOIN release_ref rr ON ri.rev_id = rr.release_rev
        WHERE ri.id = ANY(%(idents)s)
          AND ri.is_live = true
          AND ri.redirect_id IS NULL
    """,
    "releasefile": """
        SELECT
            frr.target_release_ident_id AS release_id,
            fi.id AS file_id
        FROM file_ident fi
        JOIN file_rev_release frr ON fi.rev_id = frr.file_rev
        WHERE fi.id = ANY(%(idents)s)
          AND fi.is_live = true
          AND fi.redirect_id IS NULL
    """,
    "fileurl": """
        SELECT
            fu.rel,
            fu.url,
            fi.id AS file_id
        FROM file_ident fi
        JOIN file_rev_url fu ON fi.rev_id = fu.file_rev
        WHERE fi.id = ANY(%(idents)s)
          AND fi.is_live = true
          AND fi.redirect_id IS NULL
    """,
    "filesetfile": """
        SELECT
            fi.id AS fileset_id,
            ff.path_name,
            ff.size_bytes,
            ff.md5,
            ff.sha1,
            ff.sha256,
            ff.mimetype,
            ff.extra_json AS extra
        FROM fileset_ident fi
        JOIN fileset_rev_file ff ON fi.rev_id = ff.fileset_rev
        WHERE fi.id = ANY(%(idents)s)
          AND fi.is_live = true
          AND fi.redirect_id IS NULL
    """,
    "fileseturl": """
        SELECT
            fi.id AS fileset_id,
            fu.rel,
            fu.url
        FROM fileset_ident fi
        JOIN fileset_rev_url fu ON fi.rev_id = fu.fileset_rev
        WHERE fi.id = ANY(%(idents)s)
          AND fi.is_live = true
          AND fi.redirect_id IS NULL
    """,
    "webcaptureurl": """
        SELECT
            wi.id AS webcapture_id,
            wu.rel,
            wu.url
        FROM webcapture_ident wi
        JOIN webcapture_rev_url wu ON wu.webcapture_rev = wi.rev_id
        WHERE wi.id = ANY(%(idents)s)
          AND wi.is_live = true
          AND wi.redirect_id IS NULL
    """,
    "webcapturecdx": """
        SELECT
            wi.id AS webcapture_id,
            wc.surt,
            wc.url,
            wc.timestamp AS captured,
            wc.mimetype,
            wc.status_code,
            wc.sha1,
            wc.sha256,
            wc.size_bytes
        FROM webcapture_ident wi
        JOIN webcapture_rev_cdx wc ON wc.webcapture_rev = wi.rev_id
        WHERE wi.id = ANY(%(idents)s)
          AND wi.is_live = true
          AND wi.redirect_id IS NULL
    """,
}


# ---------------------------------------------------------------------------
# Insert queries — new DB
# ---------------------------------------------------------------------------

INSERT_ENTITY_SQL = {
    "container": """
        INSERT INTO fcapi_container (
            id, legacy_rev, name, extra, container_type, publisher,
            issnl, issne, issnp, wikidata_qid, source
        ) VALUES (
            %(id)s, %(legacy_rev)s, %(name)s, %(extra)s, %(container_type)s,
            %(publisher)s, %(issnl)s, %(issne)s, %(issnp)s, %(wikidata_qid)s,
            %(source)s
        ) ON CONFLICT (id) DO NOTHING
    """,
    "creator": """
        INSERT INTO fcapi_creator (
            id, legacy_rev, display_name, given_name, surname, orcid,
            source, extra
        ) VALUES (
            %(id)s, %(legacy_rev)s, %(display_name)s, %(given_name)s,
            %(surname)s, %(orcid)s, %(source)s, %(extra)s
        ) ON CONFLICT (id) DO NOTHING
    """,
    "work": """
        INSERT INTO fcapi_work (id, legacy_rev, source, extra)
        VALUES (%(id)s, %(legacy_rev)s, %(source)s, %(extra)s)
        ON CONFLICT (id) DO NOTHING
    """,
    "release": """
        INSERT INTO fcapi_release (
            id, legacy_rev, extra, source, title, original_title, subtitle,
            release_type, release_stage, release_date, release_year, volume,
            issue, pages, number, version, publisher, language, license_slug,
            withdrawn_status, work_id, container_id,
            legacy_doi, legacy_pmid, legacy_pmcid, legacy_wikidata_qid,
            legacy_core_id, refs
        ) VALUES (
            %(id)s, %(legacy_rev)s, %(extra)s, %(source)s, %(title)s,
            %(original_title)s, %(subtitle)s, %(release_type)s,
            %(release_stage)s, %(release_date)s, %(release_year)s,
            %(volume)s, %(issue)s, %(pages)s, %(number)s, %(version)s,
            %(publisher)s, %(language)s, %(license_slug)s,
            %(withdrawn_status)s, %(work_id)s, %(container_id)s,
            %(legacy_doi)s, %(legacy_pmid)s, %(legacy_pmcid)s,
            %(legacy_wikidata_qid)s, %(legacy_core_id)s, %(refs)s
        ) ON CONFLICT (id) DO NOTHING
    """,
    "file": """
        INSERT INTO fcapi_file (
            legacy_rev, id, source, size_bytes, sha1, sha256, mimetype, md5, extra
        ) VALUES (
            %(legacy_rev)s, %(id)s, %(source)s, %(size_bytes)s, %(sha1)s,
            %(sha256)s, %(mimetype)s, %(md5)s, %(extra)s
        ) ON CONFLICT (id) DO NOTHING
    """,
    "fileset": """
        INSERT INTO fcapi_fileset (legacy_rev, id, extra, source, release_id)
        VALUES (%(legacy_rev)s, %(id)s, %(extra)s, %(source)s, %(release_id)s)
        ON CONFLICT (id) DO NOTHING
    """,
    "webcapture": """
        INSERT INTO fcapi_webcapture (
            legacy_rev, id, source, extra, original_url, captured, release_id
        ) VALUES (
            %(legacy_rev)s, %(id)s, %(source)s, %(extra)s, %(original_url)s,
            %(captured)s, %(release_id)s
        ) ON CONFLICT (id) DO NOTHING
    """,
}

INSERT_CHILD_SQL = {
    "releaseextid": """
        INSERT INTO fcapi_releaseextid (release_id, id_type, id_value)
        VALUES (%(release_id)s, %(id_type)s, %(id_value)s)
    """,
    "releaseabstract": """
        INSERT INTO fcapi_releaseabstract (release_id, sha1, mimetype, language, content)
        VALUES (%(release_id)s, %(sha1)s, %(mimetype)s, %(language)s, %(content)s)
    """,
    "releasecontrib": """
        INSERT INTO fcapi_releasecontrib (
            release_id, raw_name, given_name, surname, creator_id,
            role, raw_affiliation, position, extra
        ) VALUES (
            %(release_id)s, %(raw_name)s, %(given_name)s, %(surname)s,
            %(creator_id)s, %(role)s, %(raw_affiliation)s, %(position)s, %(extra)s
        )
    """,
    "releaseref": """
        INSERT INTO fcapi_releaseref (position, release_id, target_release_id)
        VALUES (%(position)s, %(release_id)s, %(target_release_id)s)
    """,
    "releasefile": """
        INSERT INTO fcapi_releasefile (release_id, file_id)
        VALUES (%(release_id)s, %(file_id)s)
        ON CONFLICT ON CONSTRAINT fcapi_releasefile_file_release_uniq DO NOTHING
    """,
    "fileurl": """
        INSERT INTO fcapi_fileurl (rel, url, file_id)
        VALUES (%(rel)s, %(url)s, %(file_id)s)
    """,
    "filesetfile": """
        INSERT INTO fcapi_filesetfile (
            fileset_id, path_name, size_bytes, md5, sha1, sha256, mimetype, extra
        ) VALUES (
            %(fileset_id)s, %(path_name)s, %(size_bytes)s, %(md5)s,
            %(sha1)s, %(sha256)s, %(mimetype)s, %(extra)s
        )
    """,
    "fileseturl": """
        INSERT INTO fcapi_fileseturl (fileset_id, rel, url)
        VALUES (%(fileset_id)s, %(rel)s, %(url)s)
    """,
    "webcaptureurl": """
        INSERT INTO fcapi_webcaptureurl (webcapture_id, rel, url)
        VALUES (%(webcapture_id)s, %(rel)s, %(url)s)
    """,
    "webcapturecdx": """
        INSERT INTO fcapi_webcapturecdx (
            webcapture_id, surt, url, captured, mimetype, status_code,
            sha1, sha256, size_bytes
        ) VALUES (
            %(webcapture_id)s, %(surt)s, %(url)s, %(captured)s, %(mimetype)s,
            %(status_code)s, %(sha1)s, %(sha256)s, %(size_bytes)s
        )
    """,
}


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

def _json_params(row: dict) -> dict:
    """Wrap jsonb values for psycopg and inject source."""
    out = dict(row)
    for key in ("extra", "refs"):
        if key in out:
            out[key] = Json(out[key]) if out[key] is not None else None
    out.setdefault("source", SOURCE)
    return out


def _insert_entity(new_cur, entity_type: str, rows: list[dict], stats: Stats) -> set[str]:
    """Insert main entity rows, return set of IDs that were actually inserted."""
    sql = INSERT_ENTITY_SQL[entity_type]
    inserted = set()
    total = len(rows)
    for i, row in enumerate(rows, 1):
        params = _json_params(row)
        new_cur.execute("SAVEPOINT entity_ins")
        try:
            new_cur.execute(sql, params)
            if new_cur.rowcount == 1:
                inserted.add(str(row["id"]))
                stats.inserted += 1
            else:
                stats.skipped += 1
            new_cur.execute("RELEASE SAVEPOINT entity_ins")
        except psycopg.errors.UniqueViolation:
            new_cur.execute("ROLLBACK TO SAVEPOINT entity_ins")
            logger.warning("%s %s: skipped (unique violation)", entity_type, row["id"])
            stats.skipped += 1
        except psycopg.errors.ForeignKeyViolation:
            new_cur.execute("ROLLBACK TO SAVEPOINT entity_ins")
            logger.warning("%s %s: skipped (FK violation)", entity_type, row["id"])
            stats.skipped += 1
        if i % 10 == 0:
            logger.info("%s: %d/%d processed (%d inserted, %d skipped)",
                        entity_type, i, total, stats.inserted, stats.skipped)
    return inserted


def _insert_children(new_cur, child_type: str, rows: list[dict], parent_ids: set[str],
                     parent_key: str, stats: Stats) -> None:
    """Insert child rows whose parent was newly inserted."""
    sql = INSERT_CHILD_SQL[child_type]
    total = len(rows)
    for i, row in enumerate(rows, 1):
        if str(row[parent_key]) not in parent_ids:
            continue
        params = _json_params(row)
        new_cur.execute("SAVEPOINT child_ins")
        try:
            new_cur.execute(sql, params)
            stats.children += 1
            new_cur.execute("RELEASE SAVEPOINT child_ins")
        except psycopg.errors.ForeignKeyViolation:
            new_cur.execute("ROLLBACK TO SAVEPOINT child_ins")
            logger.warning("%s: skipped child row (FK violation)", child_type)
        except psycopg.errors.UniqueViolation:
            new_cur.execute("ROLLBACK TO SAVEPOINT child_ins")
        if i % 10 == 0:
            logger.info("%s: %d/%d processed (%d inserted, %d skipped)",
                        child_type, i, total, stats.inserted, stats.skipped)


def _batched(lst: list, n: int):
    """Yield successive n-sized chunks from lst."""
    for i in range(0, len(lst), n):
        yield lst[i:i + n]


def _read_rows(old_cur, sql: str, idents: list[str]) -> list[dict]:
    """Read rows from old DB for a batch of idents."""
    old_cur.execute(sql, {"idents": idents})
    return old_cur.fetchall()


def _read_legacy_extids(old_cur, release_idents: list[str]) -> list[dict]:
    """Read legacy extid columns (doi, pmid, etc.) from release_rev for normalization."""
    rows = []
    for col in LEGACY_EXTID_COLS:
        old_cur.execute(f"""
            SELECT
                ri.id AS release_id,
                '{col}' AS id_type,
                rr.{col} AS id_value
            FROM release_ident ri
            JOIN release_rev rr ON ri.rev_id = rr.id
            WHERE ri.id = ANY(%(idents)s)
              AND ri.is_live = true
              AND ri.redirect_id IS NULL
              AND rr.{col} <> ''
        """, {"idents": release_idents})
        rows.extend(old_cur.fetchall())
    return rows


# ---------------------------------------------------------------------------
# Per-entity-type migration (batched with commits)
# ---------------------------------------------------------------------------

# Maps entity types to their child tables and the parent key in those children.
ENTITY_CHILDREN = {
    "release": {
        "parent_key": "release_id",
        "child_tables": ["releaseextid", "releaseabstract", "releasecontrib", "releaseref"],
        "has_legacy_extids": True,
    },
    "file": {
        "parent_key": "file_id",
        "child_tables": ["fileurl", "releasefile"],
    },
    "fileset": {
        "parent_key": "fileset_id",
        "child_tables": ["filesetfile", "fileseturl"],
    },
    "webcapture": {
        "parent_key": "webcapture_id",
        "child_tables": ["webcaptureurl", "webcapturecdx"],
    },
}


def _migrate_batch(old_conn, new_conn, entity_type: str, batch_idents: list[str],
                   entity_stats: Stats, child_stats: dict[str, Stats],
                   commit: bool = True) -> set[str]:
    """Migrate one batch of idents + their children in a single transaction.

    If commit is False the transaction is left open for the caller to
    rollback (used by --dry-run).
    """
    with old_conn.cursor() as old_cur, new_conn.cursor() as new_cur:
        # Insert main entity rows
        rows = _read_rows(old_cur, READ_ENTITY_SQL[entity_type], batch_idents)
        inserted_ids = _insert_entity(new_cur, entity_type, rows, entity_stats)

        # Insert children for newly-inserted entities
        children_cfg = ENTITY_CHILDREN.get(entity_type)
        if children_cfg and inserted_ids:
            parent_key = children_cfg["parent_key"]
            for child_type in children_cfg["child_tables"]:
                logger.info("reading %s rows", child_type)
                child_rows = _read_rows(old_cur, READ_CHILD_SQL[child_type], batch_idents)
                logger.info("inserting %s", child_type)
                _insert_children(new_cur, child_type, child_rows,
                                 inserted_ids, parent_key, child_stats[child_type])

            if children_cfg.get("has_legacy_extids"):
                logger.info("reading legacy extids")
                legacy_rows = _read_legacy_extids(old_cur, batch_idents)
                logger.info("inserting legacy releaseextid")
                _insert_children(new_cur, "releaseextid", legacy_rows,
                                 inserted_ids, parent_key, child_stats["legacy_extids"])
            logger.info("done with children")

    if commit:
        new_conn.commit()
    return inserted_ids


def _init_child_stats(entity_type: str) -> dict[str, Stats]:
    children_cfg = ENTITY_CHILDREN.get(entity_type, {})
    child_stats: dict[str, Stats] = {}
    for ct in children_cfg.get("child_tables", []):
        child_stats[ct] = Stats(entity=ct)
    if children_cfg.get("has_legacy_extids"):
        child_stats["legacy_extids"] = Stats(entity="legacy_extids")
    return child_stats


def migrate_entity_type(entity_type: str, old_conn, new_conn,
                        from_date: str, to_date: str, batch_size: int) -> list[Stats]:
    """Migrate all idents for an entity type in batches, committing each batch."""
    entity_stats = Stats(entity=entity_type)

    with old_conn.cursor() as old_cur:
        idents = discover_idents(old_cur, entity_type, from_date, to_date)
    entity_stats.found = len(idents)
    logger.info("%s: found %d changed idents", entity_type, entity_stats.found)

    child_stats = _init_child_stats(entity_type)

    if not idents:
        return [entity_stats] + list(child_stats.values())

    for batch_num, batch in enumerate(_batched(idents, batch_size), 1):
        logger.info("%s: batch %d (%d idents)", entity_type, batch_num, len(batch))
        _migrate_batch(old_conn, new_conn, entity_type, batch,
                       entity_stats, child_stats, commit=True)

    logger.info("%s: inserted %d, skipped %d", entity_type,
                entity_stats.inserted, entity_stats.skipped)
    for cs in child_stats.values():
        if cs.children:
            logger.info("  %s: %d child rows", cs.entity, cs.children)

    return [entity_stats] + list(child_stats.values())


def dry_run_entity_type(entity_type: str, old_conn, new_conn,
                        from_date: str, to_date: str, batch_size: int) -> list[Stats]:
    """Run a single batch for one entity type, then rollback."""
    entity_stats = Stats(entity=entity_type)

    with old_conn.cursor() as old_cur:
        idents = discover_idents(old_cur, entity_type, from_date, to_date)
    entity_stats.found = len(idents)
    logger.info("DRY RUN %s: found %d changed idents, processing first batch of %d",
                entity_type, entity_stats.found, min(len(idents), batch_size))

    child_stats = _init_child_stats(entity_type)

    if not idents:
        return [entity_stats] + list(child_stats.values())

    batch = idents[:batch_size]
    _migrate_batch(old_conn, new_conn, entity_type, batch,
                   entity_stats, child_stats, commit=False)

    new_conn.rollback()

    logger.info("DRY RUN %s: would insert %d, skipped %d (rolled back)",
                entity_type, entity_stats.inserted, entity_stats.skipped)
    for cs in child_stats.values():
        if cs.children:
            logger.info("  %s: %d child rows (rolled back)", cs.entity, cs.children)

    return [entity_stats] + list(child_stats.values())


# ---------------------------------------------------------------------------
# Orchestration
# ---------------------------------------------------------------------------

MIGRATION_ORDER = [
    # Phase 1: independent entities
    "container", "creator", "work",
    # Phase 2: release + children (depends on work, container)
    "release",
    # Phase 3: file + children
    "file",
    # Phase 4: fileset + children (depends on release)
    "fileset",
    # Phase 5: webcapture + children (depends on release)
    "webcapture",
]


def migrate(args) -> None:
    old_conn = psycopg.connect(args.old_db_url, row_factory=dict_row)
    new_conn = psycopg.connect(args.new_db_url, autocommit=False)

    all_stats: list[Stats] = []

    try:
        logger.info("date range: %s to %s", args.from_date, args.to_date)
        if args.dry_run:
            logger.info("DRY RUN: will process one batch per entity type and rollback")
        else:
            logger.info("batch size: %d idents per commit", int(args.batch_size))

        for entity_type in MIGRATION_ORDER:
            if args.entity_type and args.entity_type != entity_type:
                continue
            if args.dry_run:
                stats = dry_run_entity_type(
                    entity_type, old_conn, new_conn,
                    args.from_date, args.to_date, int(args.batch_size)
                )
            else:
                stats = migrate_entity_type(
                    entity_type, old_conn, new_conn,
                    args.from_date, args.to_date, int(args.batch_size),
                )
            all_stats.extend(stats)

    except Exception:
        new_conn.rollback()
        logger.exception("migration failed, rolled back current batch")
        raise
    finally:
        old_conn.close()
        new_conn.close()

    # Summary
    logger.info("=== SUMMARY ===")
    for s in all_stats:
        if s.found or s.children:
            logger.info(
                "%-20s found=%d  inserted=%d  skipped=%d  children=%d",
                s.entity, s.found, s.inserted, s.skipped, s.children,
            )


def main() -> None:
    parser = argparse.ArgumentParser(
        description="Incremental migration from fatcat_prod to fatcat2")
    parser.add_argument(
        "--from-date", required=True,
        help="Start of date range (inclusive), ISO format e.g. 2025-05-01")
    parser.add_argument(
        "--to-date", required=True,
        help="End of date range (exclusive), ISO format e.g. 2026-04-01")
    parser.add_argument(
        "--old-db-url",
        default=os.environ.get("FCPATCH_OLD_DB_URL", "postgresql:///fatcat_prod"),
        help="Connection URL for old fatcat database (default: $FCPATCH_OLD_DB_URL or postgresql:///fatcat_prod)")
    parser.add_argument(
        "--new-db-url",
        default=os.environ.get("FCPATCH_NEW_DB_URL", "postgresql:///fatcat2"),
        help="Connection URL for new fatcat2 database (default: $FCPATCH_NEW_DB_URL or postgresql:///fatcat2)")
    parser.add_argument(
        "--dry-run", action="store_true",
        help="Run the full migration but rollback at the end")
    parser.add_argument(
        "--entity-type", choices=ENTITY_TYPES,
        help="Migrate only this entity type (for debugging/reruns)")
    parser.add_argument(
        "--batch-size", default=DEFAULT_BATCH_SIZE,
        help="how many parent entities to insert in a single transaction")

    args = parser.parse_args()
    migrate(args)


if __name__ == "__main__":
    main()
