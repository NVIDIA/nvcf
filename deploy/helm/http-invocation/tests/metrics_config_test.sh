#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

chart_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../nvcf-invocation-service" && pwd)"
work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "metrics_config_test.sh: $*" >&2
  exit 1
}

render() {
  local output_file="$1"
  shift
  helm template invocation-service "$chart_root" \
    --namespace nvcf \
    --set-string invocation.image.registry=registry.example.test \
    --set-string invocation.image.repository=nvcf-invocation-service \
    "$@" >"$output_file"
}

assert_yq_eq() {
  local file="$1"
  local expression="$2"
  local expected="$3"
  local actual

  actual="$(yq ea -r "$expression" "$file")"
  [[ "$actual" == "$expected" ]] ||
    fail "expected $expression to equal '$expected', got '$actual'"
}

default_manifest="$work_dir/default.yaml"
enabled_manifest="$work_dir/enabled.yaml"
custom_manifest="$work_dir/custom.yaml"
monitor_manifest="$work_dir/monitor.yaml"
validation_error="$work_dir/validation-error.txt"
reserved_env_error="$work_dir/reserved-env-error.txt"
port_validation_error="$work_dir/port-validation-error.txt"

render "$default_manifest"
assert_yq_eq \
  "$default_manifest" \
  '[select(.kind == "Service") | .spec.ports[] | select(.name == "metrics")] | length' \
  0
assert_yq_eq \
  "$default_manifest" \
  '[select(.kind == "Deployment") | .spec.template.spec.containers[].ports[] | select(.name == "metrics")] | length' \
  0
assert_yq_eq \
  "$default_manifest" \
  'select(.kind == "ConfigMap" and .metadata.name == "invocation-service-helm-nvcf-invocation-service-env") | .data.APP_CONFIG // "absent"' \
  absent
assert_yq_eq \
  "$default_manifest" \
  '[select(.kind == "ServiceMonitor")] | length' \
  0

render "$enabled_manifest" --set invocation.metrics.enabled=true
assert_yq_eq \
  "$enabled_manifest" \
  'select(.kind == "ConfigMap" and .metadata.name == "invocation-service-helm-nvcf-invocation-service-env") | .data.APP_CONFIG' \
  /etc/nvcf-invocation/settings.yaml
assert_yq_eq \
  "$enabled_manifest" \
  'select(.kind == "ConfigMap" and .metadata.name == "invocation-service-helm-nvcf-invocation-service-env") | .data.APP_CONFIG_YAML | from_yaml | .server.metrics.exporters[0].endpoint' \
  http://0.0.0.0:41337
assert_yq_eq \
  "$enabled_manifest" \
  'select(.kind == "Deployment") | .spec.template.spec.containers[] | select(.name == "helm-nvcf-invocation-service") | .volumeMounts[] | select(.name == "app-config") | .mountPath' \
  /etc/nvcf-invocation
assert_yq_eq \
  "$enabled_manifest" \
  'select(.kind == "Service") | .spec.ports[] | select(.name == "metrics") | .port' \
  41337

render "$custom_manifest" \
  --set invocation.metrics.enabled=true \
  --set-string invocation.metrics.bindAddress=127.0.0.2 \
  --set invocation.metrics.port=4242
assert_yq_eq \
  "$custom_manifest" \
  'select(.kind == "ConfigMap" and .metadata.name == "invocation-service-helm-nvcf-invocation-service-env") | .data.APP_CONFIG_YAML | from_yaml | .server.metrics.exporters[0].endpoint' \
  http://127.0.0.2:4242
assert_yq_eq \
  "$custom_manifest" \
  'select(.kind == "Deployment") | .spec.template.spec.containers[] | select(.name == "helm-nvcf-invocation-service") | .ports[] | select(.name == "metrics") | .containerPort' \
  4242

render "$monitor_manifest" \
  --set invocation.metrics.enabled=true \
  --set invocation.metrics.serviceMonitor.enabled=true
assert_yq_eq \
  "$monitor_manifest" \
  'select(.kind == "ServiceMonitor") | .spec.endpoints[0].port' \
  metrics
assert_yq_eq \
  "$monitor_manifest" \
  'select(.kind == "ServiceMonitor") | .spec.endpoints[0].path' \
  /metrics

if helm template invocation-service "$chart_root" \
  --namespace nvcf \
  --set-string invocation.image.registry=registry.example.test \
  --set-string invocation.image.repository=nvcf-invocation-service \
  --set invocation.metrics.serviceMonitor.enabled=true \
  >/dev/null 2>"$validation_error"; then
  fail "expected ServiceMonitor without metrics exposure to fail"
fi
grep -Fq \
  'invocation.metrics.enabled must be true when invocation.metrics.serviceMonitor.enabled is true' \
  "$validation_error" || fail "missing ServiceMonitor validation error"

if helm template invocation-service "$chart_root" \
  --namespace nvcf \
  --set-string invocation.image.registry=registry.example.test \
  --set-string invocation.image.repository=nvcf-invocation-service \
  --set invocation.metrics.enabled=true \
  --set-string invocation.env.APP_CONFIG=/tmp/custom.yaml \
  >/dev/null 2>"$reserved_env_error"; then
  fail "expected metrics exposure with a reserved APP_CONFIG override to fail"
fi
grep -Fq \
  'invocation.env.APP_CONFIG is reserved when invocation.metrics.enabled is true' \
  "$reserved_env_error" || fail "missing reserved APP_CONFIG validation error"

for invalid_port in 0 65536 invalid; do
  if helm template invocation-service "$chart_root" \
    --namespace nvcf \
    --set-string invocation.image.registry=registry.example.test \
    --set-string invocation.image.repository=nvcf-invocation-service \
    --set invocation.metrics.enabled=true \
    --set-string invocation.metrics.port="$invalid_port" \
    >/dev/null 2>"$port_validation_error"; then
    fail "expected invalid metrics port '$invalid_port' to fail"
  fi
  grep -Fq \
    'invocation.metrics.port must be an integer between 1 and 65535 when invocation.metrics.enabled is true' \
    "$port_validation_error" || fail "missing metrics port validation error for '$invalid_port'"
done

echo "metrics_config_test.sh: invocation metrics exposure is configurable"
