#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Build repo-owned Go modules without a repo-wide go.work.
set -euo pipefail

# mapfile below requires bash 4+. macOS ships bash 3.2 by default; install a
# newer bash with `brew install bash` and re-run.
if (( BASH_VERSINFO[0] < 4 )); then
    echo "error: bash 4 or newer is required (found ${BASH_VERSION})." >&2
    echo "       On macOS, run: brew install bash" >&2
    exit 1
fi

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

build_module() {
  local dir="$1"
  local label="$2"

  echo "==> $label"
  (cd "$dir" && GOWORK=off go build ./...)
}

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
