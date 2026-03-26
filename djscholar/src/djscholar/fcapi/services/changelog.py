"""Changelog queries — recently created/updated entities."""

import datetime
from uuid import UUID

from django.db.models import QuerySet

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
    limit: int = 100,
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

    return list(
        model.objects.filter(updated__range=[begin_dt, end_dt])
        .order_by("-updated")[:limit]
    )
