#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

: "${NVCF_PUBLISHED_CHART_REGISTRY:?NVCF_PUBLISHED_CHART_REGISTRY is required}"
: "${NVCF_PUBLISHED_CHART_REPOSITORY:?NVCF_PUBLISHED_CHART_REPOSITORY is required}"

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work_dir="$(mktemp -d)"
published_manifest="$work_dir/llm-request-router.yaml"
published_release="$work_dir/llm-request-router-release.json"
published_version=1.12.0
expected_stargate_image="${NVCF_PUBLISHED_CHART_REGISTRY}/${NVCF_PUBLISHED_CHART_REPOSITORY}/stargate:0.14.1"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "llm-router-published-chart: $*" >&2
  exit 1
}

source_args=(
  --state-values-set-string "global.helm.sources.registry=$NVCF_PUBLISHED_CHART_REGISTRY"
  --state-values-set-string "global.helm.sources.repository=$NVCF_PUBLISHED_CHART_REPOSITORY"
)
values_args=(
  --state-values-set-string "global.image.registry=$NVCF_PUBLISHED_CHART_REGISTRY"
  --state-values-set-string "global.image.repository=$NVCF_PUBLISHED_CHART_REPOSITORY"
  --state-values-set-string global.workerEndpoints.llmRequestRouterAddress=https://llm-grpc.example.com:50071
  --state-values-set addons.llm.enabled=true
  --state-values-set addons.llm.pki.enabled=true
  --state-values-set-string addons.llm.pki.allowedDomains=cluster.local
  --state-values-set-string 'addons.llm.pki.dnsNames[0]=llm-request-router.nvcf.svc.cluster.local'
  --state-values-set-string 'addons.llm.pki.dnsNames[1]=*.llm-request-router-headless.nvcf.svc.cluster.local'
  --state-values-set-string addons.llm.requestRouter.backendRouter.pylonGrpcDialAddress=https://llm-grpc.example.com:50071
  --state-values-set-string addons.llm.requestRouter.backendRouter.pylonReverseTunnelDialAddress=llm-quic.example.com:50072
  --state-values-set ingress.gatewayApi.controllerNamespace=gateway
  --state-values-set ingress.gatewayApi.gateways.shared.name=shared-gw
  --state-values-set ingress.gatewayApi.gateways.shared.namespace=gateway
  --state-values-set ingress.gatewayApi.gateways.grpc.name=grpc-gw
  --state-values-set ingress.gatewayApi.gateways.grpc.namespace=gateway
)

(cd "$stack_dir" && HELMFILE_ENV=base HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" helmfile \
  --file helmfile.d/02-core.yaml.gotmpl \
  --environment default \
  "${source_args[@]}" \
  "${values_args[@]}" \
  --selector name=llm-request-router \
  list --skip-charts --output json >"$published_release")

test "$(yq -r '.[0].chart' "$published_release")" = \
  'nvcf/helm-nvcf-llm-request-router' ||
  fail "default chart source selected the wrong request-router chart"
test "$(yq -r '.[0].version' "$published_release")" = "$published_version" ||
  fail "default chart source did not select request-router $published_version"

(cd "$stack_dir" && HELMFILE_ENV=base HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" helmfile \
  --file helmfile.d/02-core.yaml.gotmpl \
  --environment default \
  "${source_args[@]}" \
  "${values_args[@]}" \
  --selector name=llm-request-router \
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
  '[select(.kind == "Deployment" and .metadata.name == "llm-request-router" and .metadata.namespace == "nvcf")] | length' \
  '1'
assert_manifest_value \
  'select(.kind == "Deployment" and .metadata.name == "llm-request-router" and .metadata.namespace == "nvcf") | .spec.template.spec.containers[0].image' \
  "$expected_stargate_image"
assert_manifest_value \
  '[select(.kind == "Deployment" and .metadata.name == "llm-request-router-backend-router" and .metadata.namespace == "nvcf")] | length' \
  '1'
assert_manifest_value \
  'select(.kind == "Deployment" and .metadata.name == "llm-request-router-backend-router" and .metadata.namespace == "nvcf") | .spec.template.spec.containers[] | select(.name == "backend-router") | .image' \
  "$expected_stargate_image"

for expected_arg in \
  '--grpc-pylon-dial-addr=https://llm-grpc.example.com:50071' \
  '--reverse-tunnel-pylon-dial-addr=llm-quic.example.com:50072' \
  '--tls-cert-path=/etc/stargate/tls/tls.crt' \
  '--tls-key-path=/etc/stargate/tls/tls.key'; do
  assert_manifest_value \
    "[select(.kind == \"Deployment\" and .metadata.name == \"llm-request-router\") | .spec.template.spec.containers[0].args[] | select(. == \"$expected_arg\")] | length" \
    '1'
done

assert_manifest_value \
  '[select(.kind == "Deployment" and .metadata.name == "llm-request-router") | .spec.template.spec.containers[0].args[] | select(. == "--quic-insecure")] | length' \
  '0'
assert_manifest_value \
  '[select(.kind == "Certificate" and .metadata.name == "stargate-quic-tls")] | length' \
  '1'
assert_manifest_value \
  'select(.kind == "Certificate" and .metadata.name == "stargate-quic-tls") | .spec.dnsNames[0]' \
  'llm-request-router.nvcf.svc.cluster.local'
assert_manifest_value \
  'select(.kind == "Certificate" and .metadata.name == "stargate-quic-tls") | .spec.dnsNames[1]' \
  '*.llm-request-router-headless.nvcf.svc.cluster.local'

echo "llm-router-published-chart: all checks passed"
