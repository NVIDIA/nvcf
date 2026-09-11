#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
stack_dir="$(cd "$script_dir/.." && pwd)"
renderer="$stack_dir/renderers/control-plane-owner-label.sh"
owner_label="nvcf.nvidia.com/control-plane-owner"
work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "verify-control-plane-owner-label-renderer: $*" >&2
  exit 1
}

command -v diff >/dev/null 2>&1 || fail "diff is required"
command -v yq >/dev/null 2>&1 || fail "yq is required"
test -x "$renderer" || fail "renderer is not executable: $renderer"

input="$work_dir/input.yaml"
output="$work_dir/output.yaml"
rerendered="$work_dir/rerendered.yaml"

cat >"$input" <<'YAML'
apiVersion: v1
kind: ConfigMap
metadata:
  name: first
  labels:
    app.kubernetes.io/name: first
spec:
  selector:
    matchLabels:
      app.kubernetes.io/name: first
  template:
    metadata:
      labels:
        app.kubernetes.io/name: first
---
apiVersion: v1
kind: Secret
metadata:
  name: second
  annotations:
    note: labels-can-come-after-other-metadata
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: third
  labels:
    nvcf.nvidia.com/control-plane-owner: stale
    app.kubernetes.io/name: third
---
apiVersion: v1
kind: ResourceQuota
metadata:
  name: nvca-operator
  namespace: nvca-operator
spec: {}
---
# Source: nvcf-nvca-crds/templates/nvcfbackends-crd.yaml
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: nvcfbackends.nvcf.nvidia.io
spec: {}
---
# Source: helm-nvca-operator/templates/crds/nvidia.io_nvcfbackends_crd.yaml
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: nvcfbackends.nvcf.nvidia.io
spec: {}
---
apiVersion: scheduling.run.ai/v2
kind: Queue
metadata:
    name: fourth
    annotations:
        note: four-space-metadata-children
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: nvca-operator
spec:
  selector:
    matchLabels:
      app.kubernetes.io/name: nvca-operator
  template:
    metadata:
      labels:
        app.kubernetes.io/name: nvca-operator
    spec:
      initContainers:
      - name: cluster-validator
        env:
        - name: EXISTING_INIT_VAR
          value: untouched
      containers:
      - name: nvca-operator
        env:
        - name: POD_NAME
          value: operator-pod
      - name: nvca-mirror
        env:
        - name: LOG_LEVEL
          value: info
YAML

"$renderer" --owner plane-a <"$input" >"$output"

for object in ConfigMap/first Secret/second ServiceAccount/third ResourceQuota/nvca-operator CustomResourceDefinition/nvcfbackends.nvcf.nvidia.io Queue/fourth Deployment/nvca-operator; do
  object_kind="${object%%/*}"
  object_name="${object#*/}"
  actual="$(
    OBJECT_KIND="$object_kind" \
    OWNER_LABEL="$owner_label" yq ea -r '
      select(.kind == strenv(OBJECT_KIND) and .metadata.name == "'"$object_name"'") |
      .metadata.labels[strenv(OWNER_LABEL)]
    ' "$output"
  )"
  [[ "$actual" == "plane-a" ]] ||
    fail "expected $object owner label to be plane-a, got $actual"
done

crd_count="$(
  yq ea -r '
    select(.kind == "CustomResourceDefinition" and .metadata.name == "nvcfbackends.nvcf.nvidia.io") |
    .metadata.name
  ' "$output" | wc -l | tr -d ' '
)"
[[ "$crd_count" == "1" ]] ||
  fail "expected only the shared nvcfbackends CRD to remain after rendering, got $crd_count"

rq_namespace="$(
  yq ea -r '
    select(.kind == "ResourceQuota" and .metadata.name == "nvca-operator") |
    .metadata.namespace
  ' "$output"
)"
[[ "$rq_namespace" == "plane-a-nvca-operator" ]] ||
  fail "expected nvca-operator ResourceQuota namespace to be plane-a-nvca-operator, got $rq_namespace"

leaked="$(
  OWNER_LABEL="$owner_label" yq ea -r '
    select(tag == "!!map") |
    [
      ((.spec.selector.matchLabels // {})[strenv(OWNER_LABEL)]),
      ((.spec.template.metadata.labels // {})[strenv(OWNER_LABEL)])
    ] |
    .[] |
    select(. != null)
  ' "$output"
)"
[[ -z "$leaked" ]] ||
  fail "owner label leaked into a selector or pod template label"

for container_name in nvca-operator nvca-mirror; do
  actual="$(
    CONTAINER_NAME="$container_name" yq ea -r '
      select(.kind == "Deployment" and .metadata.name == "nvca-operator") |
      .spec.template.spec.containers[] |
      select(.name == strenv(CONTAINER_NAME)) |
      (.env // [])[] |
      select(.name == "NVCF_CONTROL_PLANE_OWNER") |
      .value
    ' "$output"
  )"
  [[ "$actual" == "plane-a" ]] ||
    fail "expected $container_name to receive NVCF_CONTROL_PLANE_OWNER=plane-a, got $actual"
done

init_container_owner_env="$(
  yq ea -r '
    select(.kind == "Deployment" and .metadata.name == "nvca-operator") |
    .spec.template.spec.initContainers[] |
    (.env // [])[] |
    select(.name == "NVCF_CONTROL_PLANE_OWNER") |
    .value
  ' "$output"
)"
[[ -z "$init_container_owner_env" ]] ||
  fail "owner env leaked into init container"

"$renderer" --owner plane-a <"$output" >"$rerendered"
diff -u "$output" "$rerendered" >/dev/null ||
  fail "renderer is not idempotent"

if "$renderer" --owner plane_a <"$input" >/dev/null 2>&1; then
  fail "renderer accepted an owner with an underscore"
fi

max_owner="aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
too_long_owner="${max_owner}a"
if ! "$renderer" --owner "$max_owner" <"$input" >/dev/null 2>&1; then
  fail "renderer rejected a 30-character owner"
fi

if "$renderer" --owner "$too_long_owner" <"$input" >/dev/null 2>&1; then
  fail "renderer accepted an owner longer than 30 characters"
fi

echo "verify-control-plane-owner-label-renderer: all checks passed"
