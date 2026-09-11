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
  "$work_dir/01-dependencies.yaml-kai-scheduler/templates" \
  "$work_dir/02-nvca.yaml-nvca-operator/templates"

cat >"$work_dir/01-dependencies.yaml-kai-scheduler/templates/configmap.yaml" <<'YAML'
apiVersion: v1
kind: ConfigMap
metadata:
  name: kai
YAML

cat >"$work_dir/02-nvca.yaml-nvca-operator/templates/configmap.yaml" <<'YAML'
apiVersion: v1
kind: ConfigMap
metadata:
  name: nvca
YAML
touch "$work_dir/02-nvca.yaml-nvca-operator/templates/empty.yaml"

NVCF_CONTROL_PLANE_OWNER=plane-a "$helper" "$work_dir" >/dev/null

actual_shared="$(
  OWNER_LABEL="$owner_label" yq -r '.metadata.labels[strenv(OWNER_LABEL)]' \
    "$work_dir/01-dependencies.yaml-kai-scheduler/templates/configmap.yaml"
)"
[[ "$actual_shared" == "shared" ]] ||
  fail "expected dependency file owner to stay shared, got $actual_shared"

actual_plane="$(
  OWNER_LABEL="$owner_label" yq -r '.metadata.labels[strenv(OWNER_LABEL)]' \
    "$work_dir/02-nvca.yaml-nvca-operator/templates/configmap.yaml"
)"
[[ "$actual_plane" == "plane-a" ]] ||
  fail "expected nvca file owner to be plane-a, got $actual_plane"

if [[ -e "$work_dir/02-nvca.yaml-nvca-operator/templates/empty.yaml" ]]; then
  fail "expected empty rendered YAML files to be removed"
fi

echo "verify-control-plane-owner-output-dir-env: all checks passed"
