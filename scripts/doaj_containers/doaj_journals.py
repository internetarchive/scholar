"""
Shared helpers for the DOAJ container backfill scripts
(backfill_container_doaj_extra.py, backfill_container_in_doaj.py).

Reads DOAJ's journal CSV the way trawler's doaj journal importer does, and
finds the fatcat_container ES docs for the journals in it.
"""
import csv
import io
import json
import tomllib
from dataclasses import dataclass, field
from pathlib import Path
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen

CONFIG = Path.home() / ".config" / "trawler" / "config.toml"
ISSNL_TABLE = Path(__file__).resolve().parent / "20261008.issn2issnl.tsv"
DOAJ_CSV_URL = "https://doaj.org/csv"
# doaj.org refuses urllib's default User-Agent; this is the one trawler sends.
USER_AGENT = "ia-scholar-trawler (scholar@archive.org)"

COL_PISSN = "Journal ISSN (print version)"
COL_EISSN = "Journal EISSN (online version)"
COL_DOAJURL = "URL in DOAJ"
COL_ADDEDON = "Added on Date"


def load_config() -> dict:
    return tomllib.loads(CONFIG.read_text())


def normalize_issn(i: str) -> str:
    """ISSN in canonical NNNN-NNNC form, or "" if malformed (mirrors issn.Normalize)."""
    i = i.strip().upper()
    if len(i) == 8:
        i = i[:4] + "-" + i[4:]
    if len(i) != 9 or i[4] != "-":
        return ""
    digits = i[:4] + i[5:8]
    if not digits.isascii() or not digits.isdigit():
        return ""
    total = sum(int(ch) * (8 - n) for n, ch in enumerate(digits))
    check = (11 - total % 11) % 11
    want = "X" if check == 10 else str(check)
    return i if i[8] == want else ""


def doaj_journal_id(toc_url: str) -> str:
    """DOAJ's journal id off the end of its TOC URL (mirrors doajJournalID)."""
    marker = "/toc/"
    i = toc_url.rfind(marker)
    if i == -1:
        return ""
    return toc_url[i + len(marker):].strip("/")


def load_issnl_table() -> dict[str, str]:
    out = {}
    with ISSNL_TABLE.open(newline="") as f:
        for row in csv.reader(f, delimiter="\t"):
            if len(row) == 2:
                out[row[0]] = row[1]
    return out


def read_csv_text(path: str | None) -> str:
    if path:
        return Path(path).read_text(encoding="utf-8-sig")
    print(f"fetching {DOAJ_CSV_URL} ...")
    with urlopen(Request(DOAJ_CSV_URL, headers={"User-Agent": USER_AGENT}), timeout=300) as resp:
        return resp.read().decode("utf-8-sig")


@dataclass
class Journal:
    issns: list[str]  # the row's own valid print/electronic ISSNs
    linking: list[str] = field(default_factory=list)  # ISSN-Ls they map to
    doaj_id: str = ""
    added_on: str = ""

    def doaj_extra(self) -> dict:
        """extra.doaj as trawler's journalToContainer builds it."""
        out = {}
        if self.doaj_id:
            out["id"] = self.doaj_id
        if self.added_on:
            out["added_on"] = self.added_on
        return out


@dataclass
class DOAJ:
    rows: int
    no_issn: int
    journals: list[Journal]

    def all_issns(self) -> set[str]:
        """Every ISSN to match containers on: each row's own, plus ISSN-Ls,
        since legacy containers often carry only an ISSN-L."""
        out = set()
        for j in self.journals:
            out.update(j.issns)
            out.update(j.linking)
        return out

    def by_issn(self) -> dict[str, Journal]:
        """ISSN -> journal. A journal's own ISSNs win over another's ISSN-L."""
        out: dict[str, Journal] = {}
        for j in self.journals:
            for i in j.issns:
                out.setdefault(i, j)
        for j in self.journals:
            for i in j.linking:
                out.setdefault(i, j)
        return out


def load_doaj(csv_path: str | None) -> DOAJ:
    text = read_csv_text(csv_path)
    issnl = load_issnl_table()

    reader = csv.DictReader(io.StringIO(text))
    header = [h.strip() for h in (reader.fieldnames or [])]
    for col in (COL_PISSN, COL_EISSN):
        if col not in header:
            raise SystemExit(f"csv is missing required column {col!r}")
    reader.fieldnames = header

    rows = no_issn = 0
    journals = []
    for row in reader:
        rows += 1
        found = [n for n in (normalize_issn(row.get(c) or "") for c in (COL_PISSN, COL_EISSN)) if n]
        if not found:
            no_issn += 1
            continue
        j = Journal(
            issns=list(dict.fromkeys(found)),
            doaj_id=doaj_journal_id(row.get(COL_DOAJURL) or ""),
            added_on=(row.get(COL_ADDEDON) or "").strip(),
        )
        for i in j.issns:
            if i in issnl and issnl[i] not in j.linking:
                j.linking.append(issnl[i])
        journals.append(j)
    return DOAJ(rows, no_issn, journals)


def http_json(method: str, url: str, body: dict | None = None,
              headers: dict | None = None) -> tuple[int, dict | str]:
    """(status, parsed body). HTTP errors are returned, not raised."""
    h = {"Content-Type": "application/json", **(headers or {})}
    data = json.dumps(body).encode() if body is not None else None
    r = Request(url, method=method, data=data, headers=h)
    try:
        with urlopen(r, timeout=600) as resp:
            raw = resp.read().decode("utf-8", "replace")
            status = resp.status
    except HTTPError as e:
        raw = e.read().decode("utf-8", "replace")
        status = e.code
    except URLError as e:
        return 0, str(e)
    try:
        return status, json.loads(raw)
    except json.JSONDecodeError:
        return status, raw


def es(method: str, url: str, body: dict) -> dict:
    """An ES request that must succeed; anything else ends the script."""
    status, out = http_json(method, url, body)
    if status != 200 or not isinstance(out, dict):
        raise SystemExit(f"{method} {url} -> {status}: {str(out)[:500]}")
    return out


def batches(issns: set[str], size: int) -> list[list[str]]:
    ordered = sorted(issns)
    return [ordered[i:i + size] for i in range(0, len(ordered), size)]


def container_query(batch: list[str], unmarked_only: bool) -> dict:
    """fatcat_container docs carrying any of batch, optionally only those not
    yet in_doaj."""
    q: dict = {"bool": {"filter": [{"terms": {"issns": batch}}]}}
    if unmarked_only:
        q["bool"]["must_not"] = [{"term": {"in_doaj": True}}]
    return q
