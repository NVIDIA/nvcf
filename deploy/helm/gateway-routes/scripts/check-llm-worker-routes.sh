#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
chart_dir="${script_dir}/../chart"
rendered="$(mktemp)"
disabled="$(mktemp)"
invalid_backend_namespace_error="$(mktemp)"
trap 'rm -f "$rendered" "$disabled" "$invalid_backend_namespace_error"' EXIT

helm template nvcf-gateway-routes "$chart_dir" \
  --namespace gateway \
  --set nvcfGatewayRoutes.routes.llmWorker.enabled=true \
  --set nvcfGatewayRoutes.gateways.llmGrpc.name=llm-grpc-gateway \
  --set nvcfGatewayRoutes.gateways.llmGrpc.namespace=gateway \
  --set nvcfGatewayRoutes.gateways.llmQuic.name=llm-quic-gateway \
  --set nvcfGatewayRoutes.gateways.llmQuic.namespace=gateway \
  --set nvcfGatewayRoutes.routes.llmWorker.backend.namespace=router-system \
  >"$rendered"

assert_contains() {
  local pattern="$1"
  local message="$2"
  if ! grep -Fq -- "$pattern" "$rendered"; then
    echo "FAIL: ${message}" >&2
    exit 1
  fi
}

assert_contains "kind: TCPRoute" \
  "LLM worker routing must expose gRPC registration over TCP"
assert_contains "kind: UDPRoute" \
  "LLM worker routing must expose reverse tunnels over UDP"
assert_contains "name: llm-request-router-backend-router" \
  "LLM worker routes must target the authority/SNI-aware backend router"
assert_contains "name: allow-llm-worker-routes" \
  "ReferenceGrant must permit cross-namespace LLM worker routes"
assert_contains "sectionName: llm-grpc" \
  "TCPRoute must attach to the configured LLM gRPC listener"
assert_contains "sectionName: llm-quic" \
  "UDPRoute must attach to the configured LLM QUIC listener"

reference_grant_service_name="$(awk '
  $0 == "kind: ReferenceGrant" { in_grant = 1; target_grant = 0; in_to = 0 }
  in_grant && !target_grant && $1 == "name:" && $2 == "allow-llm-worker-routes" { target_grant = 1 }
  target_grant && $0 == "  to:" { in_to = 1 }
  target_grant && in_to && $1 == "name:" { print $2; exit }
' "$rendered")"
if [[ "$reference_grant_service_name" != "llm-request-router-backend-router" ]]; then
  echo "FAIL: LLM worker ReferenceGrant must stay scoped to the configured backend Service" >&2
  exit 1
fi

backend_namespace_references="$(grep -Fc -- "namespace: router-system" "$rendered" || true)"
if [[ "$backend_namespace_references" != "3" ]]; then
  echo "FAIL: LLM worker routes and ReferenceGrant must use the configured backend namespace" >&2
  exit 1
fi

if helm template nvcf-gateway-routes "$chart_dir" \
  --namespace gateway \
  --set nvcfGatewayRoutes.routes.llmWorker.enabled=true \
  --set-string nvcfGatewayRoutes.routes.llmWorker.backend.namespace= \
  >/dev/null 2>"$invalid_backend_namespace_error"; then
  echo "FAIL: enabled LLM worker routing must require an explicit backend namespace" >&2
  exit 1
fi
if ! grep -Fq -- "nvcfGatewayRoutes.routes.llmWorker.backend.namespace is required when llmWorker.enabled is true" "$invalid_backend_namespace_error"; then
  echo "FAIL: missing LLM worker backend namespace must return the expected validation error" >&2
  exit 1
fi

helm template nvcf-gateway-routes "$chart_dir" \
  --namespace gateway \
  --set nvcfGatewayRoutes.routes.llmWorker.enabled=false \
  >"$disabled"

if grep -Eq '^  name: (llm-worker-(grpc|quic)|allow-llm-worker-routes)$' "$disabled"; then
  echo "FAIL: disabled LLM worker routing must not render route resources" >&2
  exit 1
fi

echo "PASS: LLM worker Gateway routes render correctly"
