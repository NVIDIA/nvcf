# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

"""Shared helpers for nvstor architecture figures.

Style: white canvas, 1 pt black primary paths and 1 pt dashed #999999
optional or fallback paths, numbered callouts on straight segments, groups
(#cccccc outline; green #76b900 for an nvstor process or pod) only around
several components, one icon size (48), colours only by category.

Icons are original vector drawings defined in this file (ICON_GLYPHS): a
coloured disc for the category with a simple glyph on it. Each SVG carries
the icons it uses as <symbol> elements, so it renders on GitHub or
any browser without loading external files and with no raster images.

Units: one SVG unit is treated as one point.
"""
import pathlib
import textwrap
import xml.etree.ElementTree as ET
from xml.sax.saxutils import escape as esc

ROOT = pathlib.Path(__file__).resolve().parents[1]
ICON = 48

# Category colours: (disc fill, glyph stroke).
CATEGORY = {
    "service": ("#76b900", "#ffffff"),   # nvstor service or module
    "third":   ("#d0453f", "#ffffff"),   # third-party software
    "actor":   ("#2f78c4", "#ffffff"),   # a client host, program or person
    "data":    ("#e07b1f", "#ffffff"),   # stored data
    "resource": ("#7d55c0", "#ffffff"),  # nodes, hardware, directories, boundaries
    "function": ("#f2c230", "#1a1a1a"),  # admission control, dispatchers, workers
}

# Symbol id -> (category, glyph). Glyphs are drawn in a 48 x 48 box centred
# on (24, 24), inside a disc of radius 23; strokes inherit the glyph colour.
ICON_GLYPHS = {
    # A client host or pod: a monitor on a stand.
    "icon-actor": ("actor",
        '<rect x="13" y="14" width="22" height="15" rx="2"/>'
        '<path d="M24 29v5M17 34h14"/>'),
    # A person.
    "icon-user": ("actor",
        '<circle cx="24" cy="19" r="5"/>'
        '<path d="M14 35c0-6 4.5-9 10-9s10 3 10 9"/>'),
    # An nvstor service or module: a cube.
    "icon-service": ("service",
        '<path d="M24 12l11 6v12l-11 6-11-6V18z"/>'
        '<path d="M13 18l11 6 11-6M24 24v12"/>'),
    # Third-party software (kernel client, kubelet, AWS CLI): a plug.
    "icon-third": ("third",
        '<path d="M19 12v7M29 12v7"/>'
        '<path d="M15 19h18v4a9 9 0 0 1-18 0z"/>'
        '<path d="M24 32v5"/>'),
    # A database: a cylinder.
    "icon-db": ("data",
        '<ellipse cx="24" cy="15" rx="10" ry="3.5"/>'
        '<path d="M14 15v18c0 2 4.5 3.5 10 3.5s10-1.5 10-3.5V15"/>'
        '<path d="M14 24c0 2 4.5 3.5 10 3.5s10-1.5 10-3.5"/>'),
    # Files, chunks, objects, manifests: a document.
    "icon-data": ("data",
        '<path d="M16 12h11l6 6v18H16z"/>'
        '<path d="M27 12v6h6M20 24h9M20 29h9"/>'),
    # A node: two stacked server units.
    "icon-server": ("resource",
        '<rect x="13" y="13" width="22" height="9" rx="1.5"/>'
        '<rect x="13" y="26" width="22" height="9" rx="1.5"/>'
        '<path d="M17 17.5h2M17 30.5h2M25 17.5h6M25 30.5h6"/>'),
    # A workload: an application window.
    "icon-compute": ("resource",
        '<rect x="12" y="14" width="24" height="20" rx="2"/>'
        '<path d="M12 19h24M17 24h6v6h-6zM27 24h4M27 28h4"/>'),
    # NIC, NVMe or registered memory: a chip with pins.
    "icon-hardware": ("resource",
        '<rect x="16" y="16" width="16" height="16" rx="1.5"/>'
        '<path d="M20 12v4M24 12v4M28 12v4M20 32v4M24 32v4M28 32v4'
        'M12 20h4M12 24h4M12 28h4M32 20h4M32 24h4M32 28h4"/>'),
    # An authentication or privilege boundary: a shield.
    "icon-guard": ("resource",
        '<path d="M24 12l10 4v7c0 7-4.5 11-10 13-5.5-2-10-6-10-13v-7z"/>'
        '<path d="M19.5 24l3 3 6-6"/>'),
    # A directory: a folder.
    "icon-folder": ("resource",
        '<path d="M12 17v16h24V20H24l-3-3z"/>'),
    # Admission control: a funnel.
    "icon-admit": ("function",
        '<path d="M13 15h22l-8.5 10v8l-5 3V25z"/>'),
    # A worker or dispatcher: a command prompt.
    "icon-run": ("function",
        '<rect x="12" y="14" width="24" height="20" rx="2"/>'
        '<path d="M17 21l4 3-4 3M24 28h7"/>'),
}


def icon_symbol(sym):
    """The <symbol> element for one icon id."""
    cat, glyph = ICON_GLYPHS[sym]
    fill, stroke = CATEGORY[cat]
    return (
        f'<symbol id="{sym}" viewBox="0 0 48 48">'
        f'<circle cx="24" cy="24" r="23" fill="{fill}"/>'
        f'<g fill="none" stroke="{stroke}" stroke-width="2.2" stroke-linecap="round" '
        f'stroke-linejoin="round">{glyph}</g></symbol>'
    )


STYLE = (
    "<style>"
    'text{font-family:Arial,Helvetica,sans-serif;fill:#000000}'
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
        self.body.append(f'<text x="40" y="44" class="title">{title}</text>')
        if subtitle:
            self.body.append(f'<text x="40" y="64" class="sub">{esc(subtitle)}</text>')

    # ---- primitives -------------------------------------------------------
    def raw(self, s):
        self.body.append(s)

    def group(self, x, y, w, h, label, sub=None, kind="group", anchor="start"):
        """kind: group (generic), nv (an nvstor process or pod), partner (third-party software)."""
        cls = {"group": "group", "nv": "group-nv", "partner": "group-partner"}[kind]
        self.body.append(f'<rect x="{x}" y="{y}" width="{w}" height="{h}" rx="10" class="{cls}"/>')
        tx = x + 14 if anchor == "start" else x + w - 14
        self.body.append(f'<text x="{tx}" y="{y+22}" class="group-label" text-anchor="{anchor}">{esc(label)}</text>')
        if sub:
            self.body.append(f'<text x="{tx}" y="{y+37}" class="small" text-anchor="{anchor}">{esc(sub)}</text>')

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

    def hcard(self, cid, x, y, w, h, sym, label, sub=None, nv=False):
        """A compact component: icon on the left, label and one detail line to its right."""
        self.used.add(sym)
        stroke = ' style="stroke:#76b900;stroke-width:1.5"' if nv else ""
        self.body.append(f'<rect id="{cid}" x="{x}" y="{y}" width="{w}" height="{h}" rx="8" class="card"{stroke}/>')
        self.body.append(f'<use href="#{sym}" x="{x + 10}" y="{y + (h - ICON) / 2:g}" width="{ICON}" height="{ICON}"/>')
        tx = x + ICON + 20
        if sub:
            self.body.append(f'<text x="{tx}" y="{y + h / 2 - 3:g}" class="label" data-fit="{cid}" data-fit-padding="4">{esc(label)}</text>')
            self.body.append(f'<text x="{tx}" y="{y + h / 2 + 12:g}" class="small" data-fit="{cid}" data-fit-padding="4">{esc(sub)}</text>')
        else:
            self.body.append(f'<text x="{tx}" y="{y + h / 2 + 4:g}" class="label" data-fit="{cid}" data-fit-padding="4">{esc(label)}</text>')

    def path(self, d, opt=False):
        cls, mk = ("opt", "arrow-opt") if opt else ("path", "arrow")
        self.body.append(f'<path class="{cls}" d="{d}" marker-end="url(#{mk})"/>')

    def line(self, d, opt=False):
        """A connector with no arrowhead (a bidirectional link, such as a fabric)."""
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
            defs.append(icon_symbol(sym))
        # Arrowheads: tip at the path end point, 6 long x 5 wide, sized in user units.
        defs.append('<marker id="arrow" markerUnits="userSpaceOnUse" markerWidth="6" markerHeight="5" refX="6" refY="2.5" orient="auto"><path d="M0 0L6 2.5L0 5Z" fill="#000000"/></marker>')
        defs.append('<marker id="arrow-opt" markerUnits="userSpaceOnUse" markerWidth="6" markerHeight="5" refX="6" refY="2.5" orient="auto"><path d="M0 0L6 2.5L0 5Z" fill="#999999"/></marker>')
        defs.append(STYLE + "</defs>")
        w, h = self.w, self.h
        head = [
            "<!--",
            "SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.",
            "SPDX-License-Identifier: Apache-2.0",
            "-->",
            f'<svg xmlns="http://www.w3.org/2000/svg" width="{w}" height="{h}" '
            f'viewBox="0 0 {w} {h}" role="img" aria-labelledby="title desc">',
            f'<title id="title">{esc(self.title)}</title>',
            f'<desc id="desc">{esc(self.desc)}</desc>',
        ]
        bg = [f'<rect x="0" y="0" width="{w}" height="{h}" fill="#ffffff"/>']
        out = head + defs + bg + self.body + ["</svg>"]
        p = ROOT / f"{self.name}.svg"
        p.write_text("\n".join(out) + "\n")
        ET.parse(p)
        print(p, p.stat().st_size, "bytes, xml ok")
