#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

chart_dir="${1:-./llm-request-router}"
release="${RELEASE:-llm-request-router}"
namespace="${NAMESPACE:-nvcf}"
tmp_dir="$(mktemp -d)"

cleanup() {
  rm -rf "${tmp_dir}"
}
trap cleanup EXIT

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

render() {
  local output="$1"
  shift
  helm template "${release}" "${chart_dir}" \
    --namespace "${namespace}" \
    --values "${chart_dir}/values.yaml" \
    "$@" \
    > "${output}"
}

statefulset_field() {
  local manifest="$1"
  local expression="$2"
  yq -r "select(.kind == \"StatefulSet\" and .metadata.name == \"llm-request-router\") | ${expression}" "${manifest}" | head -n1
}

statefulset_args() {
  local manifest="$1"
  yq -r 'select(.kind == "StatefulSet" and .metadata.name == "llm-request-router") | .spec.template.spec.containers[0].args[]' "${manifest}"
}

per_pod_service_names() {
  local manifest="$1"
  yq -r 'select(.kind == "Service" and (.metadata.name | test("^llm-request-router-[0-9]+$"))) | .metadata.name' "${manifest}" \
    | grep -v '^---$' \
    | sort
}

service_field() {
  local manifest="$1"
  local name="$2"
  local expression="$3"
  yq -r "select(.kind == \"Service\" and .metadata.name == \"${name}\") | (${expression})" "${manifest}" | head -n1
}

certificate_dns_names() {
  local manifest="$1"
  yq -r 'select(.kind == "Certificate") | .spec.dnsNames[]' "${manifest}"
}

default_manifest="${tmp_dir}/default.yaml"
render "${default_manifest}"

[ "$(statefulset_field "${default_manifest}" ".kind")" = "StatefulSet" ] || fail "default render did not create StatefulSet"
[ "$(statefulset_field "${default_manifest}" ".spec.serviceName")" = "llm-request-router-headless" ] || fail "default StatefulSet serviceName is not llm-request-router-headless"
[ "$(statefulset_field "${default_manifest}" ".spec.replicas")" = "3" ] || fail "default replica count is not 3"
[ "$(statefulset_field "${default_manifest}" ".spec.podManagementPolicy")" = "Parallel" ] || fail "default StatefulSet podManagementPolicy is not Parallel"

default_args="$(statefulset_args "${default_manifest}")"
printf '%s\n' "${default_args}" | grep -qx -- "--stargate-discovery-dns-name=llm-request-router-headless.${namespace}.svc.cluster.local" || fail "default render missing headless discovery DNS arg"
printf '%s\n' "${default_args}" | grep -qx -- "--advertised-hostname-template={pod_name}.llm-request-router-headless.${namespace}.svc.cluster.local" || fail "default render missing per-pod advertised hostname template"
printf '%s\n' "${default_args}" | grep -qx -- '--reverse-tunnel-pylon-dial-addr=$(POD_IP):50072' || fail "default render missing reverse tunnel pylon dial addr"
if printf '%s\n' "${default_args}" | grep -qx -- "--disable-dns-discovery"; then
  fail "default multi-replica render must not disable DNS discovery"
fi

invalid_error="${tmp_dir}/invalid.err"
if helm template "${release}" "${chart_dir}" \
  --namespace "${namespace}" \
  --values "${chart_dir}/values.yaml" \
  --set llmRequestRouter.replicaCount=3 \
  --set llmRequestRouter.discovery.disableDnsDiscovery=true \
  > "${tmp_dir}/invalid.yaml" 2> "${invalid_error}"; then
  fail "multi-replica render with disabled DNS discovery unexpectedly succeeded"
fi
grep -Fq "llmRequestRouter.discovery.disableDnsDiscovery cannot be true when llmRequestRouter.replicaCount is greater than 1; multi-replica routers require DNS discovery" "${invalid_error}" || fail "invalid render did not return the expected guard message"

single_manifest="${tmp_dir}/single.yaml"
render "${single_manifest}" \
  --set llmRequestRouter.replicaCount=1 \
  --set llmRequestRouter.discovery.disableDnsDiscovery=true

single_args="$(statefulset_args "${single_manifest}")"
printf '%s\n' "${single_args}" | grep -qx -- "--disable-dns-discovery" || fail "single-replica self-only render missing --disable-dns-discovery"

custom_template_manifest="${tmp_dir}/custom-template.yaml"
render "${custom_template_manifest}" \
  --set llmRequestRouter.replicaCount=3 \
  --set-string llmRequestRouter.kubernetes.advertisedHostnameTemplate=router.example.internal

custom_template_args="$(statefulset_args "${custom_template_manifest}")"
printf '%s\n' "${custom_template_args}" | grep -qx -- '--reverse-tunnel-pylon-dial-addr=$(POD_IP):50072' || fail "custom multi-replica advertised hostname template missing reverse tunnel pylon dial addr"

[ -z "$(per_pod_service_names "${default_manifest}")" ] || fail "default render unexpectedly created per-pod Services"
if printf '%s\n' "${default_args}" | grep -q -- '--grpc-pylon-dial-addr'; then
  fail "default render unexpectedly configured an external gRPC dial address"
fi

external_domain="router.region.example"
external_manifest="${tmp_dir}/external-access.yaml"
render "${external_manifest}" \
  --set llmRequestRouter.replicaCount=2 \
  --set llmRequestRouter.service.type=NodePort \
  --set-string llmRequestRouter.service.annotations.shared=seed \
  --set llmRequestRouter.externalAccess.enabled=true \
  --set-string llmRequestRouter.externalAccess.domain="${external_domain}" \
  --set llmRequestRouter.externalAccess.service.type=NodePort \
  --set-string llmRequestRouter.externalAccess.service.annotations.scope=replica \
  --set-string 'llmRequestRouter.discovery.remoteStargateURLs[0]=https://watch-a.example:50071' \
  --set-string 'llmRequestRouter.discovery.remoteStargateURLs[1]=https://watch-b.example:50071'

external_args="$(statefulset_args "${external_manifest}")"
printf '%s\n' "${external_args}" | grep -qx -- "--grpc-pylon-dial-addr={stargate_id}.${external_domain}:50071" || fail "external render missing templated per-replica gRPC dial address"
printf '%s\n' "${external_args}" | grep -qx -- "--reverse-tunnel-pylon-dial-addr=\$(POD_NAME).${external_domain}:50072" || fail "external render missing per-replica QUIC dial address"
[ "$(printf '%s\n' "${external_args}" | grep -cx -- '--remote-stargate-url=https://watch-a.example:50071')" = "1" ] || fail "first remote Stargate URL was not rendered exactly once"
[ "$(printf '%s\n' "${external_args}" | grep -cx -- '--remote-stargate-url=https://watch-b.example:50071')" = "1" ] || fail "second remote Stargate URL was not rendered exactly once"

expected_services="$(printf 'llm-request-router-0\nllm-request-router-1\n')"
[ "$(per_pod_service_names "${external_manifest}")" = "${expected_services}" ] || fail "external render did not create exactly two per-pod Services"
[ "$(service_field "${external_manifest}" llm-request-router-1 '.spec.selector."statefulset.kubernetes.io/pod-name"')" = "llm-request-router-1" ] || fail "per-pod Service is not pinned to its StatefulSet replica"
[ "$(service_field "${external_manifest}" llm-request-router-1 '.spec.type')" = "NodePort" ] || fail "per-pod Service type override was not applied"
[ "$(service_field "${external_manifest}" llm-request-router-1 '.metadata.annotations.scope')" = "replica" ] || fail "per-pod Service annotations were not applied"
[ "$(service_field "${external_manifest}" llm-request-router-1 '.spec.ports[] | select(.name == "grpc") | .port')" = "50071" ] || fail "per-pod Service missing registration port 50071"
[ "$(service_field "${external_manifest}" llm-request-router-1 '.spec.ports[] | select(.name == "grpc") | .protocol')" = "TCP" ] || fail "per-pod registration port is not TCP"
[ "$(service_field "${external_manifest}" llm-request-router-1 '.spec.ports[] | select(.name == "quic") | .port')" = "50072" ] || fail "per-pod Service missing reverse-tunnel port 50072"
[ "$(service_field "${external_manifest}" llm-request-router-1 '.spec.ports[] | select(.name == "quic") | .protocol')" = "UDP" ] || fail "per-pod reverse-tunnel port is not UDP"
[ "$(service_field "${external_manifest}" llm-request-router '.metadata.annotations.shared')" = "seed" ] || fail "shared seed Service annotations were not applied"

scaled_manifest="${tmp_dir}/external-access-scaled.yaml"
render "${scaled_manifest}" \
  --set llmRequestRouter.replicaCount=4 \
  --set llmRequestRouter.externalAccess.enabled=true \
  --set-string llmRequestRouter.externalAccess.domain="${external_domain}"
[ "$(per_pod_service_names "${scaled_manifest}" | wc -l | tr -d ' ')" = "4" ] || fail "per-pod Service count does not follow replicaCount"

assert_render_fails() {
  local description="$1"
  local expected="$2"
  shift 2
  local error_file="${tmp_dir}/external-invalid.err"
  if helm template "${release}" "${chart_dir}" \
    --namespace "${namespace}" \
    --values "${chart_dir}/values.yaml" \
    "$@" \
    > "${tmp_dir}/external-invalid.yaml" 2> "${error_file}"; then
    fail "${description} unexpectedly succeeded"
  fi
  grep -Fq "${expected}" "${error_file}" || fail "${description} did not return the expected guard message"
}

assert_render_fails \
  "external access without a domain" \
  "llmRequestRouter.externalAccess.domain is required" \
  --set llmRequestRouter.externalAccess.enabled=true

assert_render_fails \
  "external access with an invalid domain" \
  "is not a valid DNS name" \
  --set llmRequestRouter.externalAccess.enabled=true \
  --set-string llmRequestRouter.externalAccess.domain='not_a_domain.example'

assert_render_fails \
  "external access without a reverse tunnel listener" \
  "llmRequestRouter.transport.reverseTunnelListenAddr is required" \
  --set llmRequestRouter.externalAccess.enabled=true \
  --set-string llmRequestRouter.externalAccess.domain="${external_domain}" \
  --set-string llmRequestRouter.transport.reverseTunnelListenAddr=''

pki_manifest="${tmp_dir}/external-access-pki.yaml"
render "${pki_manifest}" \
  --set llmRequestRouter.replicaCount=2 \
  --set llmRequestRouter.externalAccess.enabled=true \
  --set-string llmRequestRouter.externalAccess.domain="${external_domain}" \
  --set llmRequestRouter.certificate.enabled=true \
  --set-string llmRequestRouter.certificate.issuerRef.name=test-issuer \
  --set-string 'llmRequestRouter.certificate.dnsNames[0]=llm-request-router.nvcf.svc.cluster.local' \
  --set-string 'llmRequestRouter.certificate.dnsNames[1]=*.llm-request-router-headless.nvcf.svc.cluster.local'
pki_dns_names="$(certificate_dns_names "${pki_manifest}")"
printf '%s\n' "${pki_dns_names}" | grep -qx -- 'llm-request-router.nvcf.svc.cluster.local' || fail "PKI render missing exact internal Service SAN"
printf '%s\n' "${pki_dns_names}" | grep -Fqx -- '*.llm-request-router-headless.nvcf.svc.cluster.local' || fail "PKI render missing wildcard internal headless Service SAN"
if printf '%s\n' "${pki_dns_names}" | grep -Fq -- "${external_domain}"; then
  fail "PKI render unexpectedly added the external dial domain to certificate SANs"
fi

echo "multi-replica render checks passed"
