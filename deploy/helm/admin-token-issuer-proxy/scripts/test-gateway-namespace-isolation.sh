#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

chart_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/../chart" && pwd)"
tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

render() {
  local output="$1"
  shift
  helm template admin-token-issuer-proxy "$chart_dir" \
    --namespace plane-a-api-keys \
    --set-string adminIssuerProxy.image.registry=example.invalid \
    --set-string adminIssuerProxy.image.repository=admin-token-issuer-proxy \
    --set-string adminIssuerProxy.gateway.namespace=envoy-gateway-system \
    --set-string adminIssuerProxy.gateway.gatewayRef.name=plane-a-shared-gw \
    "$@" >"$output"
}

render "$tmpdir/legacy.yaml"
render "$tmpdir/isolated.yaml" \
  --set-string adminIssuerProxy.gateway.routeNamespace=plane-a-ingress

ruby -ryaml -e '
  file, expected_route_namespace = ARGV
  docs = YAML.load_stream(File.read(file)).compact
  route = docs.find { |doc| doc["kind"] == "HTTPRoute" }
  grant = docs.find { |doc| doc["kind"] == "ReferenceGrant" }
  abort "missing HTTPRoute or ReferenceGrant" unless route && grant
  abort "wrong route namespace" unless route.dig("metadata", "namespace") == expected_route_namespace
  abort "wrong Gateway parent namespace" unless route.dig("spec", "parentRefs", 0, "namespace") == "envoy-gateway-system"
  abort "wrong ReferenceGrant source namespace" unless grant.dig("spec", "from", 0, "namespace") == expected_route_namespace
  abort "wrong backend namespace" unless route.dig("spec", "rules", 0, "backendRefs", 0, "namespace") == "plane-a-api-keys"
' "$tmpdir/legacy.yaml" envoy-gateway-system

ruby -ryaml -e '
  file, expected_route_namespace = ARGV
  docs = YAML.load_stream(File.read(file)).compact
  route = docs.find { |doc| doc["kind"] == "HTTPRoute" }
  grant = docs.find { |doc| doc["kind"] == "ReferenceGrant" }
  abort "missing HTTPRoute or ReferenceGrant" unless route && grant
  abort "wrong isolated route namespace" unless route.dig("metadata", "namespace") == expected_route_namespace
  abort "wrong isolated Gateway parent namespace" unless route.dig("spec", "parentRefs", 0, "namespace") == "envoy-gateway-system"
  abort "wrong isolated ReferenceGrant source namespace" unless grant.dig("spec", "from", 0, "namespace") == expected_route_namespace
' "$tmpdir/isolated.yaml" plane-a-ingress

echo "Admin token issuer route namespace isolation checks passed."
