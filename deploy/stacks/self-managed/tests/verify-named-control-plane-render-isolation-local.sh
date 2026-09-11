#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "verify-named-control-plane-render-isolation-local: $*" >&2
  exit 1
}

command -v helm >/dev/null 2>&1 || fail "helm is required"
command -v helmfile >/dev/null 2>&1 || fail "helmfile is required"
command -v python3 >/dev/null 2>&1 || fail "python3 is required"
command -v yq >/dev/null 2>&1 || fail "yq is required"

for owner in plane-a plane-b; do
  render_dir="$work_dir/$owner"
  NVCF_CONTROL_PLANE_OWNER="$owner" \
    NVCF_ALPHA_NAMED_CONTROL_PLANE=true \
    "$script_dir/render-local-golden.sh" \
      "$render_dir" \
      '{{ .OutputDir }}/{{ .State.BaseName }}-{{ .Release.Name }}'
done

"$script_dir/verify-named-control-plane-render-isolation.sh" \
  --allow-shared-identity-file "$script_dir/named-control-plane-shared-identities.txt" \
  --render "plane-a=$work_dir/plane-a" \
  --render "plane-b=$work_dir/plane-b"

echo "verify-named-control-plane-render-isolation-local: all checks passed"
