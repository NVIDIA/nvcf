#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

: "${NVCF_PUBLISHED_CHART_REGISTRY:?NVCF_PUBLISHED_CHART_REGISTRY is required}"
: "${NVCF_PUBLISHED_CHART_REPOSITORY:?NVCF_PUBLISHED_CHART_REPOSITORY is required}"

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work_dir="$(mktemp -d)"
published_manifest="$work_dir/gateway-routes.yaml"
published_release="$work_dir/gateway-routes-release.json"
published_version=1.17.0
gateway_namespace=gateway
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "gateway-routes-published-chart: $*" >&2
  exit 1
}

source_args=(
  --state-values-set-string "global.helm.sources.registry=$NVCF_PUBLISHED_CHART_REGISTRY"
  --state-values-set-string "global.helm.sources.repository=$NVCF_PUBLISHED_CHART_REPOSITORY"
)
values_args=(
  --state-values-set-string global.workerEndpoints.llmRequestRouterAddress=https://llm-grpc.example.com:50071
  --state-values-set addons.llm.enabled=true
  --state-values-set addons.llm.pki.enabled=true
  --state-values-set-string addons.llm.pki.allowedDomains=cluster.local
  --state-values-set-string addons.llm.requestRouter.backendRouter.pylonGrpcDialAddress=https://llm-grpc.example.com:50071
  --state-values-set-string addons.llm.requestRouter.backendRouter.pylonReverseTunnelDialAddress=llm-quic.example.com:50072
  --state-values-set addons.llm.requestRouter.grpcTls.enabled=true
  --state-values-set-string addons.llm.requestRouter.grpcTls.mode=certManager
  --state-values-set-string addons.llm.requestRouter.grpcTls.secretName=llm-grpc-tls
  --state-values-set-string 'addons.llm.requestRouter.grpcTls.dnsNames[0]=llm-grpc.example.com'
  --state-values-set-string addons.llm.requestRouter.grpcTls.issuerRef.kind=ClusterIssuer
  --state-values-set-string addons.llm.requestRouter.grpcTls.issuerRef.name=nvcf-openbao-pki
  --state-values-set ingress.gatewayApi.enabled=true
  --state-values-set-string ingress.gatewayApi.controllerNamespace="$gateway_namespace"
  --state-values-set-string ingress.gatewayApi.gateways.shared.name=shared-gw
  --state-values-set-string ingress.gatewayApi.gateways.shared.namespace="$gateway_namespace"
  --state-values-set-string ingress.gatewayApi.gateways.grpc.name=grpc-gw
  --state-values-set-string ingress.gatewayApi.gateways.grpc.namespace="$gateway_namespace"
  --state-values-set ingress.gatewayApi.routes.llmWorker.enabled=true
  --state-values-set-string ingress.gatewayApi.routes.llmWorker.backend.namespace=nvcf
  --state-values-set-string ingress.gatewayApi.gateways.llmGrpc.name=llm-grpc-gw
  --state-values-set-string ingress.gatewayApi.gateways.llmGrpc.namespace="$gateway_namespace"
  --state-values-set-string ingress.gatewayApi.gateways.llmGrpc.listenerName=llm-grpc
  --state-values-set-string ingress.gatewayApi.gateways.llmQuic.name=llm-quic-gw
  --state-values-set-string ingress.gatewayApi.gateways.llmQuic.namespace="$gateway_namespace"
  --state-values-set-string ingress.gatewayApi.gateways.llmQuic.listenerName=llm-quic
)

(cd "$stack_dir" && HELMFILE_ENV=base HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" helmfile \
  --file helmfile.d/02-core.yaml.gotmpl \
  --environment default \
  "${source_args[@]}" \
  "${values_args[@]}" \
  --selector name=ingress \
  list --skip-charts --output json >"$published_release")

test "$(yq -r '.[0].chart' "$published_release")" = \
  'nvcf/nvcf-gateway-routes' ||
  fail "default chart source selected the wrong gateway-routes chart"
test "$(yq -r '.[0].version' "$published_release")" = "$published_version" ||
  fail "default chart source did not select gateway-routes $published_version"

(cd "$stack_dir" && HELMFILE_ENV=base HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" helmfile \
  --file helmfile.d/02-core.yaml.gotmpl \
  --environment default \
  "${source_args[@]}" \
  "${values_args[@]}" \
  --selector name=ingress \
  template >"$published_manifest")

assert_manifest_value() {
  local expression="$1"
  local expected="$2"
  local actual

  actual="$(yq ea -r "$expression" "$published_manifest")"
  test "$actual" = "$expected" ||
    fail "expected $expression to be $expected, got $actual"
}

assert_manifest_value \
  '[select(.kind == "GRPCRoute" and .metadata.name == "llm-worker-grpc" and .metadata.namespace == "gateway")] | length' \
  '1'
assert_manifest_value \
  'select(.kind == "GRPCRoute" and .metadata.name == "llm-worker-grpc" and .metadata.namespace == "gateway") | .spec.parentRefs[0].name' \
  'llm-grpc-gw'
assert_manifest_value \
  'select(.kind == "GRPCRoute" and .metadata.name == "llm-worker-grpc" and .metadata.namespace == "gateway") | .spec.parentRefs[0].sectionName' \
  'llm-grpc'
assert_manifest_value \
  'select(.kind == "GRPCRoute" and .metadata.name == "llm-worker-grpc" and .metadata.namespace == "gateway") | .spec.rules[0].backendRefs[0].name' \
  'llm-request-router-backend-router'
assert_manifest_value \
  'select(.kind == "GRPCRoute" and .metadata.name == "llm-worker-grpc" and .metadata.namespace == "gateway") | .spec.rules[0].backendRefs[0].namespace' \
  'nvcf'
assert_manifest_value \
  'select(.kind == "GRPCRoute" and .metadata.name == "llm-worker-grpc" and .metadata.namespace == "gateway") | .spec.rules[0].backendRefs[0].port' \
  '50071'
assert_manifest_value \
  '[select(.kind == "TCPRoute" and .metadata.name == "llm-worker-grpc" and .metadata.namespace == "gateway")] | length' \
  '0'

assert_manifest_value \
  '[select(.kind == "UDPRoute" and .metadata.name == "llm-worker-quic" and .metadata.namespace == "gateway")] | length' \
  '1'
assert_manifest_value \
  'select(.kind == "UDPRoute" and .metadata.name == "llm-worker-quic" and .metadata.namespace == "gateway") | .spec.parentRefs[0].name' \
  'llm-quic-gw'
assert_manifest_value \
  'select(.kind == "UDPRoute" and .metadata.name == "llm-worker-quic" and .metadata.namespace == "gateway") | .spec.parentRefs[0].sectionName' \
  'llm-quic'
assert_manifest_value \
  'select(.kind == "UDPRoute" and .metadata.name == "llm-worker-quic" and .metadata.namespace == "gateway") | .spec.rules[0].backendRefs[0].port' \
  '50072'

assert_manifest_value \
  '[select(.kind == "Certificate" and .metadata.name == "llm-grpc-tls" and .metadata.namespace == "gateway")] | length' \
  '1'
assert_manifest_value \
  'select(.kind == "Certificate" and .metadata.name == "llm-grpc-tls" and .metadata.namespace == "gateway") | .spec.secretName' \
  'llm-grpc-tls'
assert_manifest_value \
  'select(.kind == "Certificate" and .metadata.name == "llm-grpc-tls" and .metadata.namespace == "gateway") | .spec.dnsNames[0]' \
  'llm-grpc.example.com'
assert_manifest_value \
  'select(.kind == "Certificate" and .metadata.name == "llm-grpc-tls" and .metadata.namespace == "gateway") | .spec.issuerRef.kind' \
  'ClusterIssuer'
assert_manifest_value \
  'select(.kind == "Certificate" and .metadata.name == "llm-grpc-tls" and .metadata.namespace == "gateway") | .spec.issuerRef.name' \
  'nvcf-openbao-pki'

assert_manifest_value \
  '[select(.kind == "BackendTrafficPolicy" and .metadata.name == "llm-worker-grpc-streams" and .metadata.namespace == "gateway")] | length' \
  '1'
assert_manifest_value \
  'select(.kind == "BackendTrafficPolicy" and .metadata.name == "llm-worker-grpc-streams" and .metadata.namespace == "gateway") | .spec.targetRefs[0].kind' \
  'GRPCRoute'
assert_manifest_value \
  'select(.kind == "BackendTrafficPolicy" and .metadata.name == "llm-worker-grpc-streams" and .metadata.namespace == "gateway") | .spec.targetRefs[0].name' \
  'llm-worker-grpc'
assert_manifest_value \
  'select(.kind == "BackendTrafficPolicy" and .metadata.name == "llm-worker-grpc-streams" and .metadata.namespace == "gateway") | .spec.timeout.http.requestTimeout' \
  '0s'

echo "gateway-routes-published-chart: all checks passed"
