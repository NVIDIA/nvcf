#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Write or check docs/REFERENCE.md, generated from the Go sources.

The reference lists what a user, operator or another component can see of
nvsnap without reading its code:

- every ``nvsnap.io/*`` annotation and label key in non-test Go code, with
  the constant that names it and that constant's doc comment;
- every environment variable an nvsnap binary reads, and whether the chart
  sets it on the agent;
- every route of the agent's HTTP API, with the comment above it.

Descriptions are the code's own comments, so the page changes when the code
does. Usage: ``scripts/docs/reference.py [--check]``; ``--check`` exits 1
when docs/REFERENCE.md differs from what the sources generate.
"""
from __future__ import annotations

import importlib.util
import pathlib
import re
import subprocess
import sys

HERE = pathlib.Path(__file__).resolve().parent
NVSNAP = HERE.parents[1]
_spec = importlib.util.spec_from_file_location("docs_toc", HERE / "docs-toc.py")
assert _spec and _spec.loader
_toc = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(_toc)
OUT = NVSNAP / "docs" / "REFERENCE.md"
AGENT_ROUTES = NVSNAP / "internal" / "agent" / "agent.go"
CHART_DAEMONSET = NVSNAP / "deploy" / "helm" / "nvsnap" / "templates" / "agent-daemonset.yaml"

KEY = re.compile(r'"(nvsnap\.io/[A-Za-z0-9._/-]+)"')
DECL = re.compile(r'^\s*(?:const\s+|var\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*(?:[A-Za-z_.]+\s*)?=\s*"([^"]+)"\s*(?://\s*(.*))?$')
ENV_GETTERS = r"(?:Getenv|LookupEnv|getenv|mustEnv|envOr|envDurationOr|envBoolOr|isTruthyEnv|parseDurationEnv|envInt|envIntOr)"
ENV_LITERAL = re.compile(ENV_GETTERS + r'\(\s*"([A-Z][A-Z0-9_]+)"')
ENV_IDENT = re.compile(ENV_GETTERS + r"\(\s*([A-Za-z_][A-Za-z0-9_.]*)\s*[,)]")
ROUTE = re.compile(r'router\.(?:HandleFunc|Handle)\("([^"]+)",\s*(.+?)\)(?:\.Methods\(([^)]*)\))?\s*$')


def go_files() -> list[pathlib.Path]:
    out = subprocess.run(["git", "-C", str(NVSNAP), "ls-files", "--", "*.go"],
                         check=True, capture_output=True, text=True).stdout
    return sorted(NVSNAP / p for p in out.splitlines()
                  if not p.endswith("_test.go") and "node_modules/" not in p
                  and (p.startswith(("internal/", "cmd/", "pkg/"))))


def comment_above(lines: list[str], i: int) -> str:
    """The // comment block directly above line i, joined into one paragraph."""
    block: list[str] = []
    j = i - 1
    while j >= 0 and lines[j].strip().startswith("//"):
        block.insert(0, lines[j].strip()[2:].strip())
        j -= 1
    return " ".join(b for b in block if b)


def first_sentence(text: str, name: str = "") -> str:
    text = re.sub(r"\s+", " ", text).strip()
    if name and text.startswith(name + " "):
        text = text[len(name) + 1:]
    m = re.search(r"(?<=[a-z0-9)`\"'])\.(?:\s|$)", text)
    if m:
        text = text[:m.start() + 1]
    if len(text) > 240:
        text = text[:237].rsplit(" ", 1)[0] + "…"
    if text[:1].islower():
        text = text[0].upper() + text[1:]
    return text.replace("|", "\\|")


def rel(p: pathlib.Path) -> str:
    return str(p.relative_to(NVSNAP))


def collect_keys(files: list[pathlib.Path]) -> dict[str, dict]:
    keys: dict[str, dict] = {}
    consts: dict[str, str] = {}
    for f in files:
        lines = f.read_text(encoding="utf-8").splitlines()
        for i, line in enumerate(lines):
            m = DECL.match(line)
            if m and m.group(2).startswith("nvsnap.io/"):
                name, key, trailing = m.group(1), m.group(2), m.group(3) or ""
                desc = comment_above(lines, i) or trailing
                e = keys.setdefault(key, {"consts": [], "desc": "", "files": set()})
                e["consts"].append(f"`{name}` ({rel(f)})")
                if not e["desc"] and desc:
                    e["desc"] = first_sentence(desc, name)
                consts[name] = key
                continue
            for key in KEY.findall(line):
                keys.setdefault(key, {"consts": [], "desc": "", "files": set()})["files"].add(rel(f))
    return keys


def collect_env(files: list[pathlib.Path]) -> dict[str, set[str]]:
    consts: dict[str, str] = {}
    for f in files:
        for line in f.read_text(encoding="utf-8").splitlines():
            m = DECL.match(line)
            if m and re.fullmatch(r"[A-Z][A-Z0-9_]+", m.group(2)):
                consts[m.group(1)] = m.group(2)
    env: dict[str, set[str]] = {}
    for f in files:
        text = f.read_text(encoding="utf-8")
        names = set(ENV_LITERAL.findall(text))
        for ident in ENV_IDENT.findall(text):
            name = consts.get(ident.split(".")[-1])
            if name:
                names.add(name)
        for n in names:
            env.setdefault(n, set()).add(rel(f))
    return env


def chart_env() -> set[str]:
    if not CHART_DAEMONSET.exists():
        return set()
    return set(re.findall(r"-\s*name:\s*([A-Z][A-Z0-9_]+)", CHART_DAEMONSET.read_text(encoding="utf-8")))


def collect_routes() -> list[tuple[str, str, str]]:
    lines = AGENT_ROUTES.read_text(encoding="utf-8").splitlines()
    routes = []
    for i, line in enumerate(lines):
        m = ROUTE.search(line)
        if not m:
            continue
        methods = ", ".join(x.strip().strip('"') for x in (m.group(3) or "").split(",") if x.strip()) or "any"
        handler = m.group(2).strip().removeprefix("a.")
        desc = comment_above(lines, i)
        if len(desc.split()) < 3:
            desc = ""  # a group heading ("API routes"), not a description
        routes.append((m.group(1), methods, f"`{handler}`" + (f": {first_sentence(desc)}" if desc else "")))
    return routes


def render() -> str:
    files = go_files()
    keys = collect_keys(files)
    env = collect_env(files)
    in_chart = chart_env()
    routes = collect_routes()
    out = [
        "<!--",
        "SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.",
        "SPDX-License-Identifier: Apache-2.0",
        "-->",
        "# nvsnap reference",
        "",
        "Generated by `scripts/docs/reference.py` from the Go sources; do not edit.",
        "`make check-docs` fails when this page and the code disagree. Each",
        "description is the first sentence of the code's own comment; follow the",
        "constant or handler to read the rest. Words are defined in the",
        "[glossary](GLOSSARY.md).",
        "",
        "## Annotations and labels",
        "",
        "Every `nvsnap.io/*` key in non-test Go code. Keys a user or the platform",
        "sets and keys nvsnap writes for its own bookkeeping are listed together;",
        "the description says which. A key with no constant is written inline",
        "where the files column says.",
        "",
        "| Key | Constant | Description |",
        "|---|---|---|",
    ]
    for key in sorted(keys):
        e = keys[key]
        where = "<br>".join(sorted(set(e["consts"]))) or "inline: " + ", ".join(sorted(e["files"]))
        out.append(f"| `{key}` | {where} | {e['desc']} |")
    out += [
        "",
        "## Environment variables",
        "",
        "Every environment variable an nvsnap binary reads. \"Chart\" marks the",
        "ones the Helm chart sets on the agent DaemonSet",
        "(`deploy/helm/nvsnap/templates/agent-daemonset.yaml`); the others are",
        "set by hand through `agent.extraEnv`, or by the pod spec that runs the",
        "binary.",
        "",
        "| Variable | Chart | Read in |",
        "|---|---|---|",
    ]
    for name in sorted(env):
        out.append(f"| `{name}` | {'yes' if name in in_chart else ''} | {', '.join(sorted(env[name]))} |")
    out += [
        "",
        "## Agent HTTP API",
        "",
        "The routes of the node agent (`internal/agent/agent.go`). The server's",
        "API is specified in [openapi.yaml](../internal/server/openapi.yaml).",
        "",
        "| Route | Methods | Handler |",
        "|---|---|---|",
    ]
    for path, methods, desc in routes:
        out.append(f"| `{path}` | {methods} | {desc} |")
    return _toc.render("\n".join(out) + "\n")


def main(argv: list[str]) -> int:
    text = render()
    if "--check" in argv:
        current = OUT.read_text(encoding="utf-8") if OUT.exists() else ""
        if current != text:
            print(f"{rel(OUT)} is stale (run scripts/docs/reference.py)", file=sys.stderr)
            return 1
        return 0
    OUT.write_text(text, encoding="utf-8")
    print(f"wrote {rel(OUT)}")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
