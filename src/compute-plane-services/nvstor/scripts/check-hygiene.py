#!/usr/bin/env python3
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

"""Refuse content that must not be published (see AGENTS.md).

    scripts/check-hygiene.py              scan every tracked file
    scripts/check-hygiene.py PATH...      scan these files

Exit 1 and list each hit. The rules: no private registries or cloud
host names; no personal paths or addresses; no measured results; no
license header missing from a source file; no binary file outside the
allowed figure outputs. NVSTOR_HYGIENE_EXTRA may name a file of further
patterns (one regex per line) that is kept outside the tree.
"""
import os
import re
import subprocess
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

TEXT_RULES = [
    ("private registry", re.compile(r"nvcr\.io/(?!nvidia/)[a-z0-9]+/")),
    ("cloud host name", re.compile(
        r"\bip-10-\d+-\d+-\d+|\.compute\.internal|\.ec2\.internal")),
    ("personal path", re.compile(r"(?<![\w.])/(home|Users)/[a-z][\w.-]*")),
    ("personal or internal email", re.compile(
        r"[\w.+-]+@nvidia\.com", re.I), {"psirt@nvidia.com", "github_conduct@nvidia.com"}),
    ("measured throughput", re.compile(
        r"\b\d+(\.\d+)?\s?(GiB|GB|MiB|MB|KiB|Gbit|Gb)/s\b")),
]

# An optional extra denylist kept outside the tree: a file named by
# NVSTOR_HYGIENE_EXTRA with one regular expression per line (blank lines
# and lines starting with # are ignored). A leading (?i) makes a pattern
# case-insensitive, as in any Python regex.
EXTRA_ENV = "NVSTOR_HYGIENE_EXTRA"


def extra_rules():
    path = os.environ.get(EXTRA_ENV)
    if not path:
        return []
    rules = []
    with open(path, encoding="utf-8") as f:
        for n, line in enumerate(f, 1):
            pattern = line.strip()
            if not pattern or pattern.startswith("#"):
                continue
            try:
                rules.append(("internal denylist", re.compile(pattern)))
            except re.error as e:
                sys.exit(f"{path}:{n}: bad pattern {pattern!r}: {e}")
    return rules


SOURCE_EXT = {".rs", ".py", ".sh", ".c", ".h", ".cjs", ".yaml", ".yml", ".toml", ".tf", ".tpl"}
HEADER_SKIP = {"bins/nvstor-csi/proto/csi.proto"}
BINARY_OK = re.compile(r"^docs/figures/nvstor-[a-z0-9-]+-(light|dark)\.png$")
SELF = "scripts/check-hygiene.py"


def tracked():
    out = subprocess.run(["git", "ls-files", "-z"], cwd=ROOT, capture_output=True, check=True).stdout
    return [p for p in out.decode().split("\0") if p]


def scan(path, rules):
    hits = []
    full = os.path.join(ROOT, path)
    raw = open(full, "rb").read()
    if b"\0" in raw[:8192]:
        if not BINARY_OK.match(path):
            hits.append((path, 0, "binary file", ""))
        return hits
    text = raw.decode("utf-8", errors="replace")
    if path != SELF:
        for n, line in enumerate(text.splitlines(), 1):
            for rule in rules:
                name, rx = rule[0], rule[1]
                allowed = rule[2] if len(rule) > 2 else set()
                for m in rx.finditer(line):
                    if m.group(0).lower() in allowed:
                        continue
                    hits.append((path, n, name, m.group(0)))
    ext = os.path.splitext(path)[1]
    if (ext in SOURCE_EXT or os.path.basename(path).startswith("Dockerfile")) and path not in HEADER_SKIP:
        head = "\n".join(text.splitlines()[:30])
        generated = "Code generated" in head and "DO NOT EDIT" in head
        if not generated and not ("SPDX-FileCopyrightText:" in head and "SPDX-License-Identifier:" in head):
            hits.append((path, 1, "missing SPDX license header", ""))
    return hits


def main(argv):
    paths = argv or tracked()
    rules = TEXT_RULES + extra_rules()
    hits = [h for p in paths if os.path.isfile(os.path.join(ROOT, p)) for h in scan(p, rules)]
    for path, line, rule, text in hits:
        print(f"{path}:{line}: {rule}" + (f": {text}" if text else ""))
    if hits:
        print(f"{len(hits)} problem(s); see AGENTS.md", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
