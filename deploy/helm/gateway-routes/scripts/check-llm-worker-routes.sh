#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
chart_dir="${script_dir}/../chart"
rendered="$(mktemp)"
disabled="$(mktemp)"
plaintext="$(mktemp)"
existing_secret="$(mktemp)"
invalid_backend_namespace_error="$(mktemp)"
invalid_plaintext_error="$(mktemp)"
invalid_tls_error="$(mktemp)"
invalid_dns_error="$(mktemp)"
invalid_tls_without_route_error="$(mktemp)"
trap 'rm -f "$rendered" "$disabled" "$plaintext" "$existing_secret" "$invalid_backend_namespace_error" "$invalid_plaintext_error" "$invalid_tls_error" "$invalid_dns_error" "$invalid_tls_without_route_error"' EXIT

helm template nvcf-gateway-routes "$chart_dir" \
  --namespace gateway \
  --set nvcfGatewayRoutes.routes.llmWorker.enabled=true \
  --set nvcfGatewayRoutes.gateways.llmGrpc.name=llm-grpc-gateway \
  --set nvcfGatewayRoutes.gateways.llmGrpc.namespace=gateway \
  --set nvcfGatewayRoutes.gateways.llmQuic.name=llm-quic-gateway \
  --set nvcfGatewayRoutes.gateways.llmQuic.namespace=gateway \
  --set nvcfGatewayRoutes.routes.llmWorker.backend.namespace=router-system \
  --set llmRequestRouter.grpcTls.enabled=true \
  --set llmRequestRouter.grpcTls.mode=certManager \
  --set llmRequestRouter.grpcTls.secretName=llm-request-router-grpc-tls \
  --set llmRequestRouter.grpcTls.dnsNames[0]=llm-grpc.example.invalid \
  --set llmRequestRouter.grpcTls.issuerRef.name=nvcf-openbao-pki \
  >"$rendered"

assert_contains() {
  local pattern="$1"
  local message="$2"
  if ! grep -Fq -- "$pattern" "$rendered"; then
    echo "FAIL: ${message}" >&2
    exit 1
  fi
}

assert_contains "kind: GRPCRoute" \
  "secure LLM worker routing must expose gRPC registration through a GRPCRoute"
assert_contains "kind: UDPRoute" \
  "LLM worker routing must expose reverse tunnels over UDP"
assert_contains "kind: BackendTrafficPolicy" \
  "secure LLM worker routing must disable Envoy timeouts for streaming RPCs"
assert_contains "requestTimeout: 0s" \
  "secure LLM worker routing must disable the request timeout"
assert_contains "maxStreamDuration: 0s" \
  "secure LLM worker routing must disable the maximum stream duration"
assert_contains "kind: Certificate" \
  "cert-manager mode must issue the dedicated gRPC listener certificate"
assert_contains 'secretName: "llm-request-router-grpc-tls"' \
  "the gRPC Certificate must write the configured listener Secret"
assert_contains "llm-grpc.example.invalid" \
  "the gRPC Certificate must contain the external dial hostname"
assert_contains "name: llm-request-router-backend-router" \
  "LLM worker routes must target the authority/SNI-aware backend router"
assert_contains "name: allow-llm-worker-routes" \
  "ReferenceGrant must permit cross-namespace LLM worker routes"
assert_contains "sectionName: llm-grpc" \
  "GRPCRoute must attach to the configured LLM gRPC listener"
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

if awk '
  $0 == "kind: GRPCRoute" { in_route = 1; next }
  in_route && /^---$/ { in_route = 0 }
  in_route && $1 == "hostnames:" { found = 1 }
  END { exit !found }
' "$rendered"; then
  echo "FAIL: the secure GRPCRoute must not match the external TLS hostname because Pylon preserves the internal Stargate authority" >&2
  exit 1
fi

helm template nvcf-gateway-routes "$chart_dir" \
  --namespace gateway \
  --set nvcfGatewayRoutes.routes.llmWorker.enabled=true \
  --set nvcfGatewayRoutes.routes.llmWorker.backend.namespace=router-system \
  --set llmRequestRouter.grpcTls.allowInsecureHttp=true \
  >"$plaintext"

if ! grep -Fq -- "kind: TCPRoute" "$plaintext"; then
  echo "FAIL: explicit development plaintext mode must retain the TCPRoute" >&2
  exit 1
fi
if grep -Eq '^kind: (GRPCRoute|BackendTrafficPolicy|Certificate)$' "$plaintext"; then
  echo "FAIL: development plaintext mode must not render secure gRPC resources" >&2
  exit 1
fi

helm template nvcf-gateway-routes "$chart_dir" \
  --namespace gateway \
  --set nvcfGatewayRoutes.routes.llmWorker.enabled=true \
  --set nvcfGatewayRoutes.routes.llmWorker.backend.namespace=router-system \
  --set llmRequestRouter.grpcTls.enabled=true \
  --set llmRequestRouter.grpcTls.mode=existingSecret \
  --set llmRequestRouter.grpcTls.secretName=operator-owned-grpc-tls \
  >"$existing_secret"

if ! grep -Fq -- "kind: GRPCRoute" "$existing_secret"; then
  echo "FAIL: existing-secret mode must retain secure GRPCRoute routing" >&2
  exit 1
fi
if grep -Fq -- "kind: Certificate" "$existing_secret"; then
  echo "FAIL: existing-secret mode must not render a cert-manager Certificate" >&2
  exit 1
fi

if helm template nvcf-gateway-routes "$chart_dir" \
  --namespace gateway \
  --set nvcfGatewayRoutes.routes.llmWorker.enabled=true \
  --set nvcfGatewayRoutes.routes.llmWorker.backend.namespace=router-system \
  >/dev/null 2>"$invalid_plaintext_error"; then
  echo "FAIL: enabled plaintext LLM worker routing must require an explicit development opt-in" >&2
  exit 1
fi
if ! grep -Fq -- "llmRequestRouter.grpcTls.allowInsecureHttp must be true when LLM worker routing is plaintext" "$invalid_plaintext_error"; then
  echo "FAIL: implicit plaintext routing must return the expected validation error" >&2
  exit 1
fi

if helm template nvcf-gateway-routes "$chart_dir" \
  --namespace gateway \
  --set nvcfGatewayRoutes.routes.llmWorker.enabled=true \
  --set nvcfGatewayRoutes.routes.llmWorker.backend.namespace=router-system \
  --set llmRequestRouter.grpcTls.enabled=true \
  --set llmRequestRouter.grpcTls.mode=certManager \
  --set llmRequestRouter.grpcTls.secretName=llm-request-router-grpc-tls \
  --set llmRequestRouter.grpcTls.dnsNames[0]=llm-grpc.example.invalid \
  >/dev/null 2>"$invalid_tls_error"; then
  echo "FAIL: cert-manager mode must require an issuer" >&2
  exit 1
fi
if ! grep -Fq -- "llmRequestRouter.grpcTls.issuerRef.name is required when grpcTls.mode is certManager" "$invalid_tls_error"; then
  echo "FAIL: incomplete cert-manager mode must return the expected validation error" >&2
  exit 1
fi

if helm template nvcf-gateway-routes "$chart_dir" \
  --namespace gateway \
  --set nvcfGatewayRoutes.routes.llmWorker.enabled=true \
  --set nvcfGatewayRoutes.routes.llmWorker.backend.namespace=router-system \
  --set llmRequestRouter.grpcTls.enabled=true \
  --set llmRequestRouter.grpcTls.mode=certManager \
  --set llmRequestRouter.grpcTls.secretName=llm-request-router-grpc-tls \
  --set llmRequestRouter.grpcTls.issuerRef.name=nvcf-openbao-pki \
  >/dev/null 2>"$invalid_dns_error"; then
  echo "FAIL: cert-manager mode must require certificate DNS names" >&2
  exit 1
fi
if ! grep -Fq -- "llmRequestRouter.grpcTls.dnsNames is required when grpcTls.mode is certManager" "$invalid_dns_error"; then
  echo "FAIL: incomplete cert-manager DNS configuration must return the expected validation error" >&2
  exit 1
fi

if helm template nvcf-gateway-routes "$chart_dir" \
  --namespace gateway \
  --set llmRequestRouter.grpcTls.enabled=true \
  --set llmRequestRouter.grpcTls.mode=existingSecret \
  --set llmRequestRouter.grpcTls.secretName=operator-owned-grpc-tls \
  >/dev/null 2>"$invalid_tls_without_route_error"; then
  echo "FAIL: gRPC TLS must not be enabled without the LLM worker route" >&2
  exit 1
fi
if ! grep -Fq -- "nvcfGatewayRoutes.routes.llmWorker.enabled must be true when llmRequestRouter.grpcTls.enabled is true" "$invalid_tls_without_route_error"; then
  echo "FAIL: gRPC TLS without a route must return the expected validation error" >&2
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
