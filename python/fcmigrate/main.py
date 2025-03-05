import os
import subprocess
from typing import Tuple
from fastapi import FastAPI
from dbos import DBOS

SOURCE = "legacy_import"

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
def dump_containers():
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
    out, err = psql(sql, db_name="fatcat_prod")
    DBOS.logger.info(out.strip())

@DBOS.step()
def dump_creators():
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
    out, err = psql(sql, db_name="fatcat_prod")
    DBOS.logger.info(out.strip())

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
    out, err = psql(sql, db_name="fatcat_prod")
    DBOS.logger.info(out.strip())

@DBOS.step()
def dump_releases():
    # TODO
    bail("not implemented")

@DBOS.step()
def dump_release_extid():
    # TODO
    bail("not implemented")

@DBOS.step()
def dump_release_abstract():
    # TODO
    bail("not implemented")

@DBOS.step()
def dump_release_contrib():
    # TODO
    bail("not implemented")

@DBOS.step()
def dump_release_ref():
    # TODO
    bail("not implemented")

@DBOS.step()
def dump_files():
    # TODO
    bail("not implemented")

@DBOS.step()
def dump_file_url():
    # TODO
    bail("not implemented")

@DBOS.step()
def dump_filesets():
    # TODO
    bail("not implemented")

@DBOS.step()
def dump_fileset_url():
    # TODO
    bail("not implemented")

@DBOS.step()
def dump_fileset_file():
    # TODO
    bail("not implemented")

@DBOS.step()
def dump_webcaptures():
    # TODO
    bail("not implemented")

@DBOS.step()
def dump_webcapture_url():
    # TODO
    bail("not implemented")

@DBOS.step()
def dump_webcapture_cdx():
    # TODO
    bail("not implemented")

@app.get("/dump")
@DBOS.workflow()
def dump_workflow():
    ensure_psql()
    # TODO try to enqueue these in parallel
    dump_containers()
    dump_creators()
    dump_works()
    dump_releases()
    dump_release_extid()
    dump_release_abstract()
    dump_release_contrib()
    dump_release_ref()
    dump_files()
    dump_file_url()
    dump_filesets()
    dump_fileset_file()
    dump_webcaptures()
    dump_webcapture_url()
    dump_webcapture_cdx()

@DBOS.step()
def restore_containers():
    restore_containers()

@app.get("/restore")
@DBOS.workflow()
def restore_workflow():
    ensure_psql()
    restore_containers()
    # TODO
