#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Fail if a committed figure no longer matches its script.

Runs every ``docs/figures/scripts/nvsnap-*.py`` into a temporary directory
and compares each SVG with the committed one, byte for byte. Also fails when
a figure has no light or dark PNG beside it, or when an SVG or PNG under
``docs/figures/`` has no script. The PNGs are rendered by a browser
(``render.sh``), so only their presence is checked here.

Usage: ``docs/figures/scripts/check.py``
"""
from __future__ import annotations

import os
import pathlib
import subprocess
import sys
import tempfile

SCRIPTS = pathlib.Path(__file__).resolve().parent
FIGURES = SCRIPTS.parent


def main() -> int:
    names = sorted(p.stem for p in SCRIPTS.glob("nvsnap-*.py"))
    problems = []
    with tempfile.TemporaryDirectory() as tmp:
        env = dict(os.environ, NVSNAP_FIGURES_OUT=tmp)
        for n in names:
            subprocess.run([sys.executable, str(SCRIPTS / f"{n}.py")], check=True, env=env,
                           stdout=subprocess.DEVNULL)
            fresh = pathlib.Path(tmp, f"{n}.svg").read_bytes()
            committed = FIGURES / f"{n}.svg"
            if not committed.exists():
                problems.append(f"{committed.name}: missing; run docs/figures/scripts/render.sh {n}")
            elif committed.read_bytes() != fresh:
                problems.append(f"{committed.name}: differs from {n}.py; run docs/figures/scripts/render.sh {n}")
            for theme in ("light", "dark"):
                if not (FIGURES / f"{n}-{theme}.png").exists():
                    problems.append(f"{n}-{theme}.png: missing; run docs/figures/scripts/render.sh {n}")
    known = set(names) | {f"{n}-{t}" for n in names for t in ("light", "dark")}
    for p in sorted(FIGURES.glob("*.svg")) + sorted(FIGURES.glob("*.png")):
        if p.stem not in known:
            problems.append(f"{p.name}: no script draws it")
    for p in problems:
        print(p)
    print(f"checked {len(names)} figures, {len(problems)} problems")
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main())
