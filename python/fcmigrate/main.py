import os
from typing import List, Tuple

from fastapi import FastAPI
from dbos import DBOS, Queue
import psycopg

CWD = os.getcwd()
SOURCE = "legacy_import"
OLD_DATABASE_URL="postgresql:///fatcat_prod?host=/home/vilmibm/src/fatcat-scholar/devdb/pgdata/sockets"
NEW_DATABASE_URL="postgresql:///fatcat2?host=/home/vilmibm/src/fatcat-scholar/devdb/pgdata/sockets"

app = FastAPI()
DBOS(fastapi=app)

def bail(msg: str):
    DBOS.logger.error(msg)
    os._exit(1)

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
        "releasefile": """
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
              ) TO STDOUT WITH (FORMAT CSV, DELIMITER E'\t', HEADER);
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
              ) TO STDOUT WITH (FORMAT CSV, DELIMITER E'\t', HEADER);
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

def drop_indices(names: List[str]):
    """drop a list of named indices from the new fatcat db"""
    try:
        with psycopg.connect(conninfo=NEW_DATABASE_URL) as conn, conn.cursor() as cur:
           for name in names:
               cur.execute("DROP INDEX %s", (name,))
    except Exception as e:
        bail(str(e))

def create_indices(indices: List[Tuple[str, str, str]]):
    """given tuples of (index_name, table, colum), create indices in the new db"""
    try:
        with psycopg.connect(conninfo=NEW_DATABASE_URL) as conn, conn.cursor() as cur:
           for index in indices:
               cur.execute("CREATE INDEX %s ON %s (%s)", index)
    except Exception as e:
        bail(str(e))

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
        "releasefile": """
            COPY fcapi_releasefile (legacy_rev, id, source, size_bytes, sha1, sha256,
            mimetype, md5, extra, release_id)
            FROM STDIN WITH (FORMAT CSV, DELIMITER E'\t', HEADER, NULL '', FORCE_NULL (extra));
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
            COPY fcapi_filesetfile (fileset_id, path_name, source, size_bytes, md5, sha1,
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

    DBOS.logger.info(f"dropping {table} indices")
    indices = [
            # TODO will i be ruined by pkey index?
            ("fcapi_work_legacy_ident_idx", f"fcapi_{table}", "legacy_ident"),
            ("fcapi_work_source_idx", f"fcapi_{table}", "source"),
            ("fcapi_work_updated_idex", f"fcapi_{table}", "updated"),
    ]
    drop_indices([idx[0] for idx in indices])

    count = simple_restore(table)

    DBOS.logger.info(f"restoring {table} indices")
    create_indices(indices)

    return count

@DBOS.step()
def restore_release() -> int:
    table = "release"

    DBOS.logger.info(f"dropping {table} indices")
    indices = [
            # TODO will i be ruined by pkey index?
            ("fcapi_release_container_id_idx", f"fcapi_{table}", "container_id"),
            ("fcapi_release_work_id_idx", f"fcapi_{table}", "work_id"),
            ("fcapi_release_legacy_rev_idx", f"fcapi_{table}", "legacy_rev"),
            ("fcapi_release_source_idx", f"fcapi_{table}", "source"),
            ("fcapi_release_updated_idx", f"fcapi_{table}", "updated"),
    ]
    drop_indices([idx[0] for idx in indices])

    count = simple_restore(table)

    DBOS.logger.info(f"restoring {table} indices")
    create_indices(indices)

    return count

@DBOS.step()
def restore_releasefile():
    table = "files"
    indices = [
            # TODO will i be ruined by pkey,fkey indices?
            ("fcapi_releasefile_legacy_rev_idx", f"fcapi_{table}", "legacy_rev"),
            ("fcapi_releasefile_md5_idx", f"fcapi_{table}", "md5"),
            ("fcapi_releasefile_sha1_idx", f"fcapi_{table}", "sha1"),
            ("fcapi_releasefile_sha256_idx", f"fcapi_{table}", "sha256"),
            ("fcapi_releasefile_source_idx", f"fcapi_{table}", "source"),
            ("fcapi_releasefile_updated_idx", f"fcapi_{table}", "updated"),
    ]

    DBOS.logger.info(f"dropping {table} indices")
    drop_indices([idx[0] for idx in indices])

    count = simple_restore(table)

    DBOS.logger.info(f"restoring {table} indices")
    create_indices(indices)

    return count

@app.get("/restore")
@DBOS.workflow()
def restore_workflow():
    simple_restore("container")
    simple_restore("creator")
    restore_work()
    restore_release()
    simple_restore("releaseextid")
    simple_restore("releaseabstract")
    simple_restore("releasecontrib")
    simple_restore("releaseref")
    restore_releasefile()
    simple_restore("fileurl")
    simple_restore("fileset")
    simple_restore("filesetfile")
    simple_restore("webcapture")
    simple_restore("webcaptureurl")
    simple_restore("webcapturecdx")
