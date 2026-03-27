"""Pygal chart helpers for coverage visualizations."""

from typing import Any

import pygal
from pygal.style import CleanStyle


def preservation_by_year_histogram(rows: list[dict[str, Any]]) -> str:
    """Render a stacked-bar SVG (data URI) of preservation by year."""
    years = sorted(rows, key=lambda x: x["year"])

    CleanStyle.colors = ("red", "darkolivegreen", "limegreen")
    label_count = len(years)
    if label_count > 30:
        label_count = 10
    chart = pygal.StackedBar(
        dynamic_print_values=True,
        style=CleanStyle,
        width=1000,
        height=500,
        x_labels_major_count=label_count,
        show_minor_x_labels=False,
        x_label_rotation=20,
    )
    chart.x_title = "Year"
    chart.x_labels = [str(y["year"]) for y in years]
    chart.add("None", [y["none"] for y in years])
    chart.add("Dark", [y["dark"] for y in years])
    chart.add("Bright", [y["bright"] for y in years])
    return chart.render_data_uri()


def preservation_by_date_histogram(rows: list[dict[str, Any]]) -> str:
    """Render a stacked-bar SVG (data URI) of preservation by date."""
    dates = sorted(rows, key=lambda x: x["date"])

    CleanStyle.colors = ("red", "darkolivegreen", "limegreen")
    label_count = len(dates)
    if label_count > 30:
        label_count = 10
    chart = pygal.StackedBar(
        dynamic_print_values=True,
        style=CleanStyle,
        width=1000,
        height=500,
        x_labels_major_count=label_count,
        show_minor_x_labels=False,
        x_label_rotation=20,
    )
    chart.x_title = "Date"
    chart.x_labels = [str(d["date"]) for d in dates]
    chart.add("None", [d["none"] for d in dates])
    chart.add("Dark", [d["dark"] for d in dates])
    chart.add("Bright", [d["bright"] for d in dates])
    return chart.render_data_uri()


def preservation_by_volume_histogram(rows: list[dict[str, Any]]) -> str:
    """Render a stacked-bar SVG (data URI) of preservation by volume number."""
    volumes = sorted(rows, key=lambda x: int(x["volume"]))

    CleanStyle.colors = ("red", "darkolivegreen", "limegreen")
    label_count = len(volumes)
    if label_count > 30:
        label_count = 10
    chart = pygal.StackedBar(
        dynamic_print_values=True,
        style=CleanStyle,
        width=1000,
        height=500,
        x_labels_major_count=label_count,
        show_minor_x_labels=False,
        x_label_rotation=20,
    )
    chart.x_title = "Volume"
    chart.x_labels = [str(v["volume"]) for v in volumes]
    chart.add("None", [v["none"] for v in volumes])
    chart.add("Dark", [v["dark"] for v in volumes])
    chart.add("Bright", [v["bright"] for v in volumes])
    return chart.render_data_uri()
