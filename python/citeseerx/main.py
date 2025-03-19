"""
one off script for creating a tsv with:

sha1 of a pdf in citeseerx, corresponding citeseerx url, corresponding WBM url

this is complicated by some facts:

    - sha1 is not unique in fatcat (ie, multiple file_rev.ids may map to a sha1)
    - file_rev_url.url is not gauranteed to be unique (ie, several of the same urls may end up as rows)

"""
from itertools import batched
import logging
import os
import subprocess
from typing import Dict, List, Optional
from functools import wraps
from time import time
from concurrent.futures import ThreadPoolExecutor, as_completed

#import psycopg_pool
import psycopg

DB_URL = os.environ.get("DB_URL",
                        "postgresql:///fatcat_prod?host=/home/vilmibm/src/fatcat-scholar/devdb/pgdata/sockets")
CITESEER_SHA_PATH = os.environ.get("CITESEER_SHA_PATH",
                                   "/home/vilmibm/src/work/scratch/common_citeseer.txt")

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

SQL = """
    SELECT fru.url FROM file_rev_url fru
    JOIN file_rev fr ON fru.file_rev = fr.id
    WHERE fr.sha1 = %s
    AND fru.url LIKE '%%//web.archive.org%%'
"""

#pool = psycopg_pool.ConnectionPool(conninfo=DB_URL)
#pool.wait()

def process(name: str, sha1s: List[str]) -> Dict[str, Optional[str]]:
    out = { }
    total = len(sha1s)
    count = 0
    with psycopg.connect(conninfo=DB_URL) as conn:
        for sha1 in sha1s:
            with conn.cursor() as cur:
                sha1 = sha1.strip()
                us = cur.execute(SQL, (sha1,)).fetchall()
                urls = sorted(us, key=lambda u: u[0].split("/")[4], reverse=True)
                if len(urls) == 0:
                    out[sha1] = None
                else:
                    out[sha1] = urls[0][0]
                count += 1
                if count % 10000 == 0:
                    print(f"{name}: {count}/{total}")
    return out

def main():
    out = subprocess.run(["wc", "-l", CITESEER_SHA_PATH], capture_output=True, check=True)
    total = int(out.stdout.decode("utf-8").strip().split(' ')[0])

    count = 0
    skip_count = 0
    with open(CITESEER_SHA_PATH) as f:
        with ThreadPoolExecutor(max_workers=10) as executor:
            futures = []
            bcount = 0
            for batch in batched(f, 450000):
                futures.append(executor.submit(process, f"batch {bcount}", batch))
                bcount += 1
            final = {}
            for future in as_completed(futures):
                final = final | future.result()

    with open("./sha1_urls.tsv", mode='w') as outf, open("./sha1_skip.txt", mode="w") as skipf:
        count = 0
        for sha1, url in final.items():
            if url is None:
                print(sha1, file=skipf)
                skip_count += 1
            else:
                print(f"{sha1}\t{citeseer_url(sha1)}\t{url}", file=outf)
                count += 1
            print(f"\rprogress: {count}/{total} skip: {skip_count}", end="")
        

if __name__ == "__main__":
    main()
