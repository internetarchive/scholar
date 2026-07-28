#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.11"
# dependencies = ["plotext>=5.2.8"]
# ///
"""spngraph — live one-hour terminal graph of `./spn status user`.

Polls the SPN user-status endpoint (via the `spn` CLI) on an interval and draws
two colored lines over a rolling one-hour window:

  * available  — capture slots currently free
  * processing — captures in flight

The y-axis is fixed to 0..60. The x-axis covers the last 60 minutes; each
bucket is at least 5 seconds wide, growing to fit when the terminal is narrow.

Run it standalone (uv fetches plotext into an ephemeral env):

    ./spngraph.py                 # live, polling `./spn status user` every 5s
    ./spngraph.py --once          # render a single snapshot and exit
    ./spngraph.py --demo          # render a synthetic hour and exit
    uv run spngraph.py            # equivalent, if not marked executable
"""

from __future__ import annotations

import argparse
import json
import math
import random
import subprocess
import sys
import time
from collections import deque
from dataclasses import dataclass

import plotext as plt

WINDOW_SECONDS = 60 * 60  # one hour
MIN_BUCKET_SECONDS = 5  # finest x resolution
Y_MAX = 60

COLOR_AVAILABLE = 46  # 256-color bright green
COLOR_PROCESSING = 208  # 256-color orange


@dataclass
class Sample:
    ts: float
    available: float
    processing: float


def poll(cmd: str) -> tuple[float, float] | None:
    """Run the status command and return (available, processing), or None on error."""
    try:
        proc = subprocess.run(
            cmd, shell=True, capture_output=True, text=True, timeout=20
        )
    except subprocess.TimeoutExpired:
        return None
    if proc.returncode != 0:
        return None
    try:
        data = json.loads(proc.stdout)
    except json.JSONDecodeError:
        return None
    try:
        return float(data["available"]), float(data["processing"])
    except (KeyError, TypeError, ValueError):
        return None


def plot_width() -> int:
    """Estimate the drawable plot width in columns (terminal minus axis gutter)."""
    try:
        cols = plt.terminal_width()
    except Exception:
        cols = 80
    return max(20, cols - 12)


def bucket_seconds(width: int) -> int:
    """Pick a bucket size: 5s when wide enough, larger (a multiple of 5) when narrow."""
    target_buckets = min(WINDOW_SECONDS // MIN_BUCKET_SECONDS, width)
    target_buckets = max(1, target_buckets)
    raw = WINDOW_SECONDS / target_buckets
    # round up to the next multiple of MIN_BUCKET_SECONDS, never below it
    return max(
        MIN_BUCKET_SECONDS,
        math.ceil(raw / MIN_BUCKET_SECONDS) * MIN_BUCKET_SECONDS,
    )


def bucketize(samples, now: float, bsec: int):
    """Aggregate samples into fixed time buckets ending at `now`.

    Returns (xs, avail, proc, window_start, n_buckets) where xs are the bucket
    indices that actually contain data and the y-lists are per-bucket means.
    """
    n_buckets = math.ceil(WINDOW_SECONDS / bsec)
    window_start = now - n_buckets * bsec

    sums_a = [0.0] * n_buckets
    sums_p = [0.0] * n_buckets
    counts = [0] * n_buckets

    for s in samples:
        if s.ts < window_start:
            continue
        idx = int((s.ts - window_start) // bsec)
        if 0 <= idx < n_buckets:
            sums_a[idx] += s.available
            sums_p[idx] += s.processing
            counts[idx] += 1

    xs, avail, proc = [], [], []
    for i in range(n_buckets):
        if counts[i] == 0:
            continue
        xs.append(i)
        avail.append(sums_a[i] / counts[i])
        proc.append(sums_p[i] / counts[i])

    return xs, avail, proc, window_start, n_buckets


def render(samples, last, bsec: int, status_msg: str) -> None:
    now = time.time()
    xs, avail, proc, window_start, n_buckets = bucketize(samples, now, bsec)

    plt.clear_figure()
    plt.theme("dark")
    plt.ylim(0, Y_MAX)
    plt.yticks(list(range(0, Y_MAX + 1, 10)))
    plt.xlim(0, max(1, n_buckets - 1))

    # x ticks: evenly spaced clock-time labels across the hour
    n_ticks = 6
    tick_pos, tick_lab = [], []
    for k in range(n_ticks + 1):
        idx = round(k * (n_buckets - 1) / n_ticks)
        t = window_start + (idx + 1) * bsec
        tick_pos.append(idx)
        tick_lab.append(time.strftime("%H:%M", time.localtime(t)))
    plt.xticks(tick_pos, tick_lab)

    if xs:
        plt.plot(xs, avail, color=COLOR_AVAILABLE, marker="braille", label="available")
        plt.plot(xs, proc, color=COLOR_PROCESSING, marker="braille", label="processing")

    a = f"{last[0]:.0f}" if last else "--"
    p = f"{last[1]:.0f}" if last else "--"
    plt.title(f"SPN user status   available {a}   processing {p}")
    bucket_label = f"{bsec}s buckets" if bsec < 60 else f"{bsec // 60}m buckets"
    sub = f"last hour | {bucket_label}"
    if status_msg:
        sub += f" | {status_msg}"
    plt.xlabel(sub)

    plt.clear_terminal()
    plt.show()


def synthetic_window(now: float) -> "deque[Sample]":
    """Build a full synthetic hour of samples for --demo / testing."""
    s: deque[Sample] = deque()
    n = WINDOW_SECONDS // MIN_BUCKET_SECONDS
    for i in range(n):
        ts = now - WINDOW_SECONDS + i * MIN_BUCKET_SECONDS
        proc = 30 + 25 * math.sin(i / 40) + random.uniform(-4, 4)
        proc = max(0, min(Y_MAX, proc))
        avail = max(0, min(Y_MAX, Y_MAX - proc + random.uniform(-3, 3)))
        s.append(Sample(ts, avail, proc))
    return s


def main() -> int:
    ap = argparse.ArgumentParser(
        description="Live one-hour graph of `spn status user`."
    )
    ap.add_argument(
        "--cmd",
        default="./spn status user",
        help="shell command that prints the user-status JSON (default: %(default)s)",
    )
    ap.add_argument(
        "--interval",
        type=float,
        default=MIN_BUCKET_SECONDS,
        help="seconds between polls (default: %(default)s)",
    )
    ap.add_argument(
        "--once", action="store_true", help="render a single snapshot and exit"
    )
    ap.add_argument(
        "--demo",
        action="store_true",
        help="render a synthetic hour of data and exit (no polling)",
    )
    args = ap.parse_args()

    if args.demo:
        now = time.time()
        samples = synthetic_window(now)
        last = (samples[-1].available, samples[-1].processing)
        render(samples, last, bucket_seconds(plot_width()), "demo")
        return 0

    samples: deque[Sample] = deque()
    last = None
    status_msg = ""

    try:
        while True:
            result = poll(args.cmd)
            now = time.time()
            if result is None:
                status_msg = time.strftime("poll failed @ %H:%M:%S", time.localtime(now))
            else:
                last = result
                status_msg = ""
                samples.append(Sample(now, result[0], result[1]))

            cutoff = now - WINDOW_SECONDS
            while samples and samples[0].ts < cutoff:
                samples.popleft()

            render(samples, last, bucket_seconds(plot_width()), status_msg)

            if args.once:
                return 0
            time.sleep(args.interval)
    except KeyboardInterrupt:
        print()
        return 0


if __name__ == "__main__":
    sys.exit(main())
