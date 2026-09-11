#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
stack_dir="$(cd "$script_dir/.." && pwd)"
helper="$stack_dir/renderers/apply-control-plane-owner-labels-to-output-dir.sh"
owner_label="nvcf.nvidia.com/control-plane-owner"
work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "verify-control-plane-owner-output-dir-env: $*" >&2
  exit 1
}

command -v yq >/dev/null 2>&1 || fail "yq is required"
test -x "$helper" || fail "helper is not executable: $helper"

mkdir -p \
  "$work_dir/02-core.yaml-api/templates" \
  "$work_dir/01-dependencies.yaml-cert-manager/templates"

cat >"$work_dir/02-core.yaml-api/templates/configmap.yaml" <<'YAML'
apiVersion: v1
kind: ConfigMap
metadata:
  name: api
YAML

cat >"$work_dir/01-dependencies.yaml-cert-manager/templates/configmap.yaml" <<'YAML'
apiVersion: v1
kind: ConfigMap
metadata:
  name: cert-manager
YAML
touch "$work_dir/02-core.yaml-api/templates/empty.yaml"

NVCF_CONTROL_PLANE_OWNER=plane-a "$helper" "$work_dir" >/dev/null

actual_control="$(
  OWNER_LABEL="$owner_label" yq -r '.metadata.labels[strenv(OWNER_LABEL)]' \
    "$work_dir/02-core.yaml-api/templates/configmap.yaml"
)"
[[ "$actual_control" == "plane-a" ]] ||
  fail "expected control-plane file owner to be plane-a, got $actual_control"

actual_shared="$(
  OWNER_LABEL="$owner_label" yq -r '.metadata.labels[strenv(OWNER_LABEL)]' \
    "$work_dir/01-dependencies.yaml-cert-manager/templates/configmap.yaml"
)"
[[ "$actual_shared" == "shared" ]] ||
  fail "expected cert-manager owner to stay shared, got $actual_shared"

if [[ -e "$work_dir/02-core.yaml-api/templates/empty.yaml" ]]; then
  fail "expected empty rendered YAML files to be removed"
fi

echo "verify-control-plane-owner-output-dir-env: all checks passed"
