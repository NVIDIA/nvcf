#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Fail if a Markdown file under nvsnap links to a path that does not exist.

Checks relative links and image references (``[t](path)``, ``![t](path)``,
``<img src="path">``) in every git-tracked ``*.md`` under the nvsnap root.
External URLs, ``mailto:`` and pure anchors are skipped; a ``#fragment`` is
checked against the target's headings when the target is Markdown (a pure
``#fragment`` against the file's own). A doc path written as text
(``docs/X.md`` in backticks or as link text) must exist too, under the nvsnap
root or the repository root, and so must one named in a comment or string
of code, a script or a chart (``CODE_DOC_PATH``), so moving a doc cannot
strand the code that points readers at it. ``docs/results/``
holds dated records of how things were and is not maintained, so it is not
checked.

Usage: ``scripts/docs/check-doc-links.py [nvsnap-root]`` (default: the
nvsnap directory this script lives in).
"""
from __future__ import annotations

import pathlib
import re
import subprocess
import sys
import urllib.parse

NVSNAP = pathlib.Path(__file__).resolve().parents[2]

LINK = re.compile(r"!?\[[^\]]*\]\(([^)\s]+)(?:\s+\"[^\"]*\")?\)")
IMG = re.compile(r"<img[^>]*\ssrc=\"([^\"]+)\"")
HEADING = re.compile(r"^#{1,6}\s+(.*?)\s*#*\s*$", re.M)
DOC_PATH_TEXT = re.compile(r"(?:`|\[)(docs/[A-Za-z0-9_][A-Za-z0-9_./-]*\.md)(?:`|\])")
CODE_DOC_PATH = re.compile(r"(?<![\w/.-])(docs/[A-Za-z0-9_][A-Za-z0-9_./-]*\.md)")
CODE_SUFFIXES = (".go", ".sh", ".py", ".yaml", ".yml", ".txt", ".tpl", ".c", ".h", "Makefile", "Dockerfile")
SKIP = ("http://", "https://", "mailto:", "file://", "ssh://", "git@")
FROZEN = ("docs/results/",)


def tracked_markdown(root: pathlib.Path) -> list[pathlib.Path]:
    out = subprocess.run(
        ["git", "-C", str(root), "ls-files", "--", "*.md"],
        check=True, capture_output=True, text=True,
    ).stdout
    # ui/node_modules ships third-party Markdown that is not ours to check, and
    # .agents/skills/ holds skills copied unchanged from other repositories,
    # whose links point into those repositories.
    return sorted({root / p for p in out.splitlines()
                   if p and "node_modules/" not in p and not p.startswith(".agents/")})


def tracked_code(root: pathlib.Path) -> list[pathlib.Path]:
    out = subprocess.run(["git", "-C", str(root), "ls-files"],
                         check=True, capture_output=True, text=True).stdout
    # The checkers and their tests name example docs that do not exist.
    return sorted(root / p for p in out.splitlines()
                  if p.endswith(CODE_SUFFIXES) and "node_modules/" not in p
                  and not p.startswith("scripts/docs/"))


def repo_root(root: pathlib.Path) -> pathlib.Path:
    out = subprocess.run(["git", "-C", str(root), "rev-parse", "--show-toplevel"],
                         check=True, capture_output=True, text=True).stdout.strip()
    return pathlib.Path(out)


def slug(heading: str) -> str:
    """GitHub heading slug: drop markup, lowercase, drop punctuation other
    than ``-`` and ``_``, and turn each space into a dash (GitHub does not
    collapse repeated dashes)."""
    text = re.sub(r"\[([^\]]*)\]\([^)]*\)", r"\1", heading)
    text = re.sub(r"[`*]", "", text)
    text = re.sub(r"(?<!\w)_+|_+(?!\w)", "", text)
    text = text.strip().lower()
    text = re.sub(r"[^\w\- ]", "", text)
    return text.replace(" ", "-")


def strip_fences(text: str) -> str:
    out, in_fence = [], False
    for line in text.splitlines():
        if line.lstrip().startswith(("```", "~~~")):
            in_fence = not in_fence
            continue
        if not in_fence:
            out.append(line)
    return "\n".join(out)


def anchors(path: pathlib.Path) -> set[str]:
    try:
        text = path.read_text(encoding="utf-8")
    except (OSError, UnicodeDecodeError):
        return set()
    seen: dict[str, int] = {}
    out: set[str] = set()
    for line in strip_fences(text).splitlines():
        m = HEADING.match(line)
        if not m:
            continue
        s = slug(m.group(1))
        n = seen.get(s, 0)
        seen[s] = n + 1
        out.add(s if n == 0 else f"{s}-{n}")
    # Explicit anchors: <a id="x"></a> / <a name="x">.
    out.update(re.findall(r"<a\s+(?:id|name)=\"([^\"]+)\"", text))
    return out


def check(root: pathlib.Path) -> tuple[int, list[str]]:
    top = repo_root(root)
    problems: list[str] = []
    checked = 0
    for md in tracked_markdown(root):
        rel = md.relative_to(root)
        if str(rel).startswith(FROZEN):
            continue
        text = strip_fences(md.read_text(encoding="utf-8"))
        text = re.sub(r"`[^`\n]*`", lambda m: m.group(0) if DOC_PATH_TEXT.search(m.group(0)) else "", text)
        for raw in LINK.findall(text) + IMG.findall(text):
            if raw.startswith(SKIP) or raw.startswith("<"):
                continue
            target, _, frag = raw.partition("#")
            target = urllib.parse.unquote(target)
            dest = (md.parent / target).resolve() if target else md
            checked += 1
            if not dest.exists():
                problems.append(f"{rel}: missing target {raw}")
                continue
            if frag and dest.suffix.lower() == ".md" and frag.lower() not in anchors(dest):
                problems.append(f"{rel}: no heading #{frag} in {dest.relative_to(top)}")
        for written in DOC_PATH_TEXT.findall(text):
            if not (root / written).exists() and not (top / written).exists():
                problems.append(f"{rel}: names {written}, which does not exist")
    for code in tracked_code(root):
        try:
            text = code.read_text(encoding="utf-8")
        except (OSError, UnicodeDecodeError):
            continue
        for written in sorted(set(CODE_DOC_PATH.findall(text))):
            if not str(written).startswith(FROZEN) and not (root / written).exists() and not (top / written).exists():
                problems.append(f"{code.relative_to(root)}: names {written}, which does not exist")
    return checked, problems


def main() -> int:
    root = pathlib.Path(sys.argv[1]).resolve() if len(sys.argv) > 1 else NVSNAP
    checked, problems = check(root)
    for p in problems:
        print(p)
    print(f"checked {checked} links, {len(problems)} problems")
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main())
