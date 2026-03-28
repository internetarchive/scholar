"""CSL-JSON and citation rendering for releases."""

import json
from typing import Any
from uuid import UUID

from citeproc import (
    Citation,
    CitationItem,
    CitationStylesBibliography,
    CitationStylesStyle,
    formatter,
)
from citeproc.source.json import CiteProcJSON
from citeproc_styles import get_style_filepath


def _contribs_by_role(contribs: list[dict[str, Any]], role: str) -> list[dict[str, Any]]:
    ret = []
    for c in contribs:
        if c.get("role") == role:
            out = {k: v for k, v in c.items() if k != "role" and k != "literal" and v}
            ret.append(out)
    return ret


def release_to_csl(release, contribs, extids: dict[str, str],
                    container=None) -> dict[str, Any]:
    """Convert a release + related data to a CSL-JSON dict.

    `release` is a Release model instance.
    `contribs` is an iterable of ReleaseContrib (with .creator prefetched).
    `extids` is a {type: value} dict.
    `container` is a Container model instance or None.
    """
    csl_contribs = []
    for contrib in contribs:
        if contrib.creator:
            family = (
                contrib.creator.surname
                or contrib.surname
                or (contrib.raw_name and contrib.raw_name.split()[-1])
            )
            if not family:
                continue
            c = {
                "family": family,
                "given": contrib.creator.given_name or contrib.given_name,
                "literal": contrib.creator.display_name or contrib.raw_name,
                "role": contrib.role or "author",
            }
        else:
            family = contrib.surname or (contrib.raw_name and contrib.raw_name.split()[-1])
            if not family:
                continue
            c = {
                "family": family,
                "given": contrib.given_name,
                "literal": contrib.raw_name,
                "role": contrib.role or "author",
            }
        csl_contribs.append({k: v for k, v in c.items() if v})

    if not csl_contribs:
        raise ValueError("citeproc requires at least one author with a surname")

    abstract = None
    if hasattr(release, "abstracts"):
        first = release.abstracts.first()
        if first:
            abstract = first.content

    issued_date = None
    if release.release_date:
        issued_date = {
            "date-parts": [[
                release.release_date.year,
                release.release_date.month,
                release.release_date.day,
            ]]
        }
    elif release.release_year:
        issued_date = {"date-parts": [[release.release_year]]}

    csl: dict[str, Any] = {
        "type": release.release_type or "article",
        "language": release.language,
        "issued": issued_date,
        "abstract": abstract,
        "container-title": container.name if container else None,
        "DOI": extids.get("doi"),
        "ISBN": extids.get("isbn13"),
        "ISSN": container.issnl if container else None,
        "issue": release.issue,
        "page-first": release.pages.split("-")[0] if release.pages else None,
        "PMCID": extids.get("pmcid"),
        "PMID": extids.get("pmid"),
        "publisher": (container.publisher if container else None) or release.publisher,
        "title": release.title,
        "volume": release.volume,
    }

    for role in [
        "author", "collection-editor", "composer", "container-author",
        "director", "editor", "editorial-director", "interviewer",
        "illustrator", "original-author", "recipient", "reviewed-author",
        "translator",
    ]:
        cbr = _contribs_by_role(csl_contribs, role)
        if cbr:
            csl[role] = cbr

    # remove empty keys
    csl = {k: v for k, v in csl.items() if v}
    return csl


def citeproc_csl(csl_json: dict[str, Any], style: str) -> str:
    """Render a CSL-JSON dict to a formatted citation string."""
    if not csl_json.get("id"):
        csl_json["id"] = "unknown"
    if style == "csl-json":
        return json.dumps(csl_json)
    bib_src = CiteProcJSON([csl_json])
    form = formatter.plain
    style_path = get_style_filepath(style)
    bib_style = CitationStylesStyle(style_path, validate=False)
    bib = CitationStylesBibliography(bib_style, bib_src, form)
    bib.register(Citation([CitationItem(csl_json["id"])]))
    lines = bib.bibliography()[0]
    if style == "bibtex":
        out = ""
        for line in lines:
            if line.startswith(" @"):
                out += "@"
            elif line.startswith(" "):
                out += "\n " + line
            else:
                out += line
        return "".join(out)
    else:
        return "".join(lines)
