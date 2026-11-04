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
for kv in ENABLED=false TTL=10m LOOKUP_TIMEOUT=20ms LOCAL_MAX_ENTRIES=100000; do
  test "$(read_config "$default_manifest" "STARGATE_LAST_CLUSTER_${kv%%=*}")" = "${kv#*=}" ||
    fail "STARGATE_LAST_CLUSTER_${kv%%=*} default must be ${kv#*=}"
done
test "$(read_config "$default_manifest" NVCF_DECISION_IMAGE_TOKENS)" = 1024 ||
  fail "NVCF_DECISION_IMAGE_TOKENS default must be 1024"
test "$(read_config "$default_manifest" NVCF_GATEWAY_MAX_REQUEST_BODY_BYTES)" = 52428800 ||
  fail "NVCF_GATEWAY_MAX_REQUEST_BODY_BYTES default must be 52428800"

helm template llm-api-gateway "$chart_dir" \
  --namespace nvcf \
  --set-string llmApiGateway.image.repository=example.invalid/llm-api-gateway \
  --set llmApiGateway.config.nvcfGrpcInsecure=true \
  --set llmApiGateway.config.decisionImageTokens=2048 \
  --set llmApiGateway.config.maxRequestBodyBytes=0 \
  --set llmApiGateway.metrics.enabled=true \
  --set llmApiGateway.metrics.serviceMonitor.enabled=true \
  --set llmApiGateway.config.lastCluster.enabled=true \
  --set llmApiGateway.config.lastCluster.localMaxEntries=1000000 \
  >"$override_manifest"

test "$(read_config "$override_manifest" NVCF_GRPC_INSECURE)" = true ||
  fail "explicit plaintext transport override did not reach the ConfigMap"
test "$(read_config "$override_manifest" NVCF_DECISION_IMAGE_TOKENS)" = 2048 ||
  fail "decisionImageTokens override must render as NVCF_DECISION_IMAGE_TOKENS"
test "$(read_config "$override_manifest" NVCF_GATEWAY_MAX_REQUEST_BODY_BYTES)" = 0 ||
  fail "maxRequestBodyBytes=0 must render as NVCF_GATEWAY_MAX_REQUEST_BODY_BYTES 0"
# 1000000 checks that a large cap renders as a plain integer.
for kv in ENABLED=true LOCAL_MAX_ENTRIES=1000000; do
  test "$(read_config "$override_manifest" "STARGATE_LAST_CLUSTER_${kv%%=*}")" = "${kv#*=}" ||
    fail "STARGATE_LAST_CLUSTER_${kv%%=*} override must render as ${kv#*=}"
done
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

if helm template llm-api-gateway "$chart_dir" \
  --namespace nvcf \
  --set-string llmApiGateway.image.repository=example.invalid/llm-api-gateway \
  --set llmApiGateway.config.decisionImageTokens=-5 \
  >/dev/null 2>"$invalid_error"; then
  fail "a negative decisionImageTokens should fail to render"
fi
grep -Fq 'llmApiGateway.config.decisionImageTokens must be 0 or more' "$invalid_error" ||
  fail "negative decisionImageTokens returned the wrong error"

if helm template llm-api-gateway "$chart_dir" \
  --namespace nvcf \
  --set-string llmApiGateway.image.repository=example.invalid/llm-api-gateway \
  --set llmApiGateway.config.maxRequestBodyBytes=-1 \
  >/dev/null 2>"$invalid_error"; then
  fail "a negative maxRequestBodyBytes should fail to render"
fi
grep -Fq 'llmApiGateway.config.maxRequestBodyBytes must be a whole number from 0 to 9223372036854775807' "$invalid_error" ||
  fail "negative maxRequestBodyBytes returned the wrong error"

printf 'llmApiGateway:\n  config:\n    maxRequestBodyBytes: 10000000000000000000\n' >"$work_dir/over-int64.yaml"
for bad in "--set-string llmApiGateway.config.maxRequestBodyBytes=50Mi" \
  "--set llmApiGateway.config.maxRequestBodyBytes=1.5" \
  "--values $work_dir/over-int64.yaml"; do
  # shellcheck disable=SC2086
  if helm template llm-api-gateway "$chart_dir" \
    --namespace nvcf \
    --set-string llmApiGateway.image.repository=example.invalid/llm-api-gateway \
    $bad >/dev/null 2>"$invalid_error"; then
    fail "maxRequestBodyBytes must reject: $bad"
  fi
  grep -Fq 'llmApiGateway.config.maxRequestBodyBytes must be a whole number from 0 to 9223372036854775807' "$invalid_error" ||
    fail "maxRequestBodyBytes ($bad) returned the wrong error"
done

printf 'llmApiGateway:\n  config:\n    maxRequestBodyBytes: 9223372036854775807\n' >"$work_dir/max-int64.yaml"
helm template llm-api-gateway "$chart_dir" \
  --namespace nvcf \
  --set-string llmApiGateway.image.repository=example.invalid/llm-api-gateway \
  --values "$work_dir/max-int64.yaml" \
  >"$override_manifest"
test "$(read_config "$override_manifest" NVCF_GATEWAY_MAX_REQUEST_BODY_BYTES)" = 9223372036854775807 ||
  fail "maxRequestBodyBytes 9223372036854775807 in a values file must render unchanged"

# An unset value, as left by helm upgrade --reuse-values from an older chart,
# still renders the 50 MiB default because older images have none.
helm template llm-api-gateway "$chart_dir" \
  --namespace nvcf \
  --set-string llmApiGateway.image.repository=example.invalid/llm-api-gateway \
  --set llmApiGateway.config.maxRequestBodyBytes=null \
  >"$override_manifest"
test "$(read_config "$override_manifest" NVCF_GATEWAY_MAX_REQUEST_BODY_BYTES)" = 52428800 ||
  fail "an unset maxRequestBodyBytes must render the 50 MiB default"

if ! helm template llm-api-gateway "$chart_dir" \
  --namespace nvcf \
  --set-string llmApiGateway.image.repository=example.invalid/llm-api-gateway \
  --set llmApiGateway.config.decisionImageTokens=0 \
  >"$override_manifest"; then
  fail "decisionImageTokens=0 must render"
fi
test "$(read_config "$override_manifest" NVCF_DECISION_IMAGE_TOKENS)" = 0 ||
  fail "decisionImageTokens=0 must render as NVCF_DECISION_IMAGE_TOKENS 0"

echo "llm-api-gateway-default-ownership: all checks passed"
