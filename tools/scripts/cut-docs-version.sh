#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Freeze one stack's documentation version: snapshot docs/<stack>/ into
# docs/<stack>-<version>/, generate the matching Fern product version file, and
# write the catalog snapshot. Overview documentation is unversioned and is
# never cut. Each stack is cut independently.
set -euo pipefail

usage() {
  echo "Usage: $0 --stack <self-managed|compute-plane|observability> --version <X.Y.Z>" >&2
  echo "Example: $0 --stack observability --version 1.3.0" >&2
  exit 1
}

STACK=""
VERSION=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --stack) STACK="${2:-}"; shift 2 ;;
    --version) VERSION="${2:-}"; shift 2 ;;
    *) usage ;;
  esac
done
[[ -n "$STACK" && -n "$VERSION" ]] || usage

case "$STACK" in
  self-managed) RELEASE_SET_STACK="control-plane" ;;
  compute-plane|observability) RELEASE_SET_STACK="$STACK" ;;
  *) echo "Unknown stack $STACK; want self-managed, compute-plane, or observability" >&2; exit 1 ;;
esac
[[ "$VERSION" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]] || { echo "Version must use X.Y.Z, got $VERSION" >&2; exit 1; }

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
docs_src="$root/docs/$STACK"
docs_dst="$root/docs/$STACK-$VERSION"
nav_src="$root/fern/products/$STACK/dev.yml"
nav_dst="$root/fern/products/$STACK/$VERSION.yml"
docs_yml="$root/fern/docs.yml"
catalog_snapshot="$root/docs/version-catalog/$STACK-$VERSION.yaml"

[[ -d "$docs_src" ]] || { echo "Missing $docs_src" >&2; exit 1; }
[[ -f "$nav_src" ]] || { echo "Missing $nav_src" >&2; exit 1; }
[[ -f "$docs_yml" ]] || { echo "Missing $docs_yml" >&2; exit 1; }
[[ ! -d "$docs_dst" ]] || { echo "Refusing to overwrite $docs_dst" >&2; exit 1; }
[[ ! -f "$nav_dst" ]] || { echo "Refusing to overwrite $nav_dst" >&2; exit 1; }
[[ ! -f "$catalog_snapshot" ]] || { echo "Refusing to overwrite $catalog_snapshot" >&2; exit 1; }

freeze_cmd=(go run -C "$root/tools/docs-version-sync" . --target main)
if [[ -n "${DOCS_VERSION_SYNC_CMD:-}" ]]; then
  read -r -a freeze_cmd <<< "$DOCS_VERSION_SYNC_CMD"
fi

echo "==> Writing catalog snapshot docs/version-catalog/$STACK-$VERSION.yaml"
"${freeze_cmd[@]}" \
  --freeze-stack "$RELEASE_SET_STACK" \
  --freeze-version "$VERSION"
[[ -f "$catalog_snapshot" ]] || { echo "Freeze did not write $catalog_snapshot" >&2; exit 1; }

echo "==> Snapshotting docs/$STACK -> docs/$STACK-$VERSION (dereferencing symlinks)"
cp -RL "$docs_src" "$docs_dst"

echo "==> Generating fern/products/$STACK/$VERSION.yml from dev.yml"
sed "s|/docs/$STACK/|/docs/$STACK-$VERSION/|g" "$nav_src" > "$nav_dst"

cat <<EOF

Snapshot complete: $STACK $VERSION

Add this entry under the "$STACK" product's versions list in fern/docs.yml.
List it first if $VERSION is now the default version for the product, and point
the product "path" at the same file:

      - display-name: "$VERSION"
        path: products/$STACK/$VERSION.yml
        slug: "$VERSION"

Then run:
  cd fern && fern check      # validate nav and links
  fern docs dev              # preview locally
EOF
