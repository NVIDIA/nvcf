#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Snapshot the current docs/user tree into a versioned subdirectory and
# generate a matching Fern navigation file. See
# nvidia-internal/plans/docs-versioning.plan.md for the design.
set -euo pipefail

if [[ $# -ne 1 ]]; then
  echo "Usage: $0 <version>" >&2
  echo "Example: $0 v0.5" >&2
  exit 1
fi

VERSION="$1"
DISPLAY="${VERSION#v}"

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
docs_src="$root/docs/user"
docs_dst="$root/docs/$VERSION"
nav_src="$root/fern/versions/main.yml"
nav_dst="$root/fern/versions/$VERSION.yml"
docs_yml="$root/fern/docs.yml"

[[ -d "$docs_src" ]] || { echo "Missing $docs_src" >&2; exit 1; }
[[ -f "$nav_src" ]] || { echo "Missing $nav_src" >&2; exit 1; }
[[ -f "$docs_yml" ]] || { echo "Missing $docs_yml" >&2; exit 1; }
[[ ! -d "$docs_dst" ]] || { echo "Refusing to overwrite $docs_dst" >&2; exit 1; }
[[ ! -f "$nav_dst" ]] || { echo "Refusing to overwrite $nav_dst" >&2; exit 1; }

echo "==> Snapshotting docs/user -> docs/$VERSION (dereferencing symlinks)"
cp -RL "$docs_src" "$docs_dst"

echo "==> Generating fern/versions/$VERSION.yml from main.yml"
sed "s|../../docs/user/|../../docs/$VERSION/|g" "$nav_src" > "$nav_dst"

echo "==> Appending version entry to fern/docs.yml"
cat >> "$docs_yml" <<EOF
- display-name: "$DISPLAY"
  path: versions/$VERSION.yml
  slug: "$VERSION"
EOF

echo ""
echo "Snapshot complete: $VERSION"
echo "Next steps:"
echo "  cd fern && fern check      # validate nav"
echo "  fern docs dev              # preview locally"
