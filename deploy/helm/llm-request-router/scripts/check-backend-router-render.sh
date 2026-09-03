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
zero_config="$(mktemp)"
service_monitor_namespace_file="$(mktemp)"
trap 'rm -f "$rendered" "$disabled" "$external_service_account" "$wildcard_certificate" "$zero_config" "$service_monitor_namespace_file"' EXIT

helm template llm-request-router "$chart_dir" \
  --namespace nvcf \
  --set llmRequestRouter.image.registry=registry.example.invalid \
  --set llmRequestRouter.image.repository=nvcf/stargate \
  --set llmRequestRouter.backendRouter.enabled=true \
  --set llmRequestRouter.backendRouter.image.tag=next \
  --set llmRequestRouter.backendRouter.pylonGrpcDialAddress=https://llm-router.example.invalid:443 \
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

assert_occurrences() {
  local pattern="$1"
  local expected="$2"
  local message="$3"
  local actual
  actual="$(grep -Fc -- "$pattern" "$rendered")"
  if [[ "$actual" != "$expected" ]]; then
    echo "FAIL: ${message}; rendered ${actual} occurrence(s), expected ${expected}" >&2
    exit 1
  fi
}

assert_render_fails() {
  local expected_error="$1"
  local error_file
  shift
  error_file="$(mktemp)"
  if helm template llm-request-router "$chart_dir" --namespace nvcf "$@" >/dev/null 2>"$error_file"; then
    rm -f "$error_file"
    echo "FAIL: expected Helm render to fail: ${expected_error}" >&2
    exit 1
  fi
  if ! grep -Fq -- "$expected_error" "$error_file"; then
    rm -f "$error_file"
    echo "FAIL: render did not return the expected validation error: ${expected_error}" >&2
    exit 1
  fi
  rm -f "$error_file"
}

assert_backend_router_replicas() {
  local expected="$1"
  local actual
  actual="$(awk '
    $0 == "---" { in_deployment = 0; backend_router = 0 }
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
    $0 == "---" { in_binding = 0; target_binding = 0; in_subjects = 0 }
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

assert_backend_router_service_monitor_namespace() {
  local rendered_file="$1"
  local expected="$2"
  local actual
  actual="$(awk '
    $0 == "---" { in_monitor = 0; target_monitor = 0 }
    $0 == "kind: ServiceMonitor" { in_monitor = 1 }
    in_monitor && $1 == "name:" && $2 == "llm-request-router-backend-router-metrics" { target_monitor = 1; next }
    target_monitor && $1 == "namespace:" { print $2; exit }
  ' "$rendered_file")"
  if [[ "$actual" != "$expected" ]]; then
    echo "FAIL: backend router ServiceMonitor must be created in ${expected}; rendered ${actual:-none}" >&2
    exit 1
  fi
}

assert_backend_router_pdb() {
  if ! awk '
    $0 == "---" { in_pdb = 0; target = 0; max_unavailable = 0; selector = 0 }
    $0 == "kind: PodDisruptionBudget" { in_pdb = 1 }
    in_pdb && $1 == "name:" && $2 == "llm-request-router-backend-router" { target = 1 }
    target && $1 == "maxUnavailable:" && $2 == "1" { max_unavailable = 1 }
    target && $1 == "app.kubernetes.io/name:" && $2 == "llm-request-router-backend-router" { selector = 1 }
    target && max_unavailable && selector { found = 1 }
    END { exit found ? 0 : 1 }
  ' "$rendered"; then
    echo "FAIL: backend router must render a maxUnavailable=1 PDB with its own selector" >&2
    exit 1
  fi
}

assert_contains "name: llm-request-router-backend-router" \
  "backend router workload and Service must use a stable name"
assert_contains "kind: Deployment" \
  "backend router must render as a Deployment"
assert_backend_router_replicas "2"
assert_backend_router_pdb
assert_contains "kind: Role" \
  "backend router must render namespaced RBAC"
assert_contains "resources: [\"endpointslices\"]" \
  "backend router must be allowed to watch EndpointSlices"

# Backend routing follows the LLM addon, so it has to render with no
# operator-supplied dial addresses at all. This renders exactly that case.
helm template llm-request-router "$chart_dir" \
  --namespace nvcf \
  --set llmRequestRouter.image.registry=registry.example.invalid \
  --set llmRequestRouter.image.repository=nvcf/stargate \
  --set llmRequestRouter.backendRouter.enabled=true \
  >"$zero_config"

assert_zero_config_contains() {
  local pattern="$1"
  local message="$2"
  if ! grep -Fq -- "$pattern" "$zero_config"; then
    echo "FAIL: ${message}" >&2
    exit 1
  fi
}

assert_zero_config_contains "--grpc-pylon-dial-addr=http://llm-request-router-backend-router.nvcf.svc.cluster.local:50071" \
  "gRPC dial address must default to the in-cluster backend-router Service"
assert_zero_config_contains "--reverse-tunnel-pylon-dial-addr=llm-request-router-backend-router.nvcf.svc.cluster.local:50072" \
  "reverse-tunnel dial address must default to the in-cluster backend-router Service"

# An explicitly configured address must still win over the default.
assert_contains "--grpc-pylon-dial-addr=https://llm-router.example.invalid:443" \
  "configured gRPC dial address must override the in-cluster default"

# Each replica terminates QUIC itself and cannot resume another replica's
# session, so clients must be pinned.
assert_contains "sessionAffinity: ClientIP" \
  "backend router Service must pin clients so QUIC sessions do not rehash"

# Two replicas on one node would not survive node loss.
assert_contains "podAntiAffinity:" \
  "backend router must default to spreading replicas across nodes"
assert_contains "topologyKey: kubernetes.io/hostname" \
  "backend router anti-affinity must spread across nodes, not a narrower topology"
assert_contains "serviceAccountName: llm-request-router-backend-router" \
  "backend router must use its dedicated ServiceAccount"
assert_service_account_exists "$rendered" "llm-request-router-backend-router"
assert_backend_router_role_binding_subject "$rendered" "llm-request-router-backend-router"
assert_contains "command:" \
  "backend router must override the Stargate image entrypoint"
assert_contains "/usr/local/bin/stargate-k8s-router" \
  "Stargate image must include the Kubernetes router binary"
assert_occurrences "--shutdown-drain-timeout-ms=30000" "2" \
  "both Stargate and the backend router must use the configured graceful shutdown budget"
assert_occurrences "terminationGracePeriodSeconds: 35" "2" \
  "both workloads must leave Kubernetes time beyond their configured drain budget"
assert_contains "--target-service-name=llm-request-router-headless" \
  "backend router must watch the headless Service so warming pods accept pylon connections"
assert_contains "--advertised-hostname-template={pod_name}.llm-request-router-headless.nvcf.svc.cluster.local" \
  "backend router authority and SNI template must match Stargate"
assert_contains "--advertised-grpc-port=50071" \
  "backend router Watch snapshots must advertise the Stargate gRPC port"
assert_contains "--grpc-pylon-dial-addr=https://llm-router.example.invalid:443" \
  "backend router Watch snapshots must preserve the Pylon dial endpoint"
assert_contains "- '*.llm-request-router-headless.nvcf.svc.cluster.local'" \
  "request-router certificate must cover pod-specific backend routing hostnames"
assert_contains "image: registry.example.invalid/nvcf/stargate:next" \
  "backend router must use its explicitly pinned Stargate image"
assert_contains "app.kubernetes.io/version: \"next\"" \
  "backend router labels must identify the explicitly pinned image version"
assert_contains "--grpc-pylon-dial-addr=https://llm-router.example.invalid:443" \
  "Stargate must advertise the external gRPC endpoint to pylon"
assert_contains "--reverse-tunnel-pylon-dial-addr=llm-router.example.invalid:8080" \
  "Stargate must advertise the external reverse-tunnel endpoint to pylon"
assert_contains "--tls-cert-path=/etc/stargate/tls/tls.crt" \
  "backend router must use the Stargate TLS certificate"
assert_contains "secretName: \"stargate-quic-tls\"" \
  "backend router must mount the configured Stargate TLS Secret"
assert_contains "name: llm-request-router-backend-router-metrics" \
  "backend router metrics must be discoverable by the existing ServiceMonitor option"
assert_backend_router_service_monitor_namespace "$rendered" "nvcf"

helm template llm-request-router "$chart_dir" \
  --namespace nvcf \
  --set llmRequestRouter.image.registry=registry.example.invalid \
  --set llmRequestRouter.image.repository=nvcf/stargate \
  --set llmRequestRouter.replicaCount=1 \
  --set llmRequestRouter.backendRouter.enabled=false \
  >"$disabled"

if grep -Fq "llm-request-router-backend-router" "$disabled"; then
  echo "FAIL: disabled backend router must not render router resources" >&2
  exit 1
fi

assert_render_fails "llmRequestRouter.kubernetes.advertisedHostnameTemplate must contain exactly one {pod_name} when backendRouter.enabled is true" \
  --set llmRequestRouter.image.registry=registry.example.invalid \
  --set llmRequestRouter.image.repository=nvcf/stargate \
  --set-string 'llmRequestRouter.kubernetes.advertisedHostnameTemplate=\{pod_name\}\{pod_name\}' \
  --set llmRequestRouter.backendRouter.enabled=true \
  --set llmRequestRouter.backendRouter.image.tag=next \
  --set llmRequestRouter.backendRouter.pylonGrpcDialAddress=https://llm-router.example.invalid:443 \
  --set llmRequestRouter.backendRouter.pylonReverseTunnelDialAddress=llm-router.example.invalid:8080

assert_render_fails "llmRequestRouter.transport.reverseTunnelListenAddr port 50073 must match llmRequestRouter.service.reverseTunnelPort 50072 when backend routing is enabled" \
  --set llmRequestRouter.image.registry=registry.example.invalid \
  --set llmRequestRouter.image.repository=nvcf/stargate \
  --set llmRequestRouter.backendRouter.enabled=true \
  --set llmRequestRouter.backendRouter.image.tag=next \
  --set llmRequestRouter.transport.reverseTunnelListenAddr=0.0.0.0:50073

assert_render_fails "llmRequestRouter.backendRouter.image.repository or llmRequestRouter.image.repository is required when backend routing is enabled" \
  --set llmRequestRouter.backendRouter.enabled=true \
  --set llmRequestRouter.backendRouter.image.tag=next \
  --set-string llmRequestRouter.image.repository= \
  --set-string llmRequestRouter.backendRouter.image.repository=

service_monitor_namespace="$(helm template llm-request-router "$chart_dir" \
  --namespace release-namespace \
  --set llmRequestRouter.namespace=router-system \
  --set llmRequestRouter.image.registry=registry.example.invalid \
  --set llmRequestRouter.image.repository=nvcf/stargate \
  --set llmRequestRouter.backendRouter.enabled=true \
  --set llmRequestRouter.backendRouter.image.tag=next \
  --set llmRequestRouter.metrics.enabled=true \
  --set llmRequestRouter.metrics.serviceMonitor.enabled=true)"
printf '%s\n' "$service_monitor_namespace" >"$service_monitor_namespace_file"
assert_backend_router_service_monitor_namespace "$service_monitor_namespace_file" "router-system"

assert_render_fails "llmRequestRouter.backendRouter.serviceAccount.name is required when backendRouter is enabled and backendRouter.serviceAccount.create is false" \
  --set llmRequestRouter.image.registry=registry.example.invalid \
  --set llmRequestRouter.image.repository=nvcf/stargate \
  --set llmRequestRouter.backendRouter.enabled=true \
  --set llmRequestRouter.backendRouter.image.tag=next \
  --set llmRequestRouter.backendRouter.pylonGrpcDialAddress=https://llm-router.example.invalid:443 \
  --set llmRequestRouter.backendRouter.pylonReverseTunnelDialAddress=llm-router.example.invalid:8080 \
  --set llmRequestRouter.backendRouter.serviceAccount.create=false

assert_render_fails "llmRequestRouter.backendRouter.serviceAccount.name is required when backendRouter is enabled and backendRouter.serviceAccount.create is false" \
  --set llmRequestRouter.image.registry=registry.example.invalid \
  --set llmRequestRouter.image.repository=nvcf/stargate \
  --set llmRequestRouter.backendRouter.enabled=true \
  --set llmRequestRouter.backendRouter.image.tag=next \
  --set llmRequestRouter.backendRouter.pylonGrpcDialAddress=https://llm-router.example.invalid:443 \
  --set llmRequestRouter.backendRouter.pylonReverseTunnelDialAddress=llm-router.example.invalid:8080 \
  --set llmRequestRouter.backendRouter.serviceAccount.create=false \
  --set llmRequestRouter.rbac.create=false

helm template llm-request-router "$chart_dir" \
  --namespace nvcf \
  --set llmRequestRouter.image.registry=registry.example.invalid \
  --set llmRequestRouter.image.repository=nvcf/stargate \
  --set llmRequestRouter.backendRouter.enabled=true \
  --set llmRequestRouter.backendRouter.image.tag=next \
  --set llmRequestRouter.backendRouter.pylonGrpcDialAddress=https://llm-router.example.invalid:443 \
  --set llmRequestRouter.backendRouter.pylonReverseTunnelDialAddress=llm-router.example.invalid:8080 \
  --set llmRequestRouter.backendRouter.serviceAccount.create=false \
  --set llmRequestRouter.backendRouter.serviceAccount.name=external-backend-router \
  >"$external_service_account"

if ! grep -Fq -- "serviceAccountName: external-backend-router" "$external_service_account"; then
  echo "FAIL: backend router must use the configured external ServiceAccount" >&2
  exit 1
fi
assert_backend_router_role_binding_subject "$external_service_account" "external-backend-router"

assert_zero_config_contains "image: registry.example.invalid/nvcf/stargate:0.14.2" \
  "backend router must inherit the released Stargate image with no router image configuration"
assert_zero_config_contains 'app.kubernetes.io/version: "0.14.2"' \
  "backend router labels must identify the inherited Stargate image version"

helm template llm-request-router "$chart_dir" \
  --namespace nvcf \
  --set llmRequestRouter.image.registry=registry.example.invalid \
  --set llmRequestRouter.image.repository=nvcf/stargate \
  --set llmRequestRouter.backendRouter.enabled=true \
  --set llmRequestRouter.backendRouter.image.tag=next \
  --set llmRequestRouter.backendRouter.pylonGrpcDialAddress=https://llm-router.example.invalid:443 \
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

assert_render_fails "llmRequestRouter.certificate.dnsNames is required when certificate.enabled is true" \
  --set llmRequestRouter.image.registry=registry.example.invalid \
  --set llmRequestRouter.image.repository=nvcf/stargate \
  --set llmRequestRouter.replicaCount=1 \
  --set llmRequestRouter.backendRouter.enabled=false \
  --set llmRequestRouter.certificate.enabled=true \
  --set llmRequestRouter.certificate.issuerRef.name=test-issuer

assert_render_fails "llmRequestRouter.kubernetes.advertisedHostnameTemplate must contain exactly one {pod_name} when backendRouter.enabled is true" \
  --set llmRequestRouter.image.registry=registry.example.invalid \
  --set llmRequestRouter.image.repository=nvcf/stargate \
  --set llmRequestRouter.kubernetes.advertisedHostnameTemplate=llm-request-router.nvcf.svc.cluster.local \
  --set llmRequestRouter.backendRouter.enabled=true \
  --set llmRequestRouter.backendRouter.image.tag=next \
  --set llmRequestRouter.backendRouter.pylonGrpcDialAddress=https://llm-router.example.invalid:443 \
  --set llmRequestRouter.backendRouter.pylonReverseTunnelDialAddress=llm-router.example.invalid:8080

assert_render_fails "llmRequestRouter backend routing requires a TLS Secret and cert/key paths when tls.quicInsecure is false" \
  --set llmRequestRouter.image.registry=registry.example.invalid \
  --set llmRequestRouter.image.repository=nvcf/stargate \
  --set llmRequestRouter.backendRouter.enabled=true \
  --set llmRequestRouter.backendRouter.image.tag=next \
  --set llmRequestRouter.backendRouter.pylonGrpcDialAddress=https://llm-router.example.invalid:443 \
  --set llmRequestRouter.backendRouter.pylonReverseTunnelDialAddress=llm-router.example.invalid:8080 \
  --set llmRequestRouter.tls.quicInsecure=false

assert_render_fails "llmRequestRouter backend routing requires tls.secretName (or certificate secret), tls.certPath, and tls.keyPath together" \
  --set llmRequestRouter.image.registry=registry.example.invalid \
  --set llmRequestRouter.image.repository=nvcf/stargate \
  --set llmRequestRouter.backendRouter.enabled=true \
  --set llmRequestRouter.backendRouter.image.tag=next \
  --set llmRequestRouter.backendRouter.pylonGrpcDialAddress=https://llm-router.example.invalid:443 \
  --set llmRequestRouter.backendRouter.pylonReverseTunnelDialAddress=llm-router.example.invalid:8080 \
  --set llmRequestRouter.tls.certPath=/etc/stargate/tls/tls.crt

assert_render_fails "llmRequestRouter.tls.certPath and llmRequestRouter.tls.keyPath must use the same directory" \
  --set llmRequestRouter.image.registry=registry.example.invalid \
  --set llmRequestRouter.image.repository=nvcf/stargate \
  --set llmRequestRouter.backendRouter.enabled=true \
  --set llmRequestRouter.backendRouter.image.tag=next \
  --set llmRequestRouter.backendRouter.pylonGrpcDialAddress=https://llm-router.example.invalid:443 \
  --set llmRequestRouter.backendRouter.pylonReverseTunnelDialAddress=llm-router.example.invalid:8080 \
  --set llmRequestRouter.tls.secretName=stargate-quic-tls \
  --set llmRequestRouter.tls.certPath=/etc/stargate/tls/tls.crt \
  --set llmRequestRouter.tls.keyPath=/var/run/stargate/tls.key

assert_render_fails "llmRequestRouter.tls.certPath and llmRequestRouter.tls.keyPath must use the same directory" \
  --set llmRequestRouter.image.registry=registry.example.invalid \
  --set llmRequestRouter.image.repository=nvcf/stargate \
  --set llmRequestRouter.replicaCount=1 \
  --set llmRequestRouter.backendRouter.enabled=false \
  --set llmRequestRouter.tls.secretName=stargate-quic-tls \
  --set llmRequestRouter.tls.certPath=/etc/stargate/tls/tls.crt \
  --set llmRequestRouter.tls.keyPath=/var/run/stargate/tls.key

assert_render_fails "llmRequestRouter.tls.mountPath must match the directory containing tls.certPath and tls.keyPath" \
  --set llmRequestRouter.image.registry=registry.example.invalid \
  --set llmRequestRouter.image.repository=nvcf/stargate \
  --set llmRequestRouter.backendRouter.enabled=true \
  --set llmRequestRouter.backendRouter.image.tag=next \
  --set llmRequestRouter.backendRouter.pylonGrpcDialAddress=https://llm-router.example.invalid:443 \
  --set llmRequestRouter.backendRouter.pylonReverseTunnelDialAddress=llm-router.example.invalid:8080 \
  --set llmRequestRouter.tls.secretName=stargate-quic-tls \
  --set llmRequestRouter.tls.mountPath=/var/run/stargate \
  --set llmRequestRouter.tls.certPath=/etc/stargate/tls/tls.crt \
  --set llmRequestRouter.tls.keyPath=/etc/stargate/tls/tls.key

single_replica="$(helm template llm-request-router "$chart_dir" \
  --namespace nvcf \
  --set llmRequestRouter.image.registry=registry.example.invalid \
  --set llmRequestRouter.image.repository=nvcf/stargate \
  --set llmRequestRouter.replicaCount=1 \
  --set llmRequestRouter.backendRouter.enabled=true \
  --set llmRequestRouter.backendRouter.image.tag=next \
  --set llmRequestRouter.backendRouter.pylonGrpcDialAddress=https://llm-router.example.invalid:443 \
  --set llmRequestRouter.backendRouter.pylonReverseTunnelDialAddress=llm-router.example.invalid:8080)"
if ! grep -Fq -- "--advertised-hostname-template={pod_name}.llm-request-router-headless.nvcf.svc.cluster.local" <<<"$single_replica"; then
  echo "FAIL: backend routing must retain per-pod authority and SNI for one replica" >&2
  exit 1
fi

echo "PASS: LLM request-router backend routing renders correctly"
