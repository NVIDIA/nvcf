#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Snapshot the top-of-tree docs/user tree into a versioned subdirectory and
# generate a matching Fern navigation file. See the internal docs-versioning
# plan for the design.
set -euo pipefail

if [[ $# -ne 1 ]]; then
  echo "Usage: $0 <version>" >&2
  echo "Example: $0 cp-0.20.6-compute-0.4.4-obs-0.2.2" >&2
  exit 1
fi

VERSION="$1"
DISPLAY="$VERSION"

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
docs_src="$root/docs/user"
docs_dst="$root/docs/$VERSION"
nav_src="$root/fern/versions/dev.yml"
nav_dst="$root/fern/versions/$VERSION.yml"
docs_yml="$root/fern/docs.yml"
catalog="$root/docs/version-catalog/main.yaml"
catalog_snapshot="$root/docs/version-catalog/$VERSION.yaml"

[[ -d "$docs_src" ]] || { echo "Missing $docs_src" >&2; exit 1; }
[[ -f "$nav_src" ]] || { echo "Missing $nav_src" >&2; exit 1; }
[[ -f "$docs_yml" ]] || { echo "Missing $docs_yml" >&2; exit 1; }
[[ -f "$catalog" ]] || { echo "Missing $catalog" >&2; exit 1; }
[[ ! -d "$docs_dst" ]] || { echo "Refusing to overwrite $docs_dst" >&2; exit 1; }
[[ ! -f "$nav_dst" ]] || { echo "Refusing to overwrite $nav_dst" >&2; exit 1; }
[[ ! -f "$catalog_snapshot" ]] || { echo "Refusing to overwrite $catalog_snapshot" >&2; exit 1; }

stack_display="$(python3 - "$catalog" "$DISPLAY" <<'PY'
from pathlib import Path
import re
import sys

catalog_text = Path(sys.argv[1]).read_text()
display = sys.argv[2]


def release_set_value(name):
    match = re.search(rf'^  {re.escape(name)}:\s*(\S+)\s*$', catalog_text, re.MULTILINE)
    if not match:
        raise SystemExit(f"release_set is missing {name}")
    return match.group(1)


def stack_version(name):
    match = re.search(
        rf'^    {re.escape(name)}:\n      version:\s*(\S+)\s*$',
        catalog_text,
        re.MULTILINE,
    )
    if not match:
        raise SystemExit(f"release_set is missing {name} version")
    return match.group(1)


if release_set_value("status") != "qualified":
    raise SystemExit("release_set must be qualified before cutting versioned docs")
if release_set_value("documentation_version") != display:
    raise SystemExit("release_set documentation_version does not match requested docs version")
control_version = stack_version("control-plane")
compute_version = stack_version("compute-plane")
observability_version = stack_version("observability")
print(f"CP {control_version}, Compute {compute_version}, Obs {observability_version}")
PY
)"

echo "==> Snapshotting docs/user -> docs/$VERSION (dereferencing symlinks)"
cp -RL "$docs_src" "$docs_dst"
cp "$catalog" "$catalog_snapshot"

echo "==> Generating fern/versions/$VERSION.yml from dev.yml"
sed "s|../../docs/user/|../../docs/$VERSION/|g" "$nav_src" > "$nav_dst"

echo "==> Updating latest and version entries in fern/docs.yml"
python3 - "$docs_yml" "$VERSION" "$DISPLAY" "$stack_display" <<'PY'
from pathlib import Path
import re
import sys

docs_yml = Path(sys.argv[1])
version = sys.argv[2]
display = sys.argv[3]
stack_display = sys.argv[4]

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
    f'- display-name: "Latest ({display}; {stack_display})"\n'
    f"  path: versions/{version}.yml\n"
    '  slug: ""\n'
)
dev_entry = next((entry for entry in entries if entry_display_name(entry) == "dev"), (
    "- display-name: dev\n"
    "  path: versions/dev.yml\n"
    "  slug: dev\n"
))
stable_entry = (
    f'- display-name: "{display} ({stack_display})"\n'
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
