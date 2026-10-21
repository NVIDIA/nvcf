#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

chart_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../nvcf-invocation-service" && pwd)"
work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT

render() {
  local output_file="$1"
  shift
  helm template invocation-service "$chart_root" \
    --namespace nvcf \
    --set-string invocation.image.registry=registry.example.test \
    --set-string invocation.image.repository=nvcf-invocation-service \
    "$@" >"$output_file"
}

assert_probe() {
  local manifest="$1" probe="$2" expected="$3" actual
  actual="$(yq ea -o=json -I=0 \
    "select(.kind == \"Deployment\") | .spec.template.spec.containers[] | select(.name == \"helm-nvcf-invocation-service\") | .$probe | sort_keys(..)" \
    "$manifest")"
  if [[ "$actual" != "$expected" ]]; then
    echo "probes_test.sh: $probe expected $expected, got $actual" >&2
    exit 1
  fi
}

render "$work_dir/default.yaml"
for probe in livenessProbe readinessProbe startupProbe; do
  assert_probe "$work_dir/default.yaml" "$probe" \
    "$(yq -o=json -I=0 ".invocation.$probe | sort_keys(..)" "$chart_root/values.yaml")"
done

cat >"$work_dir/custom-values.yaml" <<'YAML'
invocation:
  startupProbe:
    httpGet:
      path: /health
      port: http
    failureThreshold: 30
    periodSeconds: 2
  readinessProbe:
    httpGet:
      path: /ready
      port: http
    timeoutSeconds: 5
    successThreshold: 2
  livenessProbe:
    httpGet:
      path: /live
      port: http
    failureThreshold: 4
YAML
render "$work_dir/custom.yaml" -f "$work_dir/custom-values.yaml"
for probe in startupProbe readinessProbe livenessProbe; do
  assert_probe "$work_dir/custom.yaml" "$probe" \
    "$(yq -o=json -I=0 ".invocation.$probe | sort_keys(..)" "$work_dir/custom-values.yaml")"
done

render "$work_dir/disabled.yaml" \
  --set invocation.startupProbe=null \
  --set invocation.readinessProbe=null \
  --set invocation.livenessProbe=null
for probe in startupProbe readinessProbe livenessProbe; do
  assert_probe "$work_dir/disabled.yaml" "$probe" null
done

echo "probes_test.sh: defaults, overrides and disabled probes pass"
