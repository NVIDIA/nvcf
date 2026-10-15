#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Snapshot the top-of-tree docs/user tree into a versioned subdirectory and
# generate a matching Fern navigation file. See the internal docs-versioning
# plan for the design.
set -euo pipefail

if [[ $# -ne 1 ]]; then
  echo "Usage: $0 <version>" >&2
  echo "Example: $0 v0.6.0" >&2
  exit 1
fi

VERSION="$1"
DISPLAY="${VERSION#v}"

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
docs_src="$root/docs/user"
docs_dst="$root/docs/$VERSION"
nav_src="$root/fern/versions/dev.yml"
nav_dst="$root/fern/versions/$VERSION.yml"
docs_yml="$root/fern/docs.yml"

[[ -d "$docs_src" ]] || { echo "Missing $docs_src" >&2; exit 1; }
[[ -f "$nav_src" ]] || { echo "Missing $nav_src" >&2; exit 1; }
[[ -f "$docs_yml" ]] || { echo "Missing $docs_yml" >&2; exit 1; }
[[ ! -d "$docs_dst" ]] || { echo "Refusing to overwrite $docs_dst" >&2; exit 1; }
[[ ! -f "$nav_dst" ]] || { echo "Refusing to overwrite $nav_dst" >&2; exit 1; }

echo "==> Snapshotting docs/user -> docs/$VERSION (dereferencing symlinks)"
cp -RL "$docs_src" "$docs_dst"

echo "==> Generating fern/versions/$VERSION.yml from dev.yml"
sed "s|../../docs/user/|../../docs/$VERSION/|g" "$nav_src" > "$nav_dst"

echo "==> Updating latest and version entries in fern/docs.yml"
python3 - "$docs_yml" "$VERSION" "$DISPLAY" <<'PY'
from pathlib import Path
import re
import sys

docs_yml = Path(sys.argv[1])
version = sys.argv[2]
display = sys.argv[3]

text = docs_yml.read_text()
versions_marker = "versions:\n"
redirects_marker = "\nredirects:"

versions_start = text.index(versions_marker) + len(versions_marker)
redirects_start = text.index(redirects_marker, versions_start)

prefix = text[:versions_start]
versions_block = text[versions_start:redirects_start]
suffix = text[redirects_start:]

entries = []
current = []
for line in versions_block.splitlines(keepends=True):
    if line.startswith("- display-name:") and current:
        entries.append("".join(current))
        current = [line]
    elif current or line.strip():
        current.append(line)
if current:
    entries.append("".join(current))


def entry_display_name(entry):
    match = re.search(r'^- display-name:\s*"?([^"\n]+)"?\s*$', entry, re.MULTILINE)
    return match.group(1) if match else ""


def entry_slug(entry):
    match = re.search(r'^\s+slug:\s*"?([^"\n]*)"?\s*$', entry, re.MULTILINE)
    return match.group(1) if match else ""


latest_entry = (
    f'- display-name: "Latest ({display})"\n'
    f"  path: versions/{version}.yml\n"
    '  slug: ""\n'
)
dev_entry = next((entry for entry in entries if entry_display_name(entry) == "dev"), (
    "- display-name: dev\n"
    "  path: versions/dev.yml\n"
    "  slug: dev\n"
))
stable_entry = (
    f'- display-name: "{display}"\n'
    f"  path: versions/{version}.yml\n"
    f'  slug: "{version}"\n'
)
redirect_entry = (
    f'  - source: "/nvcf/{version}/index.html"\n'
    f'    destination: "/nvcf/{version}/"\n'
)

kept_entries = []
for entry in entries:
    name = entry_display_name(entry)
    slug = entry_slug(entry)
    if name.startswith("Latest (") or name == "dev" or slug == version:
        continue
    kept_entries.append(entry if entry.endswith("\n") else entry + "\n")

if f'source: "/nvcf/{version}/index.html"' not in suffix:
    suffix = suffix.rstrip() + "\n" + redirect_entry

docs_yml.write_text(prefix + latest_entry + dev_entry + stable_entry + "".join(kept_entries) + suffix)
PY

echo ""
echo "Snapshot complete: $VERSION"
echo "Next steps:"
echo "  cd fern && fern check      # validate nav"
echo "  fern docs dev              # preview locally"
