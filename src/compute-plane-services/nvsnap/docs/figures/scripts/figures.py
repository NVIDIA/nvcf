#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Generate nvsnap's architecture figures as SVG, one light and one dark file each.

Figures are drawn from code (boxes, arrows, labels) rather than by hand, so a
change is a reviewable diff of this script. Each figure is written to
``docs/figures/<name>-light.svg`` and ``<name>-dark.svg``; a document shows
the one that matches the reader's theme with a ``<picture>`` element.

Usage: ``docs/figures/scripts/figures.py [--check]``; ``--check`` writes
nothing and exits 1 when a committed figure differs from what this script
draws.
"""
from __future__ import annotations

import pathlib
import sys
import xml.etree.ElementTree as ET
from xml.sax.saxutils import escape

OUT = pathlib.Path(__file__).resolve().parents[1]

THEMES = {
    "light": {"bg": "#ffffff", "fg": "#1b1f24", "muted": "#57606a", "box": "#ffffff",
              "line": "#8c959f", "accent": "#76b900", "accent_fill": "#f3fae8",
              "warn": "#bf3989", "lane": "#f6f8fa"},
    "dark": {"bg": "#0d1117", "fg": "#e6edf3", "muted": "#9198a1", "box": "#161b22",
             "line": "#6e7681", "accent": "#76b900", "accent_fill": "#1c2a10",
             "warn": "#ff7bc5", "lane": "#151b23"},
}
FONT = 'font-family="Arial, Helvetica, sans-serif"'


class Fig:
    """A figure drawn once per theme: every element is a function of the palette."""

    def __init__(self, name: str, w: int, h: int, title: str, desc: str):
        self.name, self.w, self.h, self.title, self.desc = name, w, h, title, desc
        self.items: list = []

    def lane(self, x, y, w, h, label):
        self.items.append(lambda t: (
            f'<rect x="{x}" y="{y}" width="{w}" height="{h}" rx="10" fill="{t["lane"]}" stroke="{t["line"]}" stroke-width="1"/>'
            f'<text x="{x + 12}" y="{y + 20}" font-size="12" font-weight="bold" fill="{t["muted"]}" {FONT}>{escape(label)}</text>'))

    def box(self, x, y, w, h, lines, accent=False, warn=False):
        def draw(t):
            stroke = t["warn"] if warn else (t["accent"] if accent else t["line"])
            fill = t["accent_fill"] if accent else t["box"]
            out = [f'<rect x="{x}" y="{y}" width="{w}" height="{h}" rx="8" fill="{fill}" stroke="{stroke}" stroke-width="1.5"/>']
            top = y + h / 2 - (len(lines) - 1) * 8 + 4
            for i, line in enumerate(lines):
                weight = ' font-weight="bold"' if i == 0 else ""
                size = 13 if i == 0 else 11
                colour = t["fg"] if i == 0 else t["muted"]
                out.append(f'<text x="{x + w / 2}" y="{top + i * 16}" font-size="{size}"{weight} fill="{colour}" '
                           f'text-anchor="middle" {FONT}>{escape(line)}</text>')
            return "".join(out)
        self.items.append(draw)

    def arrow(self, points, label=None, dashed=False, warn=False, label_at=None):
        def draw(t):
            colour = t["warn"] if warn else t["fg"]
            d = "M" + " L".join(f"{px} {py}" for px, py in points)
            dash = ' stroke-dasharray="6 4"' if dashed else ""
            marker = "arrow-warn" if warn else "arrow"
            out = f'<path d="{d}" fill="none" stroke="{colour}" stroke-width="1.5"{dash} marker-end="url(#{marker})"/>'
            if label:
                lx, ly = label_at or ((points[0][0] + points[-1][0]) / 2, (points[0][1] + points[-1][1]) / 2 - 6)
                out += (f'<text x="{lx}" y="{ly}" font-size="11" fill="{t["muted"]}" text-anchor="middle" '
                        f'{FONT}>{escape(label)}</text>')
            return out
        self.items.append(draw)

    def line(self, points):
        self.items.append(lambda t: ('<path d="M' + " L".join(f"{px} {py}" for px, py in points) +
                                     f'" fill="none" stroke="{t["accent"]}" stroke-width="2"/>'))

    def text(self, x, y, s, anchor="start", muted=True, size=11):
        self.items.append(lambda t: (f'<text x="{x}" y="{y}" font-size="{size}" fill="{t["muted"] if muted else t["fg"]}" '
                                     f'text-anchor="{anchor}" {FONT}>{escape(s)}</text>'))

    def render(self, theme: str) -> str:
        t = THEMES[theme]
        head = (f'<svg xmlns="http://www.w3.org/2000/svg" width="{self.w}" height="{self.h}" '
                f'viewBox="0 0 {self.w} {self.h}" role="img" aria-labelledby="title desc">'
                f'<title id="title">{escape(self.title)}</title><desc id="desc">{escape(self.desc)}</desc>'
                '<defs>'
                f'<marker id="arrow" markerWidth="8" markerHeight="8" refX="7" refY="3" orient="auto"><path d="M0 0L0 6L7 3Z" fill="{t["fg"]}"/></marker>'
                f'<marker id="arrow-warn" markerWidth="8" markerHeight="8" refX="7" refY="3" orient="auto"><path d="M0 0L0 6L7 3Z" fill="{t["warn"]}"/></marker>'
                '</defs>'
                f'<rect width="{self.w}" height="{self.h}" fill="{t["bg"]}"/>'
                f'<text x="24" y="34" font-size="18" font-weight="bold" fill="{t["fg"]}" {FONT}>{escape(self.title)}</text>')
        body = "\n".join(draw(t) for draw in self.items)
        return head + "\n" + body + "\n</svg>\n"


def instance_restore() -> Fig:
    f = Fig("instance-capture-restore", 980, 470, "Instance capture and restore",
            "Rank 0's agent captures every member of an opted-in instance at one freeze point; "
            "a later instance's pods become placeholders that the leader's agent restores together.")
    f.lane(20, 54, 940, 170, "Capture (internal/agent/criu_group.go, group_freeze.go)")
    f.box(40, 90, 170, 110, ["Ready instance", "ranks + companions", "opted in"])
    f.box(250, 90, 190, 110, ["Checks", "engine whole, probes,", "agents settled, preflight"])
    f.box(480, 90, 200, 110, ["Freeze point", "all members stop", "together, or all abort"], accent=True)
    f.box(720, 90, 220, 110, ["Each node dumps its member", "GPU memory, CRIU images,", "rootfs diff, group record"])
    f.arrow([(210, 145), (248, 145)])
    f.arrow([(440, 145), (478, 145)])
    f.arrow([(680, 145), (718, 145)])
    f.lane(20, 244, 940, 206, "Restore (internal/webhook/criu_group.go, internal/agent/gpushare_group_restore.go)")
    f.box(40, 280, 170, 110, ["New instance", "same configuration", "and size"])
    f.box(250, 280, 190, 110, ["Placeholders", "one per slot, admitted", "by the webhook"])
    f.box(480, 280, 200, 110, ["CRIU restore", "one address map,", "connections locked"], accent=True)
    f.box(720, 280, 220, 110, ["Unlock all, then resume", "GPUs with the handle", "exchange; companions last"])
    f.arrow([(210, 335), (248, 335)])
    f.arrow([(440, 335), (478, 335)])
    f.arrow([(680, 335), (718, 335)])
    f.arrow([(830, 200), (830, 240), (345, 240), (345, 278)], label="checkpoints fetched to each node", label_at=(590, 234))
    f.text(250, 420, "A member that cannot restore with its instance starts the instance cold; a failed restore blocks the checkpoints.")
    return f


def data_tiers() -> Fig:
    f = Fig("data-tiers", 980, 360, "Where a checkpoint lives",
            "A capture writes to its node's disk and copies to cluster storage in the background; "
            "a restore takes the first copy it finds: L1, L2, L3, then L4.")
    f.box(40, 70, 200, 90, ["Capture", "on the source node"], accent=True)
    f.box(300, 70, 180, 90, ["L1: node disk", "written first"])
    f.box(570, 70, 160, 90, ["rwx-<hash>", "written once"])
    f.box(810, 70, 150, 90, ["rox-<hash>", "read-only twin"])
    f.arrow([(240, 115), (298, 115)])
    f.arrow([(480, 115), (568, 115)], label="background", label_at=(524, 105))
    f.arrow([(730, 115), (808, 115)], label="promote", label_at=(769, 105))
    f.text(570, 180, "L2: cluster storage (or a shared filesystem)")
    f.box(40, 220, 200, 100, ["Restore", "EnsureLocal", "(cascade_fetch.go)"], accent=True)
    f.box(300, 220, 140, 100, ["1. L1", "this node"])
    f.box(470, 220, 140, 100, ["2. L2", "shared fs, then", "the rox volume"])
    f.box(640, 220, 140, 100, ["3. L3", "peer agents,", "least loaded"])
    f.box(810, 220, 150, 100, ["4. L4", "object store,", "cross-cluster"])
    f.arrow([(240, 270), (298, 270)])
    f.arrow([(440, 270), (468, 270)], label="miss", label_at=(454, 262))
    f.arrow([(610, 270), (638, 270)], label="miss", label_at=(624, 262))
    f.arrow([(780, 270), (808, 270)], label="miss", label_at=(794, 262))
    f.text(300, 344, "A node that fetches a checkpoint registers as a peer for it, so every restore adds a source.")
    return f


def group_freeze() -> Fig:
    f = Fig("group-freeze", 980, 420, "The group freeze",
            "Every member waits at the freeze point; the leader releases them together once all arrive, "
            "and aborts them all if one ends first or does not take its release. The same happens after the dump.")
    xs = [140, 340, 540]
    for i, x in enumerate(xs):
        f.box(x - 70, 60, 140, 46, [f"member {i}", "own node's agent"])
        f.arrow([(x, 106), (x, 148)], label="prepares", label_at=(x + 40, 132))
        f.box(x - 70, 150, 140, 40, ["waits at the point"], accent=True)
        f.arrow([(x, 190), (x, 248)])
        f.box(x - 70, 250, 140, 40, ["dump", "then wait (post)"])
        f.arrow([(x, 290), (x, 338)])
        f.box(x - 70, 340, 140, 40, ["capture ends"])
    f.box(700, 140, 260, 60, ["Leader (driveGroupFreeze)", "all arrived: release all"], accent=True)
    f.line([(210, 170), (270, 170)])
    f.line([(410, 170), (470, 170)])
    f.arrow([(700, 170), (612, 170)])
    f.box(700, 230, 260, 76, ["Abort all", "a member ended first, a release", "was not taken, or time ran out"], warn=True)
    f.arrow([(700, 268), (612, 268)], warn=True, dashed=True)
    f.text(700, 340, "internal/agent/group_freeze.go")
    f.text(700, 356, "Phases: before the dump, and \"post\" after it.")
    return f


FIGURES = [instance_restore, data_tiers, group_freeze]


def main(argv: list[str]) -> int:
    check = "--check" in argv
    stale = []
    for build in FIGURES:
        fig = build()
        for theme in THEMES:
            path = OUT / f"{fig.name}-{theme}.svg"
            svg = fig.render(theme)
            ET.fromstring(svg)  # well-formed, or fail here
            if check:
                if not path.exists() or path.read_text(encoding="utf-8") != svg:
                    stale.append(str(path.relative_to(OUT.parents[1])))
            else:
                path.write_text(svg, encoding="utf-8")
                print(f"wrote {path.relative_to(OUT.parents[1])}")
    if stale:
        print("stale figures (run docs/figures/scripts/figures.py):\n  " + "\n  ".join(stale), file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
