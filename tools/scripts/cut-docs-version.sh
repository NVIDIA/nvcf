#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Freeze one stack's documentation train: snapshot docs/<stack>/ into
# docs/<stack>-<train>/, generate the matching Fern product version file, and
# write the catalog snapshot. Overview documentation is unversioned and is
# never cut. Each stack is cut independently.
set -euo pipefail

usage() {
  echo "Usage: $0 --stack <self-managed|compute-plane|observability> --train <X.Y>" >&2
  echo "Example: $0 --stack observability --train 1.3" >&2
  exit 1
}

STACK=""
TRAIN=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --stack) STACK="${2:-}"; shift 2 ;;
    --train) TRAIN="${2:-}"; shift 2 ;;
    *) usage ;;
  esac
done
[[ -n "$STACK" && -n "$TRAIN" ]] || usage

case "$STACK" in
  self-managed) RELEASE_SET_STACK="control-plane" ;;
  compute-plane|observability) RELEASE_SET_STACK="$STACK" ;;
  *) echo "Unknown stack $STACK; want self-managed, compute-plane, or observability" >&2; exit 1 ;;
esac
[[ "$TRAIN" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]] || { echo "Train must use X.Y, got $TRAIN" >&2; exit 1; }

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
docs_src="$root/docs/$STACK"
docs_dst="$root/docs/$STACK-$TRAIN"
nav_src="$root/fern/products/$STACK/dev.yml"
nav_dst="$root/fern/products/$STACK/$TRAIN.yml"
docs_yml="$root/fern/docs.yml"
catalog_snapshot="$root/docs/version-catalog/$STACK-$TRAIN.yaml"

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

echo "==> Writing catalog snapshot docs/version-catalog/$STACK-$TRAIN.yaml"
"${freeze_cmd[@]}" \
  --freeze-stack "$RELEASE_SET_STACK" \
  --freeze-train "$TRAIN"
[[ -f "$catalog_snapshot" ]] || { echo "Freeze did not write $catalog_snapshot" >&2; exit 1; }

echo "==> Snapshotting docs/$STACK -> docs/$STACK-$TRAIN (dereferencing symlinks)"
cp -RL "$docs_src" "$docs_dst"

echo "==> Generating fern/products/$STACK/$TRAIN.yml from dev.yml"
sed "s|/docs/$STACK/|/docs/$STACK-$TRAIN/|g" "$nav_src" > "$nav_dst"

cat <<EOF

Snapshot complete: $STACK $TRAIN

Add this entry under the "$STACK" product's versions list in fern/docs.yml.
List it first if $TRAIN is now the default version for the product, and point
the product "path" at the same file:

      - display-name: "$TRAIN"
        path: products/$STACK/$TRAIN.yml
        slug: "$TRAIN"

Then run:
  cd fern && fern check      # validate nav and links
  fern docs dev              # preview locally
EOF
