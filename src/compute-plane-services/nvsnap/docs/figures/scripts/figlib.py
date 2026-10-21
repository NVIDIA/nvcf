# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Shared helpers for nvsnap's architecture figures.

Style: NVIDIA Cloud Architecture Diagrams Style Guide v1.1, as summarized in
.agents/skills/nvaf-architecture-diagrams/references/nvidia-cloud-architecture-style.md
(the skill is copied unchanged from the nvaf repository, as flashbox and
bedrock do). Icons are embedded byte-identical as data-URI symbols from
docs/figures/assets/nvsnap/ (provenance in docs/figures/README.md); every
symbol records its source in a ``data-source`` attribute, so GitHub and
GitLab render the SVG without loading anything external.

Units: one SVG unit is one point. Paths are 1 pt black (primary) or 1 pt
dashed #999999 (optional, fallback or failure); groups are #cccccc, NVIDIA
groups #76b900; the canvas is white. Every figure uses one icon size (48, the
guide's small preset).

A figure script writes ``docs/figures/<name>.svg``. With NVSNAP_FIGURES_OUT
set, it writes there instead, which is how ``check.py`` compares a fresh
render with the committed SVG.
"""
import base64
import os
import pathlib
import textwrap
import xml.etree.ElementTree as ET
from xml.sax.saxutils import escape as esc

ROOT = pathlib.Path(__file__).resolve().parents[1]
ASSETS = ROOT / "assets" / "nvsnap"
ICON = 48

ICONS = {  # symbol id -> file in assets/nvsnap/
    "icon-actor": "actor-machine-service.png",     # Actor: a calling system (NVCA, a controller)
    "icon-user": "actor-generic-user.png",         # Actor: a person (operator)
    "icon-service": "service-nvidia-generic.png",  # NVIDIA service: nvsnap's agent, webhook, server
    "icon-third": "third-party-generic.png",       # Third-party service: Kubernetes API, CRIU, CSI
    "icon-db": "data-database.png",                # Data: the catalog, records
    "icon-data": "data-generic.png",               # Data: a checkpoint, a chunk store, a volume
    "icon-server": "resource-server.png",          # Resource: a node
    "icon-compute": "resource-compute.png",        # Resource: a pod or process
    "icon-hardware": "resource-hardware.png",      # Resource: a GPU, a disk
    "icon-guard": "resource-guardrails.png",       # Resource: a check or boundary
    "icon-folder": "resource-folder.png",          # Resource: a directory
    "icon-admit": "function-admission.png",        # Function: admission (the webhook's mutation)
    "icon-run": "function-run-command.png",        # Function: a tool run (nvsnap-gpu-suspend)
}

STYLE = (
    "<style>"
    'text{font-family:"NVIDIA Sans",Arial,Helvetica,sans-serif;fill:#000000}'
    ".title{font-size:20px;font-weight:600}.sub{font-size:12px;fill:#333333}"
    ".group{fill:#ffffff;stroke:#cccccc;stroke-width:1}"
    ".group-nv{fill:#ffffff;stroke:#76b900;stroke-width:1.5}"
    ".group-partner{fill:#f4f7f7;stroke:#cccccc;stroke-width:1}"
    ".group-label{font-size:12px;font-weight:600}"
    ".card{fill:#ffffff;stroke:#cccccc;stroke-width:1}"
    ".label{font-size:12px}.small{font-size:10px;fill:#333333}"
    ".plabel{font-size:10px;font-weight:500}"
    ".path{fill:none;stroke:#000000;stroke-width:1;stroke-linejoin:round}"
    ".opt{fill:none;stroke:#999999;stroke-width:1;stroke-dasharray:5 3;stroke-linejoin:round}"
    ".callout{fill:#000000}.callout-text{font-size:11px;font-weight:600;fill:#ffffff}"
    ".note{font-size:11px;fill:#333333}.note-b{font-size:11px;font-weight:600}"
    "</style>"
)


class Fig:
    def __init__(self, name, w, h, title, desc, subtitle=None):
        self.name, self.w, self.h = name, w, h
        self.used = set()
        self.body = []
        self.title, self.desc = title, desc
        self.body.append(f'<text x="40" y="44" class="title">{esc(title)}</text>')
        if subtitle:
            self.body.append(f'<text x="40" y="64" class="sub">{esc(subtitle)}</text>')

    # ---- primitives -------------------------------------------------------
    def raw(self, s):
        self.body.append(s)

    def group(self, x, y, w, h, label, sub=None, kind="group", anchor="start"):
        """kind: group (generic), nv (NVIDIA service group), partner (third-party group)."""
        cls = {"group": "group", "nv": "group-nv", "partner": "group-partner"}[kind]
        self.body.append(f'<rect x="{x}" y="{y}" width="{w}" height="{h}" rx="10" class="{cls}"/>')
        tx = x + 14 if anchor == "start" else x + w - 14
        self.body.append(f'<text x="{tx}" y="{y + 22}" class="group-label" text-anchor="{anchor}">{esc(label)}</text>')
        if sub:
            self.body.append(f'<text x="{tx}" y="{y + 37}" class="small" text-anchor="{anchor}">{esc(sub)}</text>')

    def card(self, cid, x, y, w, h, sym, label, sub=None, sub2=None, nv=False):
        """A component: icon on top, label, up to two short detail lines."""
        self.used.add(sym)
        stroke = ' style="stroke:#76b900;stroke-width:1.5"' if nv else ""
        self.body.append(f'<rect id="{cid}" x="{x}" y="{y}" width="{w}" height="{h}" rx="8" class="card"{stroke}/>')
        self.body.append(f'<use href="#{sym}" x="{x + (w - ICON) / 2:g}" y="{y + 8}" width="{ICON}" height="{ICON}"/>')
        ty = y + 8 + ICON + 16
        self.body.append(f'<text x="{x + w / 2:g}" y="{ty}" class="label" text-anchor="middle" data-fit="{cid}" data-fit-padding="4">{esc(label)}</text>')
        for i, t in enumerate([t for t in (sub, sub2) if t]):
            self.body.append(f'<text x="{x + w / 2:g}" y="{ty + 15 + 13 * i}" class="small" text-anchor="middle" data-fit="{cid}" data-fit-padding="4">{esc(t)}</text>')

    def hcard(self, cid, x, y, w, h, sym, label, sub=None, sub2=None, nv=False):
        """A compact component: icon on the left, label and up to two detail lines to its right."""
        self.used.add(sym)
        stroke = ' style="stroke:#76b900;stroke-width:1.5"' if nv else ""
        self.body.append(f'<rect id="{cid}" x="{x}" y="{y}" width="{w}" height="{h}" rx="8" class="card"{stroke}/>')
        self.body.append(f'<use href="#{sym}" x="{x + 10}" y="{y + (h - ICON) / 2:g}" width="{ICON}" height="{ICON}"/>')
        tx = x + ICON + 20
        lines = [(label, "label")] + [(t, "small") for t in (sub, sub2) if t]
        top = y + h / 2 - (len(lines) - 1) * 7 + 4
        for i, (t, cls) in enumerate(lines):
            self.body.append(f'<text x="{tx}" y="{top + 14 * i:g}" class="{cls}" data-fit="{cid}" data-fit-padding="4">{esc(t)}</text>')

    def tcard(self, cid, x, y, w, h, label, sub=None, sub2=None, nv=False):
        """A card without an icon, for a step or an outcome: label and up to two detail lines, left-aligned."""
        stroke = ' style="stroke:#76b900;stroke-width:1.5"' if nv else ""
        self.body.append(f'<rect id="{cid}" x="{x}" y="{y}" width="{w}" height="{h}" rx="8" class="card"{stroke}/>')
        lines = [(label, "label")] + [(t, "small") for t in (sub, sub2) if t]
        top = y + h / 2 - (len(lines) - 1) * 7 + 4
        for i, (t, cls) in enumerate(lines):
            self.body.append(f'<text x="{x + 14}" y="{top + 14 * i:g}" class="{cls}" data-fit="{cid}" data-fit-padding="4">{esc(t)}</text>')

    def path(self, d, opt=False):
        cls, mk = ("opt", "arrow-opt") if opt else ("path", "arrow")
        self.body.append(f'<path class="{cls}" d="{d}" marker-end="url(#{mk})"/>')

    def line(self, d, opt=False):
        """A connector with no arrowhead."""
        self.body.append(f'<path class="{"opt" if opt else "path"}" d="{d}"/>')

    def plabel(self, x, y, text, anchor="middle"):
        """A path label on a straight segment, on a white backing so the line does not cross it."""
        wch = len(text) * 5.4 + 8
        rx = x - wch / 2 if anchor == "middle" else (x - 4 if anchor == "start" else x - wch + 4)
        self.body.append(f'<rect x="{rx:g}" y="{y - 10}" width="{wch:g}" height="14" fill="#ffffff"/>')
        self.body.append(f'<text x="{x}" y="{y}" class="plabel" text-anchor="{anchor}">{esc(text)}</text>')

    def callout(self, n, x, y):
        self.body.append(
            f'<circle cx="{x}" cy="{y}" r="9" class="callout"/>'
            f'<text x="{x}" y="{y + 4}" class="callout-text" text-anchor="middle">{n}</text>'
        )

    def note(self, x, y, text, cls="note", anchor="start"):
        self.body.append(f'<text x="{x}" y="{y}" class="{cls}" text-anchor="{anchor}">{esc(text)}</text>')

    def legend(self, x, y, items, width=None, line_h=15):
        """Numbered notes under the figure, wrapped to the canvas width. Returns the next free y."""
        width = width or (self.w - x - 40)
        chars = int(width / 5.9)
        for n, text in items:
            lines = textwrap.wrap(text, chars - 4)
            self.callout(n, x + 9, y - 4)
            for i, ln in enumerate(lines):
                self.note(x + 26, y + i * line_h, ln)
            y += line_h * len(lines) + 6
        return y

    # ---- output -----------------------------------------------------------
    def write(self, height=None):
        """height: the final canvas height, e.g. the y returned by legend() plus a margin."""
        if height:
            self.h = int(height)
        defs = ["<defs>"]
        for sym in sorted(self.used):
            f = ICONS[sym]
            b = base64.b64encode((ASSETS / f).read_bytes()).decode()
            defs.append(
                f'<symbol id="{sym}" viewBox="0 0 192 192"><image width="192" height="192" '
                f'href="data:image/png;base64,{b}" data-source="docs/figures/assets/nvsnap/{f}"/></symbol>'
            )
        # Arrowheads: tip at the path end point, 6 long x 5 wide, sized in user units.
        defs.append('<marker id="arrow" markerUnits="userSpaceOnUse" markerWidth="6" markerHeight="5" refX="6" refY="2.5" orient="auto"><path d="M0 0L6 2.5L0 5Z" fill="#000000"/></marker>')
        defs.append('<marker id="arrow-opt" markerUnits="userSpaceOnUse" markerWidth="6" markerHeight="5" refX="6" refY="2.5" orient="auto"><path d="M0 0L6 2.5L0 5Z" fill="#999999"/></marker>')
        defs.append(STYLE + "</defs>")
        w, h = self.w, self.h
        head = [
            f'<svg xmlns="http://www.w3.org/2000/svg" width="{w}" height="{h}" '
            f'viewBox="0 0 {w} {h}" role="img" aria-labelledby="title desc">',
            f'<title id="title">{esc(self.title)}</title>',
            f'<desc id="desc">{esc(self.desc)}</desc>',
        ]
        bg = [f'<rect x="0" y="0" width="{w}" height="{h}" fill="#ffffff"/>']
        out = head + defs + bg + self.body + ["</svg>"]
        outdir = pathlib.Path(os.environ.get("NVSNAP_FIGURES_OUT", ROOT))
        p = outdir / f"{self.name}.svg"
        p.write_text("\n".join(out) + "\n")
        ET.parse(p)
        print(p, p.stat().st_size, "bytes, xml ok")
