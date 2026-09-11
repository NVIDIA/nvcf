#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
checker="$script_dir/verify-render-structure.py"
work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "verify-render-structure-detects-violations: $*" >&2
  exit 1
}

run_checker() {
  python3 "$checker" "$1" >/dev/null 2>&1
}

expect_fail() {
  local dir="$1"
  local name="$2"

  if run_checker "$dir"; then
    fail "checker accepted $name"
  fi
}

good_dir="$work_dir/good"
mkdir -p "$good_dir/release/templates"
cat >"$good_dir/release/templates/configmap.yaml" <<'YAML'
apiVersion: v1
kind: ConfigMap
metadata:
  name: stable
  namespace: nvcf
YAML
run_checker "$good_dir" || fail "checker rejected a valid manifest"

bad_yaml_dir="$work_dir/bad-yaml"
mkdir -p "$bad_yaml_dir/release/templates"
cat >"$bad_yaml_dir/release/templates/configmap.yaml" <<'YAML'
apiVersion: v1
kind: ConfigMap
metadata: [
YAML
expect_fail "$bad_yaml_dir" "invalid YAML"

glued_dir="$work_dir/glued-yaml"
mkdir -p "$glued_dir/release/templates"
cat >"$glued_dir/release/templates/configmap.yaml" <<'YAML'
apiVersion: v1
kind: ConfigMap
metadata:
  name: stable
data:
  key: value---
apiVersion: v1
kind: ConfigMap
metadata:
  name: second
YAML
expect_fail "$glued_dir" "glued YAML separators"

long_namespace_dir="$work_dir/long-namespace"
mkdir -p "$long_namespace_dir/release/templates"
cat >"$long_namespace_dir/release/templates/rolebinding.yaml" <<'YAML'
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: stable
subjects:
- kind: ServiceAccount
  name: stable
  namespace: namespace-name-that-is-longer-than-the-kubernetes-dns-label-limit
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: stable
YAML
expect_fail "$long_namespace_dir" "overlong namespace field"

bad_namespace_dir="$work_dir/bad-namespace"
mkdir -p "$bad_namespace_dir/release/templates"
cat >"$bad_namespace_dir/release/templates/configmap.yaml" <<'YAML'
apiVersion: v1
kind: ConfigMap
metadata:
  name: stable
  namespace: Bad_Namespace
YAML
expect_fail "$bad_namespace_dir" "invalid namespace field"

echo "verify-render-structure-detects-violations: all checks passed"
