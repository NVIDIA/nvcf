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
test "$(read_config "$default_manifest" ALLOW_ANONYMOUS)" = false ||
  fail "generic chart must leave allow anonymous off by default"
test "$(yq ea -r 'select(.kind == "Deployment") | [.spec.template.spec.containers[0].ports[] | select(.name == "metrics")] | length' "$default_manifest")" = 0 ||
  fail "generic chart must leave the metrics endpoint disabled by default"
test "$(yq ea -r '[select(.kind == "ServiceMonitor")] | length' "$default_manifest")" = 0 ||
  fail "generic chart must not create a ServiceMonitor by default"

helm template llm-api-gateway "$chart_dir" \
  --namespace nvcf \
  --set-string llmApiGateway.image.repository=example.invalid/llm-api-gateway \
  --set llmApiGateway.config.nvcfGrpcInsecure=true \
  --set llmApiGateway.config.nvcfGrpcAddr= \
  --set llmApiGateway.config.allowAnonymous=true \
  --set llmApiGateway.metrics.enabled=true \
  --set llmApiGateway.metrics.serviceMonitor.enabled=true \
  >"$override_manifest"

test "$(read_config "$override_manifest" NVCF_GRPC_INSECURE)" = true ||
  fail "explicit plaintext transport override did not reach the ConfigMap"
test "$(read_config "$override_manifest" ALLOW_ANONYMOUS)" = true ||
  fail "allow anonymous override did not reach the ConfigMap"
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

test "$(yq ea -r 'select(.kind == "ConfigMap" and .metadata.name == "llm-api-gateway") | .data | has("CALLER_KEYS_FILE")' "$default_manifest")" = false ||
  fail "generic chart must leave caller keys off by default"

caller_keys_manifest="$work_dir/caller-keys.yaml"
helm template llm-api-gateway "$chart_dir" \
  --namespace nvcf \
  --set-string llmApiGateway.image.repository=example.invalid/llm-api-gateway \
  --set llmApiGateway.config.nvcfGrpcAddr= \
  --set llmApiGateway.callerKeys.enabled=true \
  --set llmApiGateway.callerKeys.secretName=demo-caller-keys \
  >"$caller_keys_manifest"

test "$(read_config "$caller_keys_manifest" CALLER_KEYS_FILE)" = /etc/llm-api-gateway/caller-keys/caller-keys.yaml ||
  fail "caller keys opt-in did not set CALLER_KEYS_FILE"
test "$(yq ea -r 'select(.kind == "Deployment") | .spec.template.spec.volumes[] | select(.name == "caller-keys") | .secret.secretName + "/" + .secret.items[0].key + "/" + .secret.items[0].path' "$caller_keys_manifest")" = demo-caller-keys/caller-keys.yaml/caller-keys.yaml ||
  fail "caller keys opt-in did not mount the key file from the Secret"
test "$(yq ea -r 'select(.kind == "Deployment") | .spec.template.spec.containers[0].volumeMounts[] | select(.name == "caller-keys") | .mountPath + "/" + (.readOnly | tostring)' "$caller_keys_manifest")" = /etc/llm-api-gateway/caller-keys/true ||
  fail "caller keys opt-in did not mount the Secret read-only into the gateway"

if helm template llm-api-gateway "$chart_dir" \
  --namespace nvcf \
  --set-string llmApiGateway.image.repository=example.invalid/llm-api-gateway \
  --set llmApiGateway.callerKeys.enabled=true \
  >/dev/null 2>"$invalid_error"; then
  fail "caller keys opt-in without a Secret name should fail"
fi
grep -Fq 'llmApiGateway.callerKeys.secretName is required' "$invalid_error" ||
  fail "caller keys opt-in without a Secret name returned the wrong error"

echo "llm-api-gateway-default-ownership: all checks passed"
