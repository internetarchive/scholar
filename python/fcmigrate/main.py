import os
import subprocess
from math import ceil
from typing import Tuple
from fastapi import FastAPI
from dbos import DBOS, Queue

SOURCE = "legacy_import"
OLD_DB = "fatcat_prod"
NEW_DB = "fatcat2"
CHUNKED_UPDATE_SIZE = 50000000

CWD = os.getcwd()

CONTAINERS_OUT = os.path.join(CWD, "containers.tsv")
CREATORS_OUT = os.path.join(CWD, "creators.tsv")
WORKS_OUT = os.path.join(CWD, "works.tsv")
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

# TODO switch to psycopg
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

@DBOS.step()
def ensure_psql():
    DBOS.logger.info("ensuring psql")
    out, err = psql("SELECT 1")
    if "(1 row)" not in out:
        bail(f"unexpected psql stdout: {out}")
    DBOS.logger.info("ensured psql")

@DBOS.step()
def dump_containers() -> int:
    DBOS.logger.info("dumping containers")
    sql = f"""
    COPY (
      SELECT
        ci.id AS legacy_ident,
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
        ci.is_live = true
        AND
        ci.redirect_id is NULL
        AND
        cr.container_type != 'test'
     ) TO '{CONTAINERS_OUT}' WITH (FORMAT CSV, DELIMITER E'\t', HEADER);
    """
    out, err = psql(sql, db_name=OLD_DB)
    DBOS.logger.info(f"containers: {out.strip()}")
    return copy_result_to_int(out)

@DBOS.step()
def dump_creators() -> int:
    DBOS.logger.info("dumping creators")
    sql = f"""
    COPY (
      SELECT
        ci.id AS legacy_ident,
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
      ) TO '{CREATORS_OUT}' WITH (FORMAT CSV, DELIMITER E'\t', HEADER);"""
    out, err = psql(sql, db_name=OLD_DB)
    DBOS.logger.info(f"creators: {out.strip()}")
    return copy_result_to_int(out)

@DBOS.step()
def dump_works() -> str:
    DBOS.logger.info("dumping works")
    sql = f"""
    COPY (
      SELECT
        wi.id AS legacy_ident,
        '{SOURCE}' AS source,
        to_json(wr.extra_json) AS extra
      FROM work_ident wi
      JOIN work_rev wr ON wi.rev_id = wr.id
      WHERE
        wi.is_live = true
      AND
        wi.redirect_id IS NULL
      ) TO '{WORKS_OUT}' WITH (FORMAT CSV, DELIMITER E'\t', HEADER);

    """
    out, err = psql(sql, db_name=OLD_DB)
    DBOS.logger.info(f"works: {out.strip()}")
    return copy_result_to_int(out)

@DBOS.step()
def dump_releases() -> int:
    DBOS.logger.info("dumping releases")
    sql = f"""
    COPY (
      SELECT
        ri.id AS legacy_ident,
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

        rr.work_ident_id AS legacy_work_ident,
        rr.container_ident_id AS legacy_container_ident,
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
      ) TO '{RELEASES_OUT}' WITH (FORMAT CSV, DELIMITER E'\t', HEADER);
    """
    out, err = psql(sql, db_name=OLD_DB)
    DBOS.logger.info(f"releases: {out.strip()}")
    return copy_result_to_int(out)

@DBOS.step()
def dump_release_extid() -> int:
    DBOS.logger.info("dumping release_extid")
    sql = f"""
    COPY (
      SELECT
        ei.release_rev AS legacy_release_rev,
        ei.extid_type AS id_type,
        ei.value AS id_value
      FROM release_ident ri
      JOIN release_rev_extid ei ON ri.rev_id = ei.release_rev
      WHERE ri.is_live = true
      AND ri.redirect_id IS NULL
    ) TO '{RELEASES_EXTID_OUT}' WITH (FORMAT CSV, DELIMITER E'\t', HEADER);
    """
    out, err = psql(sql, db_name=OLD_DB)
    DBOS.logger.info(f"extid: {out.strip()}")
    return copy_result_to_int(out)

@DBOS.step()
def dump_release_abstract():
    DBOS.logger.info("dumping abstracts")
    sql = f"""
    COPY (
      SELECT
        ra.release_rev AS legacy_release_rev,
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
      ) TO '{RELEASES_ABSTRACT_OUT}' WITH (FORMAT CSV, DELIMITER E'\t', HEADER);
    """
    out, err = psql(sql, db_name=OLD_DB)
    DBOS.logger.info(f"abstracts: {out.strip()}")
    return copy_result_to_int(out)

@DBOS.step()
def dump_release_contrib():
    DBOS.logger.info("dumping contribs")
    sql = f"""
    COPY (
      SELECT
        rc.release_rev AS legacy_release_rev,
        rc.raw_name,
        rc.given_name,
        rc.surname,
        rc.creator_ident_id AS legacy_creator_ident,
        rc.role,
        rc.raw_affiliation,
        rc.index_val AS position,
        to_json(rc.extra_json) AS extra
      FROM release_ident ri
      JOIN release_contrib rc ON ri.rev_id = rc.release_rev
      WHERE ri.is_live = true
      AND ri.redirect_id IS NULL
      ) TO '{RELEASES_ABSTRACT_OUT}' WITH (FORMAT CSV, DELIMITER E'\t', HEADER);
    """
    out, err = psql(sql, db_name=OLD_DB)
    DBOS.logger.info(f"contribs: {out.strip()}")
    return copy_result_to_int(out)

@DBOS.step()
def dump_release_ref():
    DBOS.logger.info("dumping refs")
    sql = f"""
    COPY (
      SELECT
        rr.index_val AS position,
        rr.release_rev AS legacy_release_rev,
        rr.target_release_ident_id AS legacy_target_release_ident
      FROM release_ident ri
      JOIN release_ref rr ON ri.rev_id = rr.release_rev
      WHERE ri.is_live = true
      AND ri.redirect_id IS NULL
      ) TO '{RELEASES_REF_OUT}' WITH (FORMAT CSV, DELIMITER E'\t', HEADER);
    """
    out, err = psql(sql, db_name=OLD_DB)
    DBOS.logger.info(f"refs: {out.strip()}")
    return copy_result_to_int(out)

@DBOS.step()
def dump_files():
    DBOS.logger.info("dumping files")
    sql = f"""
    COPY (
      SELECT
        fi.rev_id AS legacy_rev,
        fi.id AS legacy_ident,
        '{SOURCE}' as source,
        fr.size_bytes,
        fr.sha1,
        fr.sha256,
        fr.mimetype,
        fr.md5,
        to_json(f.extra_json) AS extra,
        (SELECT target_release_ident_id
         FROM file_rev_release frr
         WHERE frr.file_rev = fr.id
        ) AS legacy_release_ident
      FROM
        file_ident fi
      JOIN file_rev fr ON fi.rev_id = fr.id
      WHERE fi.is_live = true
      AND fi.redirect_id IS NULL
      ) TO '{RELEASES_REF_OUT}' WITH (FORMAT CSV, DELIMITER E'\t', HEADER);
    """
    out, err = psql(sql, db_name=OLD_DB)
    DBOS.logger.info(f"files: {out.strip()}")
    return copy_result_to_int(out)

@DBOS.step()
def dump_file_url():
    DBOS.logger.info("dumping file urls")
    sql = f"""
    COPY (
      SELECT
        fu.rel,
        fu.url,
        fi.rev_id AS legacy_file_rev
      FROM
        file_ident fi
      JOIN file_rev_url fu ON fi.rev_id = fu.file_rev
      WHERE fi.is_live = true
      AND fi.redirect_id IS NULL
      ) TO '{FILES_URL_OUT}' WITH (FORMAT CSV, DELIMITER E'\t', HEADER);
    """
    out, err = psql(sql, db_name=OLD_DB)
    DBOS.logger.info(f"file urls: {out.strip()}")
    return copy_result_to_int(out)

@DBOS.step()
def dump_filesets():
    DBOS.logger.info("dumping filesets")
    sql = f"""
    COPY (
      SELECT
        fi.rev_id AS legacy_rev,
        to_json(f.extra_json) AS extra,
        '{SOURCE}' AS source,
        (SELECT
          target_release_ident_id
         FROM fileset_rev_release frr
         WHERE frr.fileset_rev = fi.rev_id
        ) AS legacy_release_ident
      FROM
        fileset_ident fi
      JOIN fileset_rev fr ON fi.rev_id = fr.id
      WHERE fi.is_live = true
      AND fi.redirect_id IS NULL
      ) TO '{FILESETS_OUT}' WITH (FORMAT CSV, DELIMITER E'\t', HEADER);
    """
    out, err = psql(sql, db_name=OLD_DB)
    DBOS.logger.info(f"filesets: {out.strip()}")
    return copy_result_to_int(out)

@DBOS.step()
def dump_fileset_url():
    DBOS.logger.info("dumping fileset urls")
    sql = f"""
    COPY (
      SELECT
        fi.rev_id AS legacy_fileset_rev,
        fu.rel,
        fu.url
      FROM
        fileset_ident fi
      JOIN fileset_rev_url fu ON fi.rev_id = fu.fileset_rev
      WHERE fi.is_live = true
      AND fi.redirect_id IS NULL
      ) TO '{FILESETS_URL_OUT}' WITH (FORMAT CSV, DELIMITER E'\t', HEADER);
    """
    out, err = psql(sql, db_name=OLD_DB)
    DBOS.logger.info(f"fileset urls: {out.strip()}")
    return copy_result_to_int(out)

@DBOS.step()
def dump_fileset_file():
    DBOS.logger.info("dumping fileset files")
    sql = f"""
    COPY (
      SELECT
        fi.rev_id AS legacy_fileset_rev,
        ff.path_name,
        '{SOURCE}' AS source,
        ff.size_bytes,
        ff.md5,
        ff.sha1,
        ff.sha256,
        ff.mimetype,
        to_json(ff.extra_json) AS extra,
      FROM
        fileset_ident fi
      JOIN fileset_rev_file ff ON fi.rev_id = ff.fileset_rev
      WHERE fi.is_live = true
      AND fi.redirect_id IS NULL
      ) TO '{FILESETS_FILE_OUT}' WITH (FORMAT CSV, DELIMITER E'\t', HEADER);

    """
    out, err = psql(sql, db_name=OLD_DB)
    DBOS.logger.info(f"fileset files: {out.strip()}")
    return copy_result_to_int(out)

@DBOS.step()
def dump_webcaptures():
    DBOS.logger.info("dumping webcaptures")
    sql = f"""
    COPY (
      SELECT
        wi.rev_id AS legacy_rev,
        wi.id AS legacy_ident,
        '{SOURCE}' AS source,
        to_json(wr.extra_json) AS extra,
        wr.original_url,
        wr.timestamp AS captured,
        (SELECT target_release_ident_id
         FROM webcapture_rev_release
         WHERE webcapture_rev = wr.id) AS legacy_release_id
      FROM webcapture_ident wi
      JOIN webcapture_rev wr ON wi.rev_id = wr.id
      WHERE wi.is_live = true
      AND wi.redirect_id IS NULL
      ) TO '{WEBCAPTURES_OUT}' WITH (FORMAT CSV, DELIMITER E'\t', HEADER);
    """
    out, err = psql(sql, db_name=OLD_DB)
    DBOS.logger.info(f"webcaptures: {out.strip()}")
    return copy_result_to_int(out)

@DBOS.step()
def dump_webcapture_url():
    DBOS.logger.info("dumping webcapture urls")
    sql = f"""
    COPY (
      SELECT
        wu.webcapture_rev AS legacy_webcapture_rev,
        wu.rel,
        wu.url
      FROM webcapture_ident wi
      JOIN webcapture_rev_url wu ON wu.webcapture_rev = wi.rev_id
      WHERE wi.is_live = true
      AND wi.redirect_id IS NULL
      ) TO '{WEBCAPTURES_URL_OUT}' WITH (FORMAT CSV, DELIMITER E'\t', HEADER);
    """
    out, err = psql(sql, db_name=OLD_DB)
    DBOS.logger.info(f"webcapture urls: {out.strip()}")
    return copy_result_to_int(out)

@DBOS.step()
def dump_webcapture_cdx():
    DBOS.logger.info("dumping webcapture cdx")
    sql = f"""
    COPY (
      SELECT
        wc.webcapture_rev AS legacy_webcapture_rev,
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
      ) TO '{WEBCAPTURES_CDX_OUT}' WITH (FORMAT CSV, DELIMITER E'\t', HEADER);
    """
    out, err = psql(sql, db_name=OLD_DB)
    DBOS.logger.info(f"webcapture cdx: {out.strip()}")
    return copy_result_to_int(out)

@app.get("/dump")
@DBOS.workflow()
def dump_workflow():
    ensure_psql()
    queue = Queue("dump_queue", concurrency=10, worker_concurrency=2)
    handles = []
    dumpers = [
        dump_containers,
        dump_creators,
        dump_works,
        dump_releases,
        dump_release_extid,
        dump_release_abstract,
        dump_release_contrib,
        dump_release_ref,
        dump_files,
        dump_file_url,
        dump_filesets,
        dump_fileset_file,
        dump_webcaptures,
        dump_webcapture_url,
        dump_webcapture_cdx,
    ]
    for dumper in dumpers:
        handles.append(queue.enqueue(dumper))

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

@DBOS.step()
def restore_containers() -> int:
    DBOS.logger.info("restoring containers")
    sql = f"""
    COPY fcapi_container (
      legacy_ident, name, extra, container_type, publisher,
      issnl, issne, issnp, wikidata_qid, source)
    FROM '{CONTAINERS_OUT}'
    WITH (FORMAT CSV, DELIMITER E'\t', HEADER, NULL '',
          FORCE_NULL (extra));
    """
    out, err = psql(sql, db_name=NEW_DB)
    DBOS.logger.info(f"containers: {out.strip()}")
    return copy_result_to_int(out)

@DBOS.step()
def restore_creators() -> int:
    DBOS.logger.info("restoring creators")
    sql = f"""
    COPY fcapi_creator (
      legacy_ident, display_name, given_name, surname, orcid,
      source, extra)
    FROM '{CREATORS_OUT}'
    WITH (FORMAT CSV, DELIMITER E'\t', HEADER, NULL '',
          FORCE_NULL (extra));
    """
    out, err = psql(sql, db_name=NEW_DB)
    DBOS.logger.info(f"creators: {out.strip()}")
    return copy_result_to_int(out)

@DBOS.step()
def restore_works() -> int:
    DBOS.logger.info("restoring works")

    DBOS.logger.info("dropping work indices")
    indices = [
            ("fcapi_work_legacy_ident_idx", "legacy_ident"),
            ("fcapi_work_source_idx", "source"),
            ("fcapi_work_updated_idex", "updated"),
    ]
    for index, _ in indices:
        drop_index(index)

    sql = f"""
    COPY fcapi_work (legacy_ident, source, extra)
    FROM '{WORKS_OUT}' WITH (FORMAT CSV, DELIMITER E'\t', HEADER, NULL '', FORCE_NULL (extra));
    """
    out, err = psql(sql, db_name=NEW_DB)
    DBOS.logger.info(f"works: {out.strip()}")

    DBOS.logger.info("restoring work indices")
    for index, column in indices:
        create_index("fcapi_work", index, column)

    return copy_result_to_int(out)

def chunked_update(sql: str, total_rows: int):
    runs = ceil(total_rows/CHUNKED_UPDATE_SIZE)
    for _ in range(runs):
        out, err = psql(sql, db_name=NEW_DB)
        DBOS.logger.info(out.strip())

@DBOS.step()
def restore_releases() -> int:
    DBOS.logger.info("restoring releases")
    # TODO have to allow null work_id

    DBOS.logger.info("dropping release indices")
    indices = [
            ("fcapi_release_legacy_container_ident_idx", "legacy_container_ident"),
            ("fcapi_release_legacy_ident_idx", "legacy_ident"),
            ("fcapi_release_legacy_rev_idx", "legacy_rev"),
            ("fcapi_release_legacy_work_ident_idx", "legacy_work_ident"),
            ("fcapi_release_source_idx", "source"),
            ("fcapi_release_updated_idx", "updated"),
            ("fcapi_release_work_id_idx", "work_id"),
            ("fcapi_release_container_id_idx", "container_id"),
    ]
    for index, _ in indices:
        drop_index(index)

    sql = f"""
      COPY fcapi_release (
        legacy_ident, legacy_rev, legacy_work_ident, legacy_container_ident, extra, source, title,
        original_title, subtitle, release_type, release_stage, release_date, release_year, volume,
        issue, pages, number, version, publisher, language, license_slug, withdrawn_status, legacy_doi,
        legacy_pmid, legacy_pmcid, legacy_wikidata_qid, legacy_core_id, refs)
      FROM '{RELEASES_OUT}' WITH (FORMAT CSV, DELIMITER E'\t', HEADER, NULL '', FORCE_NULL (extra));
    """
    out, err = psql(sql, db_name=NEW_DB)
    DBOS.logger.info(f"releases: {out.strip()}")

    copied = copy_result_to_int(out)

    DBOS.logger.info("restoring release foreign keys")

    sql = f"""
    WITH chunk AS (
      SELECT
        r.ctid,
        r.legacy_work_ident,
        r.legacy_container_ident
      FROM fcapi_release AS r
      WHERE work_id IS NULL
      FOR UPDATE LIMIT {CHUNKED_UPDATE_SIZE})
    UPDATE fcapi_release r
    SET
      work_id = fw.id,
      container_id = fco.id
    FROM chunk
    JOIN fcapi_work fw ON fw.legacy_ident = chunk.legacy_work_ident
    FULL OUTER JOIN fcapi_container fco ON fco.legacy_ident = chunk.legacy_container_ident
    WHERE r.ctid = chunk.ctid
    """
    chunked_update(sql, copied)

    DBOS.logger.info("restoring release indices")
    for index, column in indices:
        create_index("fcapi_release", index, column)

    # TODO restore have to not null work_id

    return copied


@DBOS.step
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

@DBOS.step
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

@DBOS.step
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

@DBOS.step
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

@DBOS.step
def restore_files():
    # TODO
    bail()

@DBOS.step
def restore_file_url():
    # TODO
    bail()

@DBOS.step
def restore_filesets():
    # TODO
    bail()

@DBOS.step
def restore_fileset_file():
    # TODO
    bail()

@DBOS.step
def restore_webcaptures():
    # TODO
    bail()

@DBOS.step
def restore_webcapture_url():
    # TODO
    bail()

@DBOS.step
def restore_webcapture_cdx():
    # TODO
    bail()

@app.get("/restore")
@DBOS.workflow()
def restore_workflow():
    ensure_psql()
    restore_containers()
    restore_works()
    restore_creators()
    restore_releases()
    restore_release_extid()
    restore_release_abstract()
    restore_release_contrib()
    restore_release_ref()
    restore_files()
    restore_file_url()
    restore_filesets()
    restore_fileset_file()
    restore_webcaptures()
    restore_webcapture_url()
    restore_webcapture_cdx()
