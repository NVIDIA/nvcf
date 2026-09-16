#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

chart_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/../llm-api-gateway" && pwd)"
work_dir="$(mktemp -d)"
default_manifest="$work_dir/default.yaml"
override_manifest="$work_dir/override.yaml"
invalid_error="$work_dir/invalid.err"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "llm-api-gateway-default-ownership: $*" >&2
  exit 1
}

read_config() {
  local manifest="$1" key="$2"
  yq ea -r "select(.kind == \"ConfigMap\" and .metadata.name == \"llm-api-gateway\") | .data.${key}" "$manifest"
}

helm template llm-api-gateway "$chart_dir" \
  --namespace nvcf \
  --set-string llmApiGateway.image.repository=example.invalid/llm-api-gateway \
  >"$default_manifest"

test "$(read_config "$default_manifest" NVCF_GRPC_INSECURE)" = false ||
  fail "generic chart must default to verified NVCF gRPC transport"
test "$(yq ea -r 'select(.kind == "Deployment") | [.spec.template.spec.containers[0].ports[] | select(.name == "metrics")] | length' "$default_manifest")" = 0 ||
  fail "generic chart must leave the metrics endpoint disabled by default"
test "$(yq ea -r '[select(.kind == "ServiceMonitor")] | length' "$default_manifest")" = 0 ||
  fail "generic chart must not create a ServiceMonitor by default"

helm template llm-api-gateway "$chart_dir" \
  --namespace nvcf \
  --set-string llmApiGateway.image.repository=example.invalid/llm-api-gateway \
  --set llmApiGateway.config.nvcfGrpcInsecure=true \
  --set llmApiGateway.metrics.enabled=true \
  --set llmApiGateway.metrics.serviceMonitor.enabled=true \
  >"$override_manifest"

test "$(read_config "$override_manifest" NVCF_GRPC_INSECURE)" = true ||
  fail "explicit plaintext transport override did not reach the ConfigMap"
test "$(yq ea -r 'select(.kind == "Deployment") | [.spec.template.spec.containers[0].ports[] | select(.name == "metrics")] | length' "$override_manifest")" = 1 ||
  fail "explicit metrics override did not expose the metrics container port"
test "$(yq ea -r '[select(.kind == "ServiceMonitor" and .metadata.name == "llm-api-gateway-metrics")] | length' "$override_manifest")" = 1 ||
  fail "explicit ServiceMonitor opt-in did not render exactly one resource"

if helm template llm-api-gateway "$chart_dir" \
  --namespace nvcf \
  --set-string llmApiGateway.image.repository=example.invalid/llm-api-gateway \
  --set llmApiGateway.metrics.serviceMonitor.enabled=true \
  >/dev/null 2>"$invalid_error"; then
  fail "ServiceMonitor opt-in without metrics should fail"
fi
grep -Fq 'llmApiGateway.metrics.enabled must be true' "$invalid_error" ||
  fail "invalid ServiceMonitor configuration returned the wrong error"

echo "llm-api-gateway-default-ownership: all checks passed"
