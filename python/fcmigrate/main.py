import os
import subprocess
from math import ceil
from typing import Tuple

from fastapi import FastAPI
from dbos import DBOS, Queue
import psycopg

OLD_DATABASE_URL="postgresql:///fatcat_prod?host=/home/vilmibm/src/fatcat-scholar/devdb/pgdata/sockets"
NEW_DATABASE_URL="postgresql:///fatcat2?host=/home/vilmibm/src/fatcat-scholar/devdb/pgdata/sockets"

SOURCE = "legacy_import"
OLD_DB = "fatcat_prod"
NEW_DB = "fatcat2"
CHUNKED_UPDATE_SIZE = 50000000

CWD = os.getcwd()

RELEASES_OUT = os.path.join(CWD, "releases.tsv")
RELEASES_EXTID_OUT = os.path.join(CWD, "releaseextids.tsv")
RELEASES_ABSTRACT_OUT = os.path.join(CWD, "releaseabstracts.tsv")
RELEASES_CONTRIB_OUT = os.path.join(CWD, "releasecontribs.tsv")
RELEASES_REF_OUT = os.path.join(CWD, "releaserefs.tsv")
FILES_OUT = os.path.join(CWD, "files.tsv")
FILES_URL_OUT = os.path.join(CWD, "fileurls.tsv")
FILESETS_OUT = os.path.join(CWD, "fileurls.tsv")
FILESETS_URL_OUT = os.path.join(CWD, "fileseturls.tsv")
FILESETS_FILE_OUT = os.path.join(CWD, "filesetfiles.tsv")
WEBCAPTURES_OUT = os.path.join(CWD, "webcaptures.tsv")
WEBCAPTURES_URL_OUT = os.path.join(CWD, "webcaptureurls.tsv")
WEBCAPTURES_CDX_OUT = os.path.join(CWD, "webcapturecdx.tsv")

app = FastAPI()
DBOS(fastapi=app)

def bail(msg: str):
    DBOS.logger.error(msg)
    os._exit(1)

def copy_result_to_int(copy_output: str) -> int:
    out = -1
    try:
        int(copy_output.strip()[5:])
    except Exception as e:
        DBOS.logger.error(e)
        os._exit(1)
    return out

def psql(sql: str, db_name="postgres") -> Tuple[str, str]:
    """
    Execute SQL by passing it to psql on STDIN
    Returns (stdout, stderr) from the psql command
    """
    psql_cmd = ["/usr/bin/psql", "-d", db_name]

    env = os.environ.copy()
    process = subprocess.Popen(
        psql_cmd,
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        env=env,
        text=True  # Use text mode to handle strings directly
    )

    # Send the SQL query through stdin and get the output
    out, err = process.communicate(input=sql)

    if err != "":
        bail(f"unexpected psql stderr: {err}")

    return out, err

def outfile(table: str) -> str:
    return os.path.join(CWD, f"{table}.tsv")

DUMP_SQL = {
        "container": """
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
                    %s AS source
                  FROM container_ident ci
                  JOIN container_rev cr ON ci.rev_id = cr.id
                  WHERE
                    ci.is_live = true
                    AND
                    ci.redirect_id is NULL
                    AND
                    cr.container_type != 'test'
                ) TO STDOUT WITH (FORMAT CSV, DELIMITER E'\t', HEADER);
        """,
        "creator": """
             COPY (
               SELECT
                 ci.id,
                 ci.rev_id as legacy_rev,
                 cr.display_name,
                 cr.given_name,
                 cr.surname,
                 cr.orcid,
                 %s AS source,
                 to_json(cr.extra_json) AS extra
               FROM creator_ident ci
               JOIN creator_rev cr ON ci.rev_id = cr.id
               WHERE
                 ci.is_live = true
                 AND
                 ci.redirect_id IS NULL
               ) TO STDOUT WITH (FORMAT CSV, DELIMITER E'\t', HEADER);
        """,
        "work": """
            COPY (
              SELECT
                wi.id,
                wi.rev_id AS legacy_rev,
                %s AS source,
                to_json(wr.extra_json) AS extra
              FROM work_ident wi
              JOIN work_rev wr ON wi.rev_id = wr.id
              WHERE
                wi.is_live = true
              AND
                wi.redirect_id IS NULL
              ) TO STDOUT WITH (FORMAT CSV, DELIMITER E'\t', HEADER);
        """,
        "release": """
            COPY (
              SELECT
                ri.id,
                rr.id AS legacy_rev,
                to_json(rr.extra_json) AS extra,
                %s AS source,
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
              WHERE ri.is_live = true
              AND ri.redirect_id IS NULL
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
                rr.target_release_ident_id AS target_release_id,
              FROM release_ident ri
              JOIN release_ref rr ON ri.rev_id = rr.release_rev
              WHERE ri.is_live = true
              AND ri.redirect_id IS NULL
              ) TO STDOUT WITH (FORMAT CSV, DELIMITER E'\t', HEADER);
        """,
        "file": """
            COPY (
              SELECT
                fi.rev_id AS legacy_rev,
                fi.id,
                %s as source,
                fr.size_bytes,
                fr.sha1,
                fr.sha256,
                fr.mimetype,
                fr.md5,
                to_json(f.extra_json) AS extra,
                (SELECT target_release_ident_id
                 FROM file_rev_release frr
                 WHERE frr.file_rev = fr.id
                ) AS release_id
              FROM
                file_ident fi
              JOIN file_rev fr ON fi.rev_id = fr.id
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
              ) TO '{FILES_URL_OUT}' WITH (FORMAT CSV, DELIMITER E'\t', HEADER);
        """,
        "fileset": """
            COPY (
              SELECT
                fi.rev_id AS legacy_rev,
                fi.id,
                to_json(f.extra_json) AS extra,
                %s AS source,
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
              ) TO '{FILESETS_URL_OUT}' WITH (FORMAT CSV, DELIMITER E'\t', HEADER);
        """,
        "filesetfile": """
            COPY (
              SELECT
                fi.id as fileset_id,
                ff.path_name,
                '{SOURCE}' AS source,
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
        "webcapture": """
            COPY (
              SELECT
                wi.rev_id AS legacy_rev,
                wi.id,
                %s AS source,
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
                wc.url,
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

@DBOS.step()
def dump(table: str) -> int:
    DBOS.logger.info(f"dumping {table}")
    count = 0
    sql = DUMP_SQL[table]
    try:
        with psycopg.connect(conninfo=OLD_DATABASE_URL) as conn:
            with conn.cursor() as cur, open(outfile(table), 'wb') as f:
               with cur.copy(sql, (SOURCE,)) as copy:
                   for row in copy:
                       f.write(row)
                       count += 1
    except Exception as e:
        bail(str(e))
    DBOS.logger.info(f"dumped {count} {table}")
    return count

@app.get("/dump")
@DBOS.workflow()
def dump_workflow():
    queue = Queue("dump_queue", concurrency=8)
    handles = []
    for k in DUMP_SQL:
        handles.append(queue.enqueue(dump, k))

    return [handle.get_result() for handle in handles]

def drop_index(name: str):
    """drop a single named index from the new fatcat db"""
    out, err = psql(f"DROP INDEX {name}", db_name=NEW_DB)
    if not out.strip().startswith("DROP INDEX"):
        bail(f"unexpected drop index output: {out.strip()}")

def create_index(table: str, name: str, column: str):
    """add a single named index to a table in the new fatcat db"""
    out, err = psql(f"CREATE INDEX {name} ON {table} ({column})", db_name=NEW_DB)
    if not out.strip().startswith("CREATE INDEX"):
        bail(f"unexpected create index output: {out.strip()}")

RESTORE_SQL = {
        "container": """
            COPY fcapi_container (
              id, legacy_rev, name, extra, container_type, publisher,
              issnl, issne, issnp, wikidata_qid, source)
            FROM STDIN
            WITH (FORMAT CSV, DELIMITER E'\t', HEADER, NULL '',
                  FORCE_NULL (extra));
        """,
        "creator": """
            COPY fcapi_creator (
              id, legacy_rev, display_name, given_name, surname, orcid,
              source, extra)
            FROM STDIN
            WITH (FORMAT CSV, DELIMITER E'\t', HEADER, NULL '',
                  FORCE_NULL (extra));
        """,
        "work": """
            COPY fcapi_work (id, legacy_rev, source, extra)
            FROM STDIN
            WITH (FORMAT CSV, DELIMITER E'\t', HEADER, NULL '', FORCE_NULL (extra));
        """,
        "release": """
            COPY fcapi_release (
              id, legacy_rev, extra, source, title, original_title, subtitle, release_type,
              release_stage, release_date, release_year, volume, issue, pages, number, version,
              publisher, language, license_slug, withdrawn_status, work_id, container_id,
              legacy_doi, legacy_pmid, legacy_pmcid, legacy_wikidata_qid, legacy_core_id, refs)
            FROM STDIN WITH (FORMAT CSV, DELIMITER E'\t', HEADER, NULL '', FORCE_NULL (extra));
        """,
        }

@DBOS.step()
def simple_restore(table: str) -> int:
    table = "container"
    DBOS.logger.info(f"restoring {table}")
    count = 0
    sql = RESTORE_SQL[table]
    try:
        with psycopg.connect(conninfo=OLD_DATABASE_URL) as conn:
            with conn.cursor() as cur, open(outfile(table), "r") as f:
                with cur.copy(sql) as copy:
                    for line in f:
                        copy.write(line)
                        count += 1
    except Exception as e:
        bail(str(e))
    DBOS.logger.info(f"restored {count} {table}")
    return count

@DBOS.step()
def restore_work() -> int:
    table = "work"

    DBOS.logger.info("dropping work indices")
    indices = [
            ("fcapi_work_legacy_ident_idx", "legacy_ident"),
            ("fcapi_work_source_idx", "source"),
            ("fcapi_work_updated_idex", "updated"),
    ]
    for index, _ in indices:
        drop_index(index)

    count = simple_restore(table)

    DBOS.logger.info("restoring work indices")
    for index, column in indices:
        create_index("fcapi_work", index, column)

    return count

def chunked_update(sql: str, total_rows: int):
    runs = ceil(total_rows/CHUNKED_UPDATE_SIZE)
    for _ in range(runs):
        out, err = psql(sql, db_name=NEW_DB)
        DBOS.logger.info(out.strip())

@DBOS.step()
def restore_release() -> int:
    table = "release"

    DBOS.logger.info("dropping release indices")
    indices = [
            # TODO will i be ruined by pkey index?
            ("fcapi_release_container_id_idx", "container_id"),
            ("fcapi_release_work_id_idx", "work_id"),
            ("fcapi_release_legacy_rev_idx", "legacy_rev"),
            ("fcapi_release_source_idx", "source"),
            ("fcapi_release_updated_idx", "updated"),
    ]
    for index, _ in indices:
        drop_index(index)

    count = simple_restore(table)

    DBOS.logger.info("restoring release indices")
    for index, column in indices:
        create_index("fcapi_release", index, column)

    return count

@DBOS.step()
def restore_release_extid():
    DBOS.logger.info("restoring release extids")
    # TODO have to allow null foreign keys
    sql = f"""
      COPY fcapi_releaseextid (legacy_release_rev, id_type, id_value)
      FROM '{RELEASES_EXTID_OUT}'
      WITH (FORMAT CSV, DELIMITER E'\t', HEADER, NULL '');
    """
    out, err = psql(sql, db_name=NEW_DB)
    DBOS.logger.info(f"release extid: {out.strip()}")
    copied = copy_result_to_int(out)

    DBOS.logger.info("restoring release extid foreign keys")
    sql = f"""
    WITH chunk AS (
      SELECT
        r.ctid,
        r.legacy_release_rev,
      FROM fcapi_releaseextid AS ei
      WHERE release_id IS NULL
      FOR UPDATE LIMIT {CHUNKED_UPDATE_SIZE})
    UPDATE fcapi_releaseextid ei
    SET
      release_id = r.id
    FROM chunk
    JOIN fcapi_release r ON r.legacy_rev = chunk.legacy_release_rev
    WHERE ei.ctid = chunk.ctid
    """
    chunked_update(sql, copied)

    # TODO restore not null foreign keys

    return copied

@DBOS.step()
def restore_release_abstract() -> int:
    DBOS.logger.info("restoring release abstracts")
    # TODO have to allow null foreign keys
    sql = f"""
      COPY fcapi_releaseabstract (legacy_release_rev, sha1, mimetype, language, content)
      FROM '{RELEASES_ABSTRACT_OUT}'
      WITH (FORMAT CSV, DELIMITER E'\t', HEADER, NULL '');
    """
    out, err = psql(sql, db_name=NEW_DB)
    DBOS.logger.info(f"release abstract: {out.strip()}")
    copied = copy_result_to_int(out)

    DBOS.logger.info("restoring release abstract foreign keys")
    sql = f"""
    WITH chunk AS (
      SELECT
        ra.ctid,
        ra.legacy_release_rev,
      FROM fcapi_releaseabstract AS ra
      WHERE release_id IS NULL
      FOR UPDATE LIMIT {CHUNKED_UPDATE_SIZE})
    UPDATE fcapi_releaseabstract ra
    SET
      release_id = r.id
    FROM chunk
    JOIN fcapi_release r ON r.legacy_rev = chunk.legacy_release_rev
    WHERE ra.ctid = chunk.ctid
    """
    chunked_update(sql, copied)
    # TODO have to allow null foreign keys

    return copied

@DBOS.step()
def restore_release_contrib():
    DBOS.logger.info("restoring release contribs")
    # TODO have to allow null foreign keys
    sql = f"""
      COPY fcapi_releasecontrib (legacy_release_rev, raw_name, given_name, surname,
        legacy_creator_ident, role, raw_affiliation, position, extra)
      FROM '{RELEASES_CONTRIB_OUT}'
      WITH (FORMAT CSV, DELIMITER E'\t', HEADER, NULL '', FORCE_NULL (extra));
    """
    out, err = psql(sql, db_name=NEW_DB)
    DBOS.logger.info(f"release contrib: {out.strip()}")
    copied = copy_result_to_int(out)

    DBOS.logger.info("restoring release contrib foreign keys")
    sql = f"""
    WITH chunk AS (
      SELECT
        rc.ctid,
        rc.legacy_release_rev,
        rc.legacy_creator_ident,
      FROM fcapi_releasecontrib AS rc
      WHERE release_id IS NULL
      FOR UPDATE LIMIT {CHUNKED_UPDATE_SIZE})
    UPDATE fcapi_releasecontrib ra
    SET
      release_id = r.id
      creator_id = c.id
    FROM chunk
    JOIN fcapi_release r ON r.legacy_rev = chunk.legacy_release_rev
    FULL OUTER JOIN fcapi_creator c ON c.legacy_ident = chunk.legacy_creator_ident
    WHERE rc.ctid = chunk.ctid
    """
    chunked_update(sql, copied)
    # TODO restore not null foreign keys

    return copied

@DBOS.step()
def restore_release_ref():
    DBOS.logger.info("restoring refs")

    # TODO have to allow null foreign keys
    sql = f"""
      COPY fcapi_releaseref (position, legacy_release_rev, legacy_target_release_ident)
      FROM '{RELEASES_REF_OUT}'
      WITH (FORMAT CSV, DELIMITER E'\t', HEADER, NULL '');
    """
    out, err = psql(sql, db_name=NEW_DB)
    DBOS.logger.info(f"release ref: {out.strip()}")
    copied = copy_result_to_int(out)

    DBOS.logger.info("restoring release ref release_id foreign key")
    sql = f"""
    WITH chunk AS (
      SELECT
        rc.ctid,
        rc.legacy_release_rev,
      FROM fcapi_releaseref AS rr
      WHERE release_id IS NULL
      FOR UPDATE LIMIT {CHUNKED_UPDATE_SIZE})
    UPDATE fcapi_releaseref rr
    SET
      release_id = r.id
    FROM chunk
    JOIN fcapi_release r ON r.legacy_rev = chunk.legacy_release_rev
    WHERE rr.ctid = chunk.ctid
    """
    chunked_update(sql, copied)

    DBOS.logger.info("restoring release ref target_release_id foreign key")
    sql = f"""
    WITH chunk AS (
      SELECT
        rc.ctid,
        rc.legacy_target_release_rev,
      FROM fcapi_releaseref AS rr
      WHERE release_id IS NULL
      FOR UPDATE LIMIT {CHUNKED_UPDATE_SIZE})
    UPDATE fcapi_releaseref rr
    SET
      target_release_id = r.id
    FROM chunk
    JOIN fcapi_release r ON r.legacy_rev = chunk.legacy_target_release_rev
    WHERE rr.ctid = chunk.ctid
    """
    chunked_update(sql, copied)

    # TODO have to restore not null foreign keys

    return copied

@DBOS.step()
def restore_files():
    # TODO
    bail()

@DBOS.step()
def restore_file_url():
    # TODO
    bail()

@DBOS.step()
def restore_filesets():
    # TODO
    bail()

@DBOS.step()
def restore_fileset_file():
    # TODO
    bail()

@DBOS.step()
def restore_webcaptures():
    # TODO
    bail()

@DBOS.step()
def restore_webcapture_url():
    # TODO
    bail()

@DBOS.step()
def restore_webcapture_cdx():
    # TODO
    bail()

@app.get("/restore")
@DBOS.workflow()
def restore_workflow():
    simple_restore("container")
    simple_restore("creator")
    restore_work()
    #restore_releases()
    #restore_release_extid()
    #restore_release_abstract()
    #restore_release_contrib()
    #restore_release_ref()
    #restore_files()
    #restore_file_url()
    #restore_filesets()
    #restore_fileset_file()
    #restore_webcaptures()
    #restore_webcapture_url()
    #restore_webcapture_cdx()
