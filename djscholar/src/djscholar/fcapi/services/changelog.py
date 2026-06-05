"""Changelog queries — recently created/updated entities."""

import datetime
from uuid import UUID

from django.db.models import Prefetch, Q

from djscholar.fcapi import models as m


ENTITY_MODELS = {
    "releases": m.Release,
    "containers": m.Container,
    "creators": m.Creator,
    "files": m.File,
    "works": m.Work,
    "filesets": m.Fileset,
    "webcaptures": m.Webcapture,
}


def recent(
    entity_type: str,
    date: datetime.date,
    window: datetime.timedelta = datetime.timedelta(days=1),
    limit: int = 50,
    offset: int = 0,
    source: str | None = None,
) -> list[m.Entity]:
    """Return entities updated within a window ending at date+1day, newest first.

    The window is subtracted from the start date to determine the range.
    For a single day's changes, use the default window of 1 day.
    """
    model = ENTITY_MODELS.get(entity_type)
    if model is None:
        raise ValueError(f"unknown entity type: {entity_type}")

    start_dt = datetime.datetime.combine(
        date, datetime.time(), tzinfo=datetime.timezone.utc)
    end_dt = start_dt + datetime.timedelta(days=1)
    begin_dt = start_dt - window

    qs = model.objects.filter(updated__range=[begin_dt, end_dt]).order_by("-updated")

    if source:
        qs = qs.filter(source=source)

    if model is m.File:
        qs = qs.prefetch_related(
            Prefetch("releases", queryset=m.Release.objects.only("id", "title"))
        )

    return list(qs[offset:offset + limit])


def recent_page(
    entity_type: str,
    date: datetime.date,
    window: datetime.timedelta = datetime.timedelta(0),
    limit: int = 50,
    source: str | None = None,
    cursor: tuple[datetime.datetime, UUID] | None = None,
    direction: str = "older",
) -> tuple[list[m.Entity], bool]:
    """Return (entities, has_more) updated within a window, newest first.

    Keyset pagination over (updated, id). Pass a boundary row's (updated, id)
    as ``cursor``: ``direction="older"`` returns the page just older than the
    cursor, ``direction="newer"`` the page just newer (already re-sorted to
    newest-first). ``has_more`` reports whether a further page exists in the
    direction travelled.

    Unlike a COUNT + OFFSET scheme this issues a single query whose cost is
    independent of how deep into the day's changes the user has paged, at the
    expense of knowing the exact total. The default ``window`` of 0 means the
    query covers exactly one calendar day.
    """
    model = ENTITY_MODELS.get(entity_type)
    if model is None:
        raise ValueError(f"unknown entity type: {entity_type}")

    start_dt = datetime.datetime.combine(
        date, datetime.time(), tzinfo=datetime.timezone.utc)
    end_dt = start_dt + datetime.timedelta(days=1)
    begin_dt = start_dt - window

    qs = model.objects.filter(updated__range=[begin_dt, end_dt])
    if source:
        qs = qs.filter(source=source)
    if model is m.File:
        qs = qs.prefetch_related(
            Prefetch("releases", queryset=m.Release.objects.only("id", "title"))
        )

    if direction == "newer" and cursor is not None:
        cur_updated, cur_id = cursor
        qs = qs.filter(
            Q(updated__gt=cur_updated) | Q(updated=cur_updated, id__gt=cur_id)
        ).order_by("updated", "id")
    else:
        if cursor is not None:
            cur_updated, cur_id = cursor
            qs = qs.filter(
                Q(updated__lt=cur_updated) | Q(updated=cur_updated, id__lt=cur_id)
            )
        qs = qs.order_by("-updated", "-id")

    rows = list(qs[:limit + 1])
    has_more = len(rows) > limit
    rows = rows[:limit]
    if direction == "newer":
        rows.reverse()
    return rows, has_more
