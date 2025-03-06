import os
import subprocess
from typing import Tuple
from fastapi import FastAPI
from dbos import DBOS, Queue

SOURCE = "legacy_import"
OLD_DB = "fatcat_prod"
NEW_DB = "fatcat2"

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
    sql = f"""
    COPY (
      SELECT
        ei.release_rev AS legacy_release_rev,
        ei.extid_type AS id_type,
        ei.value AS id_value
      FROM release_ident ri
      JOIN release_rev_extid ei ON ri.release_rev = ri.release_rev
      WHERE ri.is_live = true
      AND ri.redirect_id IS NULL
    ) TO '{RELEASES_EXTID_OUT}' WITH (FORMAT CSV, DELIMITER E'\t', HEADER);
    """
    out, err = psql(sql, db_name=OLD_DB)
    DBOS.logger.info(f"extid: {out.strip()}")
    return copy_result_to_int(out)

@DBOS.step()
def dump_release_abstract():
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
        to_json(rc.extra_json) AS extra,
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

@DBOS.step()
def restore_containers():
    restore_containers()

@app.get("/restore")
@DBOS.workflow()
def restore_workflow():
    ensure_psql()
    restore_containers()
    # TODO
