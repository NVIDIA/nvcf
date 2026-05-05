#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0


set -euo pipefail

usage() {
  cat <<'EOF'
Usage: check-imports.sh [--root <path>] [--manifest <path>]

Checks that each imported directory in the synthetic monorepo matches the
source repository and pinned commit recorded in imports.yaml.
EOF
}

require_command() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "missing required command: $1" >&2
    exit 1
  fi
}

trim_value() {
  local value="$1"
  value="${value#"${value%%[![:space:]]*}"}"
  value="${value%"${value##*[![:space:]]}"}"
  value="${value%\"}"
  value="${value#\"}"
  value="${value%\'}"
  value="${value#\'}"
  printf '%s' "$value"
}

normalize_authoritative_source() {
  local raw
  raw="$(trim_value "${1:-}")"
  raw="${raw,,}"
  if [[ -z "$raw" ]]; then
    printf 'upstream'
    return 0
  fi
  case "$raw" in
    upstream)
      printf 'upstream'
      ;;
    native|monorepo)
      printf 'native'
      ;;
    *)
      echo "invalid authoritative_source: $raw" >&2
      return 1
      ;;
  esac
}

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/../.." && pwd)"
root="$repo_root"
manifest="$repo_root/imports.yaml"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --root)
      root="$2"
      shift 2
      ;;
    --manifest)
      manifest="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown argument: $1" >&2
      usage >&2
      exit 1
      ;;
  esac
done

require_command git
require_command yq
require_command tar
require_command diff

gitlab_instead_of_config='url.git@github.com/NVIDIA:.insteadOf=https://github.com/NVIDIA/'

materialize_submodules() {
  local clone_dir="$1"

  if [[ ! -f "$clone_dir/.gitmodules" ]]; then
    return 0
  fi

  if ! git -C "$clone_dir" -c "$gitlab_instead_of_config" submodule update --init --recursive >/dev/null 2>&1; then
    echo "FAIL $path git submodule update failed after checkout" >&2
    return 1
  fi

  if command -v git-lfs >/dev/null 2>&1; then
    if ! git -C "$clone_dir" -c "$gitlab_instead_of_config" submodule foreach --recursive 'git lfs pull >/dev/null 2>&1 || exit 1' >/dev/null 2>&1; then
      echo "FAIL $path git lfs pull failed in a submodule after checkout" >&2
      return 1
    fi
  fi
}

if [[ ! -f "$manifest" ]]; then
  echo "manifest not found: $manifest" >&2
  exit 1
fi

if [[ ! -d "$root" ]]; then
  echo "root not found: $root" >&2
  exit 1
fi

import_count="$(yq eval '.imports | length' "$manifest")"
if [[ "$import_count" == "0" ]]; then
  echo "no imports defined in $manifest"
  exit 0
fi

workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT

failures=0

for ((i = 0; i < import_count; i++)); do
  path="$(yq eval -r ".imports[$i].path" "$manifest")"
  authoritative_source_raw="$(yq eval -r ".imports[$i].authoritative_source // \"upstream\"" "$manifest")"
  repo="$(yq eval -r ".imports[$i].repo" "$manifest")"
  commit="$(yq eval -r ".imports[$i].commit" "$manifest")"
  authoritative_source="$(normalize_authoritative_source "$authoritative_source_raw")"
  import_dir="$root/$path"
  clone_dir="$workdir/clone-$i"
  expected_dir="$workdir/expected-$i"
  diff_file="$workdir/diff-$i.txt"

  if [[ "$path" == "null" ]]; then
    echo "FAIL invalid manifest entry at index $i" >&2
    failures=1
    continue
  fi

  if [[ ! -d "$import_dir" ]]; then
    echo "FAIL $path missing import directory: $import_dir" >&2
    failures=1
    continue
  fi

  if [[ "$authoritative_source" != "upstream" ]]; then
    echo "SKIP $path authoritative_source=$authoritative_source"
    continue
  fi

  if [[ "$repo" == "null" || "$commit" == "null" ]]; then
    echo "FAIL invalid upstream manifest entry at index $i ($path)" >&2
    failures=1
    continue
  fi

  echo "Checking $path against $repo @ $commit"

  if ! git clone --quiet "$repo" "$clone_dir" >/dev/null 2>&1; then
    echo "FAIL $path unable to clone $repo" >&2
    failures=1
    continue
  fi

  if ! git -C "$clone_dir" -c advice.detachedHead=false checkout --quiet "$commit" >/dev/null 2>&1; then
    echo "FAIL $path unable to checkout $commit from $repo" >&2
    failures=1
    continue
  fi

  # Match umbrella working tree: LFS paths must be real files, not pointer stubs.
  if command -v git-lfs >/dev/null 2>&1; then
    if ! git -C "$clone_dir" lfs pull >/dev/null 2>&1; then
      echo "FAIL $path git lfs pull failed after checkout (needed for LFS-tracked files)" >&2
      failures=1
      continue
    fi
  fi

  if ! materialize_submodules "$clone_dir"; then
    failures=1
    continue
  fi

  mkdir -p "$expected_dir"
  (cd "$clone_dir" && tar cf - --exclude .git .) | (cd "$expected_dir" && tar xf -)

  if diff -qr --exclude .git "$expected_dir" "$import_dir" >"$diff_file" 2>&1; then
    echo "OK   $path matches $commit"
  else
    echo "FAIL $path drift detected relative to $commit" >&2
    cat "$diff_file" >&2
    failures=1
  fi
done

if [[ "$failures" -ne 0 ]]; then
  exit 1
fi

echo "All upstream synthetic imports match imports.yaml"
