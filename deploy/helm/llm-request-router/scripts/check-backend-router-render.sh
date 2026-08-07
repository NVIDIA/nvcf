#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
chart_dir="${script_dir}/../llm-request-router"
rendered="$(mktemp)"
disabled="$(mktemp)"
external_service_account="$(mktemp)"
wildcard_certificate="$(mktemp)"
trap 'rm -f "$rendered" "$disabled" "$external_service_account" "$wildcard_certificate"' EXIT

helm template llm-request-router "$chart_dir" \
  --namespace nvcf \
  --set llmRequestRouter.image.registry=registry.example.invalid \
  --set llmRequestRouter.image.repository=nvcf/stargate \
  --set llmRequestRouter.backendRouter.enabled=true \
  --set llmRequestRouter.backendRouter.image.tag=next \
  --set llmRequestRouter.backendRouter.pylonGrpcDialAddress=llm-router.example.invalid:443 \
  --set llmRequestRouter.backendRouter.pylonReverseTunnelDialAddress=llm-router.example.invalid:8080 \
  --set llmRequestRouter.certificate.enabled=true \
  --set llmRequestRouter.certificate.issuerRef.name=test-issuer \
  --set 'llmRequestRouter.certificate.dnsNames[0]=llm-request-router.nvcf.svc.cluster.local' \
  --set llmRequestRouter.tls.secretName=stargate-quic-tls \
  --set llmRequestRouter.tls.certPath=/etc/stargate/tls/tls.crt \
  --set llmRequestRouter.tls.keyPath=/etc/stargate/tls/tls.key \
  --set llmRequestRouter.tls.quicInsecure=false \
  --set llmRequestRouter.metrics.enabled=true \
  --set llmRequestRouter.metrics.serviceMonitor.enabled=true \
  >"$rendered"

assert_contains() {
  local pattern="$1"
  local message="$2"
  if ! grep -Fq -- "$pattern" "$rendered"; then
    echo "FAIL: ${message}" >&2
    exit 1
  fi
}

assert_backend_router_replicas() {
  local expected="$1"
  local actual
  actual="$(awk '
    $0 == "kind: Deployment" { in_deployment = 1; backend_router = 0 }
    in_deployment && $1 == "name:" && $2 == "llm-request-router-backend-router" { backend_router = 1 }
    backend_router && $1 == "replicas:" { print $2; exit }
  ' "$rendered")"
  if [[ "$actual" != "$expected" ]]; then
    echo "FAIL: backend router must default to ${expected} replica; rendered ${actual:-none}" >&2
    exit 1
  fi
}

assert_backend_router_role_binding_subject() {
  local rendered_file="$1"
  local expected="$2"
  local actual
  actual="$(awk '
    $0 == "kind: RoleBinding" { in_binding = 1; target_binding = 0; in_subjects = 0 }
    in_binding && !target_binding && $1 == "name:" && $2 == "llm-request-router-backend-router-endpointslice-reader" { target_binding = 1 }
    target_binding && $0 == "subjects:" { in_subjects = 1 }
    target_binding && in_subjects && $1 == "name:" { print $2; exit }
  ' "$rendered_file")"
  if [[ "$actual" != "$expected" ]]; then
    echo "FAIL: backend router RoleBinding must target ${expected}; rendered ${actual:-none}" >&2
    exit 1
  fi
}

assert_service_account_exists() {
  local rendered_file="$1"
  local expected="$2"
  if ! awk -v expected="$expected" '
    $0 == "---" { in_service_account = 0 }
    $0 == "kind: ServiceAccount" { in_service_account = 1 }
    in_service_account && $1 == "name:" && $2 == expected { found = 1 }
    END { exit found ? 0 : 1 }
  ' "$rendered_file"; then
    echo "FAIL: chart must render the dedicated ${expected} ServiceAccount" >&2
    exit 1
  fi
}

assert_contains "name: llm-request-router-backend-router" \
  "backend router workload and Service must use a stable name"
assert_contains "kind: Deployment" \
  "backend router must render as a Deployment"
assert_backend_router_replicas "1"
assert_contains "kind: Role" \
  "backend router must render namespaced RBAC"
assert_contains "resources: [\"endpointslices\"]" \
  "backend router must be allowed to watch EndpointSlices"
assert_contains "serviceAccountName: llm-request-router-backend-router" \
  "backend router must use its dedicated ServiceAccount"
assert_service_account_exists "$rendered" "llm-request-router-backend-router"
assert_backend_router_role_binding_subject "$rendered" "llm-request-router-backend-router"
assert_contains "command:" \
  "backend router must override the Stargate image entrypoint"
assert_contains "/usr/local/bin/stargate-k8s-router" \
  "Stargate image must include the Kubernetes router binary"
assert_contains "--target-service-name=llm-request-router" \
  "backend router must watch the readiness-respecting request-router Service"
assert_contains "--advertised-hostname-template={pod_name}.llm-request-router-headless.nvcf.svc.cluster.local" \
  "backend router authority and SNI template must match Stargate"
assert_contains "- '*.llm-request-router-headless.nvcf.svc.cluster.local'" \
  "request-router certificate must cover pod-specific backend routing hostnames"
assert_contains "image: registry.example.invalid/nvcf/stargate:next" \
  "backend router must use its explicitly pinned Stargate image"
assert_contains "app.kubernetes.io/version: \"next\"" \
  "backend router labels must identify the explicitly pinned image version"
assert_contains "--grpc-pylon-dial-addr=llm-router.example.invalid:443" \
  "Stargate must advertise the external gRPC endpoint to pylon"
assert_contains "--reverse-tunnel-pylon-dial-addr=llm-router.example.invalid:8080" \
  "Stargate must advertise the external reverse-tunnel endpoint to pylon"
assert_contains "--tls-cert-path=/etc/stargate/tls/tls.crt" \
  "backend router must use the Stargate TLS certificate"
assert_contains "secretName: \"stargate-quic-tls\"" \
  "backend router must mount the configured Stargate TLS Secret"
assert_contains "name: llm-request-router-backend-router-metrics" \
  "backend router metrics must be discoverable by the existing ServiceMonitor option"

helm template llm-request-router "$chart_dir" \
  --namespace nvcf \
  --set llmRequestRouter.image.registry=registry.example.invalid \
  --set llmRequestRouter.image.repository=nvcf/stargate \
  --set llmRequestRouter.backendRouter.enabled=false \
  >"$disabled"

if grep -Fq "llm-request-router-backend-router" "$disabled"; then
  echo "FAIL: disabled backend router must not render router resources" >&2
  exit 1
fi

if helm template llm-request-router "$chart_dir" \
  --namespace nvcf \
  --set llmRequestRouter.image.registry=registry.example.invalid \
  --set llmRequestRouter.image.repository=nvcf/stargate \
  --set llmRequestRouter.backendRouter.enabled=true \
  --set llmRequestRouter.backendRouter.image.tag=next \
  >/dev/null 2>&1; then
  echo "FAIL: enabled backend router must require pylon dial addresses" >&2
  exit 1
fi

if helm template llm-request-router "$chart_dir" \
  --namespace nvcf \
  --set llmRequestRouter.image.registry=registry.example.invalid \
  --set llmRequestRouter.image.repository=nvcf/stargate \
  --set-string 'llmRequestRouter.kubernetes.advertisedHostnameTemplate=\{pod_name\}\{pod_name\}' \
  --set llmRequestRouter.backendRouter.enabled=true \
  --set llmRequestRouter.backendRouter.image.tag=next \
  --set llmRequestRouter.backendRouter.pylonGrpcDialAddress=llm-router.example.invalid:443 \
  --set llmRequestRouter.backendRouter.pylonReverseTunnelDialAddress=llm-router.example.invalid:8080 \
  >/dev/null 2>&1; then
  echo "FAIL: backend routing must reject multiple {pod_name} placeholders" >&2
  exit 1
fi

if helm template llm-request-router "$chart_dir" \
  --namespace nvcf \
  --set llmRequestRouter.image.registry=registry.example.invalid \
  --set llmRequestRouter.image.repository=nvcf/stargate \
  --set llmRequestRouter.backendRouter.enabled=true \
  --set llmRequestRouter.backendRouter.image.tag=next \
  --set llmRequestRouter.backendRouter.pylonGrpcDialAddress=llm-router.example.invalid:443 \
  --set llmRequestRouter.backendRouter.pylonReverseTunnelDialAddress=llm-router.example.invalid:8080 \
  --set llmRequestRouter.backendRouter.serviceAccount.create=false \
  >/dev/null 2>&1; then
  echo "FAIL: chart-managed backend RBAC must not bind the namespace default ServiceAccount" >&2
  exit 1
fi

if helm template llm-request-router "$chart_dir" \
  --namespace nvcf \
  --set llmRequestRouter.image.registry=registry.example.invalid \
  --set llmRequestRouter.image.repository=nvcf/stargate \
  --set llmRequestRouter.backendRouter.enabled=true \
  --set llmRequestRouter.backendRouter.image.tag=next \
  --set llmRequestRouter.backendRouter.pylonGrpcDialAddress=llm-router.example.invalid:443 \
  --set llmRequestRouter.backendRouter.pylonReverseTunnelDialAddress=llm-router.example.invalid:8080 \
  --set llmRequestRouter.backendRouter.serviceAccount.create=false \
  --set llmRequestRouter.rbac.create=false \
  >/dev/null 2>&1; then
  echo "FAIL: an external backend ServiceAccount must be named even when chart RBAC is disabled" >&2
  exit 1
fi

helm template llm-request-router "$chart_dir" \
  --namespace nvcf \
  --set llmRequestRouter.image.registry=registry.example.invalid \
  --set llmRequestRouter.image.repository=nvcf/stargate \
  --set llmRequestRouter.backendRouter.enabled=true \
  --set llmRequestRouter.backendRouter.image.tag=next \
  --set llmRequestRouter.backendRouter.pylonGrpcDialAddress=llm-router.example.invalid:443 \
  --set llmRequestRouter.backendRouter.pylonReverseTunnelDialAddress=llm-router.example.invalid:8080 \
  --set llmRequestRouter.backendRouter.serviceAccount.create=false \
  --set llmRequestRouter.backendRouter.serviceAccount.name=external-backend-router \
  >"$external_service_account"

if ! grep -Fq -- "serviceAccountName: external-backend-router" "$external_service_account"; then
  echo "FAIL: backend router must use the configured external ServiceAccount" >&2
  exit 1
fi
assert_backend_router_role_binding_subject "$external_service_account" "external-backend-router"

if helm template llm-request-router "$chart_dir" \
  --namespace nvcf \
  --set llmRequestRouter.image.registry=registry.example.invalid \
  --set llmRequestRouter.image.repository=nvcf/stargate \
  --set llmRequestRouter.backendRouter.enabled=true \
  --set llmRequestRouter.backendRouter.pylonGrpcDialAddress=llm-router.example.invalid:443 \
  --set llmRequestRouter.backendRouter.pylonReverseTunnelDialAddress=llm-router.example.invalid:8080 \
  >/dev/null 2>&1; then
  echo "FAIL: enabled backend router must require an explicit image tag" >&2
  exit 1
fi

helm template llm-request-router "$chart_dir" \
  --namespace nvcf \
  --set llmRequestRouter.image.registry=registry.example.invalid \
  --set llmRequestRouter.image.repository=nvcf/stargate \
  --set llmRequestRouter.backendRouter.enabled=true \
  --set llmRequestRouter.backendRouter.image.tag=next \
  --set llmRequestRouter.backendRouter.pylonGrpcDialAddress=llm-router.example.invalid:443 \
  --set llmRequestRouter.backendRouter.pylonReverseTunnelDialAddress=llm-router.example.invalid:8080 \
  --set llmRequestRouter.certificate.enabled=true \
  --set llmRequestRouter.certificate.issuerRef.name=test-issuer \
  --set llmRequestRouter.tls.secretName=stargate-quic-tls \
  --set llmRequestRouter.tls.certPath=/etc/stargate/tls/tls.crt \
  --set llmRequestRouter.tls.keyPath=/etc/stargate/tls/tls.key \
  >"$wildcard_certificate"

if ! grep -Fq -- "- '*.llm-request-router-headless.nvcf.svc.cluster.local'" "$wildcard_certificate"; then
  echo "FAIL: backend routing must add its wildcard before certificate DNS-name validation" >&2
  exit 1
fi

if helm template llm-request-router "$chart_dir" \
  --namespace nvcf \
  --set llmRequestRouter.image.registry=registry.example.invalid \
  --set llmRequestRouter.image.repository=nvcf/stargate \
  --set llmRequestRouter.backendRouter.enabled=false \
  --set llmRequestRouter.certificate.enabled=true \
  --set llmRequestRouter.certificate.issuerRef.name=test-issuer \
  >/dev/null 2>&1; then
  echo "FAIL: a certificate without an expanded DNS name must fail rendering" >&2
  exit 1
fi

if helm template llm-request-router "$chart_dir" \
  --namespace nvcf \
  --set llmRequestRouter.image.registry=registry.example.invalid \
  --set llmRequestRouter.image.repository=nvcf/stargate \
  --set llmRequestRouter.kubernetes.advertisedHostnameTemplate=llm-request-router.nvcf.svc.cluster.local \
  --set llmRequestRouter.backendRouter.enabled=true \
  --set llmRequestRouter.backendRouter.image.tag=next \
  --set llmRequestRouter.backendRouter.pylonGrpcDialAddress=llm-router.example.invalid:443 \
  --set llmRequestRouter.backendRouter.pylonReverseTunnelDialAddress=llm-router.example.invalid:8080 \
  >/dev/null 2>&1; then
  echo "FAIL: backend routing must reject a hostname template without {pod_name}" >&2
  exit 1
fi

if helm template llm-request-router "$chart_dir" \
  --namespace nvcf \
  --set llmRequestRouter.image.registry=registry.example.invalid \
  --set llmRequestRouter.image.repository=nvcf/stargate \
  --set llmRequestRouter.backendRouter.enabled=true \
  --set llmRequestRouter.backendRouter.image.tag=next \
  --set llmRequestRouter.backendRouter.pylonGrpcDialAddress=llm-router.example.invalid:443 \
  --set llmRequestRouter.backendRouter.pylonReverseTunnelDialAddress=llm-router.example.invalid:8080 \
  --set llmRequestRouter.tls.quicInsecure=false \
  >/dev/null 2>&1; then
  echo "FAIL: secure backend QUIC must require a TLS Secret and cert/key paths" >&2
  exit 1
fi

if helm template llm-request-router "$chart_dir" \
  --namespace nvcf \
  --set llmRequestRouter.image.registry=registry.example.invalid \
  --set llmRequestRouter.image.repository=nvcf/stargate \
  --set llmRequestRouter.backendRouter.enabled=true \
  --set llmRequestRouter.backendRouter.image.tag=next \
  --set llmRequestRouter.backendRouter.pylonGrpcDialAddress=llm-router.example.invalid:443 \
  --set llmRequestRouter.backendRouter.pylonReverseTunnelDialAddress=llm-router.example.invalid:8080 \
  --set llmRequestRouter.tls.certPath=/etc/stargate/tls/tls.crt \
  >/dev/null 2>&1; then
  echo "FAIL: backend routing must reject a partial TLS configuration" >&2
  exit 1
fi

if helm template llm-request-router "$chart_dir" \
  --namespace nvcf \
  --set llmRequestRouter.image.registry=registry.example.invalid \
  --set llmRequestRouter.image.repository=nvcf/stargate \
  --set llmRequestRouter.backendRouter.enabled=true \
  --set llmRequestRouter.backendRouter.image.tag=next \
  --set llmRequestRouter.backendRouter.pylonGrpcDialAddress=llm-router.example.invalid:443 \
  --set llmRequestRouter.backendRouter.pylonReverseTunnelDialAddress=llm-router.example.invalid:8080 \
  --set llmRequestRouter.tls.secretName=stargate-quic-tls \
  --set llmRequestRouter.tls.certPath=/etc/stargate/tls/tls.crt \
  --set llmRequestRouter.tls.keyPath=/var/run/stargate/tls.key \
  >/dev/null 2>&1; then
  echo "FAIL: backend TLS cert and key paths must use the same directory" >&2
  exit 1
fi

if helm template llm-request-router "$chart_dir" \
  --namespace nvcf \
  --set llmRequestRouter.image.registry=registry.example.invalid \
  --set llmRequestRouter.image.repository=nvcf/stargate \
  --set llmRequestRouter.backendRouter.enabled=false \
  --set llmRequestRouter.tls.secretName=stargate-quic-tls \
  --set llmRequestRouter.tls.certPath=/etc/stargate/tls/tls.crt \
  --set llmRequestRouter.tls.keyPath=/var/run/stargate/tls.key \
  >/dev/null 2>&1; then
  echo "FAIL: Stargate TLS cert and key paths must use the same directory" >&2
  exit 1
fi

if helm template llm-request-router "$chart_dir" \
  --namespace nvcf \
  --set llmRequestRouter.image.registry=registry.example.invalid \
  --set llmRequestRouter.image.repository=nvcf/stargate \
  --set llmRequestRouter.backendRouter.enabled=true \
  --set llmRequestRouter.backendRouter.image.tag=next \
  --set llmRequestRouter.backendRouter.pylonGrpcDialAddress=llm-router.example.invalid:443 \
  --set llmRequestRouter.backendRouter.pylonReverseTunnelDialAddress=llm-router.example.invalid:8080 \
  --set llmRequestRouter.tls.secretName=stargate-quic-tls \
  --set llmRequestRouter.tls.mountPath=/var/run/stargate \
  --set llmRequestRouter.tls.certPath=/etc/stargate/tls/tls.crt \
  --set llmRequestRouter.tls.keyPath=/etc/stargate/tls/tls.key \
  >/dev/null 2>&1; then
  echo "FAIL: backend TLS mount path must contain the configured cert and key paths" >&2
  exit 1
fi

single_replica="$(helm template llm-request-router "$chart_dir" \
  --namespace nvcf \
  --set llmRequestRouter.image.registry=registry.example.invalid \
  --set llmRequestRouter.image.repository=nvcf/stargate \
  --set llmRequestRouter.replicaCount=1 \
  --set llmRequestRouter.backendRouter.enabled=true \
  --set llmRequestRouter.backendRouter.image.tag=next \
  --set llmRequestRouter.backendRouter.pylonGrpcDialAddress=llm-router.example.invalid:443 \
  --set llmRequestRouter.backendRouter.pylonReverseTunnelDialAddress=llm-router.example.invalid:8080)"
if ! grep -Fq -- "--advertised-hostname-template={pod_name}.llm-request-router-headless.nvcf.svc.cluster.local" <<<"$single_replica"; then
  echo "FAIL: backend routing must retain per-pod authority and SNI for one replica" >&2
  exit 1
fi

echo "PASS: LLM request-router backend routing renders correctly"
