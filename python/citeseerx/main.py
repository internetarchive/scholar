"""
one off script for creating a tsv with:

sha1 of a pdf in citeseerx, corresponding citeseerx url, corresponding WBM url

this is complicated by some facts:

    - sha1 is not unique in fatcat (ie, multiple file_rev.ids may map to a sha1)
    - file_rev_url.url is not gauranteed to be unique (ie, several of the same urls may end up as rows)

"""
import logging
import subprocess
from functools import wraps
from time import time

import psycopg

# TODO fix for prod
DB_URL="postgresql:///fatcat_prod?host=/home/vilmibm/src/fatcat-scholar/devdb/pgdata/sockets"
CITESEER_SHA_PATH = "/home/vilmibm/src/work/scratch/common_citeseer.txt"

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

def citeseer_url(sha1: str) -> str:
    return f"https://citeseerx.ist.psu.edu/document?repid=rep1&type=pdf&doi={sha1}"

def main():
    out = subprocess.run(["wc", "-l", CITESEER_SHA_PATH], capture_output=True, check=True)
    total = int(out.stdout.decode("utf-8").strip().split(' ')[0])

    count = 0
    with psycopg.connect(conninfo=DB_URL) as conn, conn.cursor() as cur:
        with open(CITESEER_SHA_PATH) as f:
            for line in f:
                sha1 = line.strip()
                sql = """
                SELECT fru.url FROM file_rev_url fru
                JOIN file_rev fr ON fru.file_rev = fr.id
                WHERE fr.sha1 = %s
                AND fru.url LIKE '%%//web.archive.org%%'
                """
                cur.execute(sql, (sha1,))
                urls = cur.fetchall()
                urls = sorted(urls, key=lambda u: u[0].split("/")[4], reverse=True)
                print(f"{sha1}\t{citeseer_url(sha1)}\t{urls[0][0]}")
                count += 1
                if count % 10000:
                    print(f"{count}/{total}\r", end="")

if __name__ == "__main__":
    main()
