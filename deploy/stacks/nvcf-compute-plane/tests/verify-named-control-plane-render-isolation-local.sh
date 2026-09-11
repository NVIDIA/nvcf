#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
stack_dir="$(cd "$script_dir/.." && pwd)"
work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "verify-named-control-plane-render-isolation-local: $*" >&2
  exit 1
}

command -v make >/dev/null 2>&1 || fail "make is required"
command -v python3 >/dev/null 2>&1 || fail "python3 is required"

render_owner() {
  local owner="$1"
  local cache_dir="$work_dir/cache-$owner"

  mkdir -p "$cache_dir/helm-cache" \
    "$cache_dir/helm-config" \
    "$cache_dir/helm-data" \
    "$cache_dir/helm-repository-cache" \
    "$cache_dir/helmfile-cache"
  printf 'repositories: []\n' >"$cache_dir/repositories.yaml"

  HELM_CACHE_HOME="$cache_dir/helm-cache" \
    HELM_CONFIG_HOME="$cache_dir/helm-config" \
    HELM_DATA_HOME="$cache_dir/helm-data" \
    HELM_REPOSITORY_CACHE="$cache_dir/helm-repository-cache" \
    HELM_REPOSITORY_CONFIG="$cache_dir/repositories.yaml" \
    HELM_REGISTRY_CONFIG="$cache_dir/registry.json" \
    HELMFILE_CACHE_HOME="$cache_dir/helmfile-cache" \
    make -C "$stack_dir" render-local \
      DEV_MODE=1 \
      DIST_DIR="$work_dir/$owner-dist" \
      NVCF_CONTROL_PLANE_OWNER="$owner" \
      NVCF_ALPHA_NAMED_CONTROL_PLANE=true
}

render_owner plane-a
render_owner plane-b

"$script_dir/verify-named-control-plane-render-isolation.sh" \
  --allow-shared-identity-file "$script_dir/named-control-plane-shared-identities.txt" \
  --render "plane-a=$work_dir/plane-a-dist/out" \
  --render "plane-b=$work_dir/plane-b-dist/out"

echo "verify-named-control-plane-render-isolation-local: all checks passed"
