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

echo "multi-replica render checks passed"
