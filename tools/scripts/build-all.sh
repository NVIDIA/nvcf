#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Build repo-owned Go modules without a repo-wide go.work.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

build_module() {
  local dir="$1"
  local label="$2"

  echo "==> $label"
  (cd "$dir" && GOWORK=off go build ./...)
}

echo "==> tools/sync-synthetic-imports"
(cd "$root/tools/sync-synthetic-imports" && GOWORK=off go build -ldflags="-s -w" -o "$root/tools/scripts/sync-synthetic-imports" ./cmd)

build_module "$root/tools/collect-dependencies" "tools/collect-dependencies"
build_module "$root/tools/generate-subproject-ci" "tools/generate-subproject-ci"
build_module "$root/tools/byoo" "tools/byoo"

mapfile -t public_module_dirs < <(
  while IFS= read -r gomod; do
    module_path="$(sed -n 's/^module[[:space:]]\+//p' "$gomod" | sed -n '1p')"
    if [[ "$module_path" == github.com/NVIDIA/* ]]; then
      dirname "$gomod"
    fi
  done < <(
    find "$root/src" -name go.mod \
      -not -path '*/vendor/*' \
      -not -path '*/.git/*' \
      | sort
  )
)

for dir in "${public_module_dirs[@]}"; do
  build_module "$dir" "${dir#"${root}/"}"
done

echo "build-all: OK (${#public_module_dirs[@]} public modules + tooling)"
