import logging
import os
from collections import namedtuple
from functools import wraps
from time import time
from typing import List, Tuple

import diskcache as dc
import psycopg
from psycopg.rows import dict_row

CWD = os.getcwd()
SOURCE = "legacy_import"
OLD_DB_URL="postgresql:///fatcat_prod?host=/home/vilmibm/src/fatcat-scholar/devdb/pgdata/sockets"
NEW_DB_URL="postgresql:///fatcat2?host=/home/vilmibm/src/fatcat-scholar/devdb/pgdata/sockets"

PKTABLES = ["release", "work", "file", "creator"]
FKTABLES = [
        "release",
        "releaseextid",
        "releaseabstract",
        "releasecontrib",
        "releaseref",
        "releasefile",
        "fileset",
        "webcapture",
        "fileurl",
        ]

FK = namedtuple("FK", ["table", "name", "column", "target_table", "target_column"])
LEGACY_COLS = ["doi", "pmid", "pmcid", "wikidata_qid", "core_id"]

logger = logging.getLogger(__name__)
logger.setLevel(logging.INFO)
formatter = logging.Formatter('%(asctime)s [%(levelname)s] - %(message)s',
                              datefmt='%m-%d %H:%M:%S')

stdout_handler = logging.StreamHandler()
stdout_handler.setFormatter(formatter)
logger.addHandler(stdout_handler)

def timing(f):
    """ https://stackoverflow.com/a/27737385 """
    @wraps(f)
    def wrap(*args, **kw):
        ts = time()
        result = f(*args, **kw)
        te = time()
        logger.info("func:%r took: %2.4f sec" % (f.__name__, te-ts))
        return result
    return wrap

DUMP_SQL = {
        "container": f"""
            COPY (
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
                  WHERE
                    coalesce(trim(cr.container_type), '') != 'test'
                ) TO STDOUT WITH (FORMAT CSV, DELIMITER E'\t', HEADER);
        """,
        "creator": f"""
             COPY (
               SELECT
                 ci.id,
                 ci.rev_id as legacy_rev,
                 cr.display_name,
                 cr.given_name,
                 cr.surname,
                 cr.orcid,
                 '{SOURCE}' AS source,
                 to_json(cr.extra_json) AS extra
               FROM creator_ident ci
               JOIN creator_rev cr ON ci.rev_id = cr.id
               WHERE
                 ci.is_live = true
                 AND
                 ci.redirect_id IS NULL
               ) TO STDOUT WITH (FORMAT CSV, DELIMITER E'\t', HEADER);
        """,
        "work": f"""
            COPY (
              SELECT
                wi.id,
                wi.rev_id AS legacy_rev,
                '{SOURCE}' AS source,
                to_json(wr.extra_json) AS extra
              FROM work_ident wi
              JOIN work_rev wr ON wi.rev_id = wr.id
              WHERE
                wi.is_live = true
              AND
                wi.redirect_id IS NULL
              ) TO STDOUT WITH (FORMAT CSV, DELIMITER E'\t', HEADER);
        """,
        "release": f"""
            COPY (
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
                rr.doi as legacy_doi,
                rr.pmid as legacy_pmid,
                rr.pmcid as legacy_pmcid,
                rr.wikidata_qid as legacy_wikidata_qid,
                rr.core_id as legacy_core_id,

                (SELECT to_json(refs_json)
                 FROM refs_blob rb
                 WHERE rb.sha1 = rr.refs_blob_sha1) AS refs
              FROM
                release_ident ri
              JOIN release_rev rr ON ri.rev_id = rr.id
              JOIN work_ident wi ON rr.work_ident_id = wi.id
              WHERE ri.is_live = true
              AND ri.redirect_id IS NULL
              AND wi.is_live = true
              ) TO STDOUT WITH (FORMAT CSV, DELIMITER E'\t', HEADER);
        """,
        "releaseextid": """
            COPY (
              SELECT
                ri.id as release_id,
                ei.extid_type AS id_type,
                ei.value AS id_value
              FROM release_ident ri
              JOIN release_rev_extid ei ON ri.rev_id = ei.release_rev
              WHERE ri.is_live = true
              AND ri.redirect_id IS NULL
            ) TO STDOUT WITH (FORMAT CSV, DELIMITER E'\t', HEADER);
        """,
        "releaseabstract": """
            COPY (
              SELECT
                ri.id as release_id,
                ra.abstract_sha1 AS sha1,
                ra.mimetype,
                ra.lang AS language,
                (SELECT content
                 FROM abstracts a
                 WHERE ra.abstract_sha1 = a.sha1)
              FROM release_ident ri
              JOIN release_rev_abstract ra ON ri.rev_id = ra.release_rev
              WHERE ri.is_live = true
              AND ri.redirect_id IS NULL
              ) TO STDOUT WITH (FORMAT CSV, DELIMITER E'\t', HEADER);
        """,
        "releasecontrib": """
            COPY (
              SELECT
                ri.id as release_id,
                rc.raw_name,
                rc.given_name,
                rc.surname,
                rc.creator_ident_id AS creator_id,
                rc.role,
                rc.raw_affiliation,
                rc.index_val AS position,
                to_json(rc.extra_json) AS extra
              FROM release_ident ri
              JOIN release_contrib rc ON ri.rev_id = rc.release_rev
              WHERE ri.is_live = true
              AND ri.redirect_id IS NULL
              ) TO STDOUT WITH (FORMAT CSV, DELIMITER E'\t', HEADER);
        """,
        "releaseref": """
            COPY (
              SELECT
                rr.index_val AS position,
                ri.id as release_id,
                rr.target_release_ident_id AS target_release_id
              FROM release_ident ri
              JOIN release_ref rr ON ri.rev_id = rr.release_rev
              WHERE ri.is_live = true
              AND ri.redirect_id IS NULL
              ) TO STDOUT WITH (FORMAT CSV, DELIMITER E'\t', HEADER);
        """,
        "file": f"""
            COPY (
              SELECT
                fi.rev_id AS legacy_rev,
                fi.id,
                '{SOURCE}' as source,
                fr.size_bytes,
                fr.sha1,
                fr.sha256,
                fr.mimetype,
                fr.md5,
                to_json(fr.extra_json) AS extra
              FROM
                file_ident fi
              JOIN file_rev fr ON fi.rev_id = fr.id
              WHERE fi.is_live = true
              AND fi.redirect_id IS NULL
              ) TO STDOUT WITH (FORMAT CSV, DELIMITER E'\t', HEADER);
        """,
        "releasefile": """
            COPY (
              SELECT
                frr.target_release_ident_id AS release_id,
                fi.id AS file_id
              FROM file_ident fi
              JOIN file_rev_release frr ON fi.rev_id = frr.file_rev
              WHERE fi.is_live = true
              AND fi.redirect_id IS NULL
            ) TO STDOUT WITH (FORMAT CSV, DELIMITER E'\t', HEADER);
        """,
        "fileurl": """
            COPY (
              SELECT
                fu.rel,
                fu.url,
                fi.id AS file_id
              FROM
                file_ident fi
              JOIN file_rev_url fu ON fi.rev_id = fu.file_rev
              WHERE fi.is_live = true
              AND fi.redirect_id IS NULL
              ) TO STDOUT WITH (FORMAT CSV, DELIMITER E'\t', HEADER);
        """,
        "fileset": f"""
            COPY (
              SELECT
                fi.rev_id AS legacy_rev,
                fi.id,
                to_json(fr.extra_json) AS extra,
                '{SOURCE}' AS source,
                (SELECT
                  target_release_ident_id
                 FROM fileset_rev_release frr
                 WHERE frr.fileset_rev = fi.rev_id
                ) AS release_id
              FROM
                fileset_ident fi
              JOIN fileset_rev fr ON fi.rev_id = fr.id
              WHERE fi.is_live = true
              AND fi.redirect_id IS NULL
              ) TO STDOUT WITH (FORMAT CSV, DELIMITER E'\t', HEADER);
        """,
        "fileseturl": """
            COPY (
              SELECT
                fi.id as fileset_id,
                fu.rel,
                fu.url
              FROM
                fileset_ident fi
              JOIN fileset_rev_url fu ON fi.rev_id = fu.fileset_rev
              WHERE fi.is_live = true
              AND fi.redirect_id IS NULL
              ) TO STDOUT WITH (FORMAT CSV, DELIMITER E'\t', HEADER);
        """,
        "filesetfile": """
            COPY (
              SELECT
                fi.id as fileset_id,
                ff.path_name,
                ff.size_bytes,
                ff.md5,
                ff.sha1,
                ff.sha256,
                ff.mimetype,
                to_json(ff.extra_json) AS extra
              FROM
                fileset_ident fi
              JOIN fileset_rev_file ff ON fi.rev_id = ff.fileset_rev
              WHERE fi.is_live = true
              AND fi.redirect_id IS NULL
              ) TO STDOUT WITH (FORMAT CSV, DELIMITER E'\t', HEADER);
        """,
        "webcapture": f"""
            COPY (
              SELECT
                wi.rev_id AS legacy_rev,
                wi.id,
                '{SOURCE}' AS source,
                to_json(wr.extra_json) AS extra,
                wr.original_url,
                wr.timestamp AS captured,
                (SELECT target_release_ident_id
                 FROM webcapture_rev_release
                 WHERE webcapture_rev = wr.id) AS release_id
              FROM webcapture_ident wi
              JOIN webcapture_rev wr ON wi.rev_id = wr.id
              WHERE wi.is_live = true
              AND wi.redirect_id IS NULL
              ) TO STDOUT WITH (FORMAT CSV, DELIMITER E'\t', HEADER);
        """,
        "webcaptureurl": """
            COPY (
              SELECT
                wi.id AS webcapture_id,
                wu.rel,
                wu.url
              FROM webcapture_ident wi
              JOIN webcapture_rev_url wu ON wu.webcapture_rev = wi.rev_id
              WHERE wi.is_live = true
              AND wi.redirect_id IS NULL
              ) TO STDOUT WITH (FORMAT CSV, DELIMITER E'\t', HEADER);
        """,
        "webcapturecdx": """
            COPY (
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
              WHERE wi.is_live = true
              AND wi.redirect_id IS NULL
              ) TO STDOUT WITH (FORMAT CSV, DELIMITER E'\t', HEADER);
        """,
        }

def outfile(table: str) -> str:
    return os.path.join(CWD, f"{table}.tsv")

@timing
def dump_table(table: str) -> int:
    logger.info(f"dumping {table}")
    count = 0
    sql = DUMP_SQL[table]
    with psycopg.connect(conninfo=OLD_DB_URL) as conn:
        with conn.cursor() as cur, open(outfile(table), 'wb') as f:
           with cur.copy(sql) as copy:
               for row in copy:
                   f.write(row)
                   count += 1
                   if count % 100000 == 0:
                       logger.info(f"dumped {count} from {table}")
    logger.info(f"dumped {count} {table}")
    return count

@timing
def dump_legacy_release_extid() -> int:
    count = 0
    for col in LEGACY_COLS:
        legacy_extid_sql = f"""
            COPY (
              SELECT
                ri.id as release_id,
                '{col}' as id_type,
                rr.{col} as id_value
              FROM release_ident ri
              JOIN release_rev rr ON ri.rev_id = rr.id
              WHERE
                ri.is_live = true
                AND
                ri.redirect_id is NULL
                AND
                rr.{col} <> ''
            ) TO STDOUT WITH (FORMAT CSV, DELIMITER E'\t', HEADER);
        """
        logger.info("dumping legacy extids")
        with psycopg.connect(conninfo=OLD_DB_URL) as conn:
            with conn.cursor() as cur, open(f"legacy_{col}_extid.tsv", 'wb') as f:
               with cur.copy(legacy_extid_sql) as copy:
                   for row in copy:
                       f.write(row)
                       count += 1
                       if count % 100000 == 0:
                           logger.info(f"dumped {count} from legacy extids")
        logger.info(f"dumped {count} legacy extids")
    logger.info(f"dumped {count} legacy extids")
    return count

def drop_indexes(table, names: List[str]):
    """drop a list of named indexes from the new fatcat db"""
    logger.info(f"{table}: dropping indexes")

    with psycopg.connect(conninfo=NEW_DB_URL) as conn, conn.cursor() as cur:
       for name in names:
           cur.execute(f"DROP INDEX {name}")

    logger.info(f"{table}: dropped indexes")

def create_indexes(table, indexes: List[Tuple[str, str]]):
    """given tuples of (index_name, table, colum), create indexes in the new db"""
    logger.info(f"{table}: restoring indexes")

    with psycopg.connect(conninfo=NEW_DB_URL) as conn, conn.cursor() as cur:
       for index in indexes:
           cur.execute(f"CREATE INDEX {index[0]} ON fcapi_{table} ({index[1]});")

    logger.info(f"{table}: restored indexes")

RESTORE_SQL = {
        "container": """
            COPY fcapi_container (
              id, legacy_rev, name, extra, container_type, publisher,
              issnl, issne, issnp, wikidata_qid, source)
            FROM STDIN WITH (FORMAT CSV, DELIMITER E'\t', HEADER, NULL '', FORCE_NULL (extra));
        """,
        "creator": """
            COPY fcapi_creator (
              id, legacy_rev, display_name, given_name, surname, orcid,
              source, extra)
            FROM STDIN WITH (FORMAT CSV, DELIMITER E'\t', HEADER, NULL '', FORCE_NULL (extra));
        """,
        "work": """
            COPY fcapi_work (id, legacy_rev, source, extra)
            FROM STDIN WITH (FORMAT CSV, DELIMITER E'\t', HEADER, NULL '', FORCE_NULL (extra));
        """,
        "release": """
            COPY fcapi_release (
              id, legacy_rev, extra, source, title, original_title, subtitle, release_type,
              release_stage, release_date, release_year, volume, issue, pages, number, version,
              publisher, language, license_slug, withdrawn_status, work_id, container_id,
              legacy_doi, legacy_pmid, legacy_pmcid, legacy_wikidata_qid, legacy_core_id, refs)
            FROM STDIN WITH (FORMAT CSV, DELIMITER E'\t', HEADER, NULL '', FORCE_NULL (extra));
        """,
        "releaseextid": """
            COPY fcapi_releaseextid (release_id, id_type, id_value)
            FROM STDIN WITH (FORMAT CSV, DELIMITER E'\t', HEADER, NULL '');
        """,
        "releaseabstract": """
            COPY fcapi_releaseabstract (release_id, sha1, mimetype, language, content)
            FROM STDIN WITH (FORMAT CSV, DELIMITER E'\t', HEADER, NULL '');
        """,
        "releasecontrib": """
            COPY fcapi_releasecontrib (release_id, raw_name, given_name, surname,
              creator_id, role, raw_affiliation, position, extra)
            FROM STDIN WITH (FORMAT CSV, DELIMITER E'\t', HEADER, NULL '', FORCE_NULL (extra));
        """,
        "releaseref": """
            COPY fcapi_releaseref (position, release_id, target_release_id)
            FROM STDIN WITH (FORMAT CSV, DELIMITER E'\t', HEADER, NULL '');
        """,
        "file": """
            COPY fcapi_file (legacy_rev, id, source, size_bytes, sha1, sha256,
            mimetype, md5, extra)
            FROM STDIN WITH (FORMAT CSV, DELIMITER E'\t', HEADER, NULL '', FORCE_NULL (extra));
        """,
        "releasefile": """
            COPY fcapi_releasefile (release_id, file_id)
            FROM STDIN WITH (FORMAT CSV, DELIMITER E'\t', HEADER, NULL '');
        """,
        "fileurl": """
            COPY fcapi_fileurl (rel, url, file_id)
            FROM STDIN WITH (FORMAT CSV, DELIMITER E'\t', HEADER, NULL '');
        """,
        "fileset": """
            COPY fcapi_fileset (legacy_rev, id, extra, source, release_id)
            FROM STDIN WITH (FORMAT CSV, DELIMITER E'\t', HEADER, NULL '', FORCE_NULL (extra));
        """,
        "filesetfile": """
            COPY fcapi_filesetfile (fileset_id, path_name, size_bytes, md5, sha1,
                sha256, mimetype, extra)
            FROM STDIN WITH (FORMAT CSV, DELIMITER E'\t', HEADER, NULL '', FORCE_NULL (extra));
        """,
        "webcapture": """
            COPY fcapi_webcapture (legacy_rev, id, source, extra, original_url, captured,
                release_id)
            FROM STDIN WITH (FORMAT CSV, DELIMITER E'\t', HEADER, NULL '', FORCE_NULL (extra));
        """,
        "webcaptureurl": """
            COPY fcapi_webcaptureurl (webcapture_id, rel, url)
            FROM STDIN WITH (FORMAT CSV, DELIMITER E'\t', HEADER, NULL '');
        """,
        "webcapturecdx": """
            COPY fcapi_webcapturecdx (webcapture_id, surt, url, captured, mimetype, status_code,
                sha1, sha256, size_bytes)
            FROM STDIN WITH (FORMAT CSV, DELIMITER E'\t', HEADER, NULL '');
        """,
        }

def simple_restore(table: str) -> int:
    logger.info(f"{table}: starting restore")
    count = 0
    sql = RESTORE_SQL[table]
    with psycopg.connect(conninfo=NEW_DB_URL) as conn:
        with conn.cursor() as cur, open(outfile(table), "r") as f:
            with cur.copy(sql) as copy:
                for line in f:
                    copy.write(line)
                    count += 1
                    if count % 100000 == 0:
                        logger.info(f"{table}: restored {count} rows")

    logger.info(f"{table}: finished restoring")

    return count

def restore_creator() -> int:
    table = "creator"

    indexes = [
            ("fcapi_creator_legacy_rev_idx", "legacy_rev"),
            ("fcapi_creator_source_idx", "source"),
            ("fcapi_creator_updated_idx", "updated"),
            ]

    drop_indexes(table, [idx[0] for idx in indexes])
    count = simple_restore(table)
    create_indexes(table, indexes)

    return count

def restore_work() -> int:
    table = "work"

    indexes = [
            ("fcapi_work_legacy_rev_idx", "legacy_rev"),
            ("fcapi_work_source_idx", "source"),
            ("fcapi_work_updated_idx", "updated"),
    ]

    drop_indexes(table, [idx[0] for idx in indexes])
    count = simple_restore(table)
    create_indexes(table, indexes)

    return count

def restore_release() -> int:
    table = "release"

    indexes = [
            ("fcapi_release_container_id_idx", "container_id"),
            ("fcapi_release_work_id_idx", "work_id"),
            ("fcapi_release_legacy_rev_idx", "legacy_rev"),
            ("fcapi_release_source_idx", "source"),
            ("fcapi_release_updated_idx", "updated"),
    ]

    drop_indexes(table, [idx[0] for idx in indexes])
    count = simple_restore(table)
    create_indexes(table, indexes)

    return count

def restore_releaseabstract() -> int:
    table = "releaseabstract"
    indexes = [
            ("fcapi_releaseabstract_release_idx", "release_id"),
            ("fcapi_releaseabstract_sha1_idx", "sha1"),
    ]
    drop_indexes(table, [idx[0] for idx in indexes])
    count = simple_restore(table)
    create_indexes(table, indexes)
    return count

def restore_releasecontrib() -> int:
    table = "releasecontrib"
    indexes = [
            ("fcapi_releasecontrib_release_idx", "release_id"),
            ("fcapi_releasecontrib_creator_idx", "creator_id"),
    ]
    drop_indexes(table, [idx[0] for idx in indexes])
    count = simple_restore(table)
    create_indexes(table, indexes)
    return count

def restore_releaseref() -> int:
    table = "releaseref"
    indexes = [
            ("fcapi_releaseref_release_idx", "release_id"),
            ("fcapi_releaseref_target_release_idx", "target_release_id"),
    ]
    drop_indexes(table, [idx[0] for idx in indexes])
    count = simple_restore(table)
    create_indexes(table, indexes)
    return count

def restore_releaseextid() -> int:
    table = "releaseextid"
    indexes = [
            ("fcapi_releaseextid_release_idx", "release_id"),
    ]
    drop_indexes(table, [idx[0] for idx in indexes])

    # NB have to manually drop bc I didn't account for compound indexes in {create,drop}_indexes
    logger.info("dropping extid_lookup_idx")
    with psycopg.connect(conninfo=NEW_DB_URL) as conn, conn.cursor() as cur:
        cur.execute("DROP INDEX extid_lookup_idx")
    logger.info("dropped extid_lookup_idx")

    count = simple_restore(table)

    logger.info("restoring legacy extids")
    with psycopg.connect(conninfo=NEW_DB_URL) as conn, conn.cursor() as cur:
        for col in LEGACY_COLS:
            with open(f"legacy_{col}_extid.tsv", "r") as f:
                sql = """
                    COPY fcapi_releaseextid (release_id, id_type, id_value)
                    FROM STDIN WITH (FORMAT CSV, DELIMITER E'\t', HEADER, NULL '');
                """
                with cur.copy(sql) as copy:
                    for line in f:
                        copy.write(line)
                        count += 1
                        if count % 100000 == 0:
                            logger.info(f"{table}/{col}: restored {count} rows")

    logger.info("restored legacy extids")
    create_indexes(table, indexes)

    # NB have to manually create bc I didn't account for compound indexes in {create,drop}_indexes
    logger.info("creating extid_lookup_idx")
    with psycopg.connect(conninfo=NEW_DB_URL) as conn, conn.cursor() as cur:
        cur.execute(f"CREATE INDEX extid_lookup_idx ON fcapi_{table} (id_type, id_value);")
    logger.info("created extid_lookup_idx")
    return count

def restore_file():
    table = "file"
    indexes = [
            # TODO will i be ruined by pkey,fkey indexes?
            ("fcapi_file_legacy_rev_idx", "legacy_rev"),
            ("fcapi_file_md5_idx", "md5"),
            ("fcapi_file_sha1_idx", "sha1"),
            ("fcapi_file_sha256_idx", "sha256"),
            ("fcapi_file_source_idx", "source"),
            ("fcapi_file_updated_idx", "updated"),
    ]

    drop_indexes(table, [idx[0] for idx in indexes])
    count = simple_restore(table)
    create_indexes(table, indexes)

    return count

def restore_fileurl():
    table = "fileurl"
    indexes = [
            ("fcapi_fileurl_file_idx", "file_id"),
    ]

    drop_indexes(table, [idx[0] for idx in indexes])
    count = simple_restore(table)
    create_indexes(table, indexes)

    return count

def constraint_name_to_col(name: str) -> str:
    return "_".join(name.split("_")[-2:])

def constraint_name_to_table(name: str) -> str:
    return name.split("_")[-2]

def drop_fk_constraints() -> List[FK]:
    logger.info("dropping fk constraints")
    dropped: List[FK] = []
    with psycopg.connect(conninfo=NEW_DB_URL) as conn, conn.cursor(row_factory=dict_row) as cur:
        for table in FKTABLES:
            fk_name_sql = f"""
                SELECT conname AS name FROM pg_constraint
                WHERE conrelid = 'fcapi_{table}'::regclass AND pg_constraint.contype = 'f';
            """
            cur.execute(fk_name_sql)
            constraints = cur.fetchall()
            for constraint in constraints:
                conname = constraint["name"]
                drop_sql = f"ALTER TABLE fcapi_{table} DROP CONSTRAINT {conname};"
                logger.info(f"{table}: dropping {conname}")
                cur.execute(drop_sql)
                dropped.append(FK(table, constraint["name"], 
                                  constraint_name_to_col(conname),
                                  constraint_name_to_table(conname), "id"))
                logger.info(f"{table}: dropped {conname}")
    logger.info("dropped fk constraints")
    return dropped

def create_fk_constraints(constraints: List[FK]):
    logger.info("creating fk constraints")
    with psycopg.connect(conninfo=NEW_DB_URL) as conn, conn.cursor(row_factory=dict_row) as cur:
        for cons in constraints:
            create_fk_sql = f"""
                ALTER TABLE fcapi_{cons.table} ADD CONSTRAINT {cons.name}
                FOREIGN KEY ({cons.column})
                REFERENCES fcapi_{cons.target_table} ({cons.target_column})
                DEFERRABLE INITIALLY DEFERRED
            """
            cur.execute(create_fk_sql)
            logger.info(f"{cons.table}: created {cons.name}")
    logger.info("created fk constraints")
        
def drop_pk_constraints() -> List[str]:
    logger.info("dropping pk constraints")
    dropped: List[str] = []
    with psycopg.connect(conninfo=NEW_DB_URL) as conn, conn.cursor(row_factory=dict_row) as cur:
        for table in PKTABLES:
            drop_pk_sql = f"ALTER TABLE fcapi_{table} DROP CONSTRAINT fcapi_{table}_pkey;"
            logger.info(f"{table}: dropping fcapi_{table}_pkey")
            cur.execute(drop_pk_sql)
            logger.info(f"{table}: dropped fcapi_{table}_pkey")
            dropped.append(table)
    logger.info("dropped pk constraints")
    return dropped

def create_pk_constraints(tables: List[str]):
    logger.info("creating pk constraints")
    with psycopg.connect(conninfo=NEW_DB_URL) as conn, conn.cursor(row_factory=dict_row) as cur:
        for table in tables:
            create_pk_sql = f"""
                ALTER TABLE fcapi_{table} ADD CONSTRAINT fcapi_{table}_pkey PRIMARY KEY (id);
            """
            logger.info(f"{table}: creating fcapi_{table}_pkey")
            cur.execute(create_pk_sql)
            logger.info(f"{table}: created fcapi_{table}_pkey")
    logger.info("created pk constraints")

@timing
def dump_all(start_from: str = "container"):
    logger.info("starting dump")
    go = False
    for k in DUMP_SQL:
        if k == start_from:
            go = True
        if go:
            dump_table(k)
    dump_legacy_release_extid()
    logger.info("finished dump")

@timing
def restore_all():
    logger.info("starting restore")
    cache = dc.Cache("fcmigrate")

    if b"dropped_fk" in cache:
        dropped_fk = cache[b"dropped_fk"]
    else:
        dropped_fk = drop_fk_constraints()
        cache[b'dropped_fk'] = dropped_fk

    if b"dropped_pk" in cache:
        dropped_pk = cache[b"dropped_pk"]
    else:
        dropped_pk = drop_pk_constraints()
        cache[b'dropped_pk'] = dropped_pk

    #simple_restore("container")
    #restore_creator()
    #restore_work()
    #restore_release()
    restore_releaseextid()
    simple_restore("releaseabstract")
    simple_restore("releasecontrib")
    simple_restore("releaseref")
    restore_file()
    simple_restore("fileurl")
    simple_restore("fileset")
    simple_restore("filesetfile")
    simple_restore("webcapture")
    simple_restore("webcaptureurl")
    simple_restore("webcapturecdx")

    create_pk_constraints(dropped_pk)
    create_fk_constraints(dropped_fk)

    del cache[b'dropped_fk']
    del cache[b'dropped_pk']

    logger.info("finished restore")

@timing
def main():
    # TODO accept from_table argument from args
    #dump_all()
    restore_all()

if __name__ == '__main__':
    main()
