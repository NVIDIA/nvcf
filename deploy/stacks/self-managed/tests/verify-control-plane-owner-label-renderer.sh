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
apiVersion: scheduling.run.ai/v2
kind: Queue
metadata:
    name: fourth
    annotations:
        note: four-space-metadata-children
YAML

"$renderer" --owner plane-a <"$input" >"$output"

for object_name in first second third fourth; do
  actual="$(
    OWNER_LABEL="$owner_label" yq ea -r '
      select(.metadata.name == "'"$object_name"'") |
      .metadata.labels[strenv(OWNER_LABEL)]
    ' "$output"
  )"
  [[ "$actual" == "plane-a" ]] ||
    fail "expected $object_name owner label to be plane-a, got $actual"
done

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
