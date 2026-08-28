#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -eu

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
    --set llmRequestRouter.image.repository=stargate \
    "$@" \
    > "${output}"
}

workload_field() {
  local manifest="$1"
  local kind="$2"
  local expression="$3"
  yq -r "select(.kind == \"${kind}\" and .metadata.name == \"llm-request-router\") | ${expression}" "${manifest}" | head -n1
}

workload_args() {
  local manifest="$1"
  local kind="$2"
  yq -r "select(.kind == \"${kind}\" and .metadata.name == \"llm-request-router\") | .spec.template.spec.containers[0].args[]" "${manifest}"
}

backend_router_args() {
  local manifest="$1"
  yq -r 'select(.kind == "Deployment" and .metadata.name == "llm-request-router-backend-router") | .spec.template.spec.containers[0].args[]' "${manifest}"
}

assert_render_fails() {
  local expected_error="$1"
  shift
  local error_file="${tmp_dir}/render-error"
  if helm template "${release}" "${chart_dir}" \
    --namespace "${namespace}" \
    --values "${chart_dir}/values.yaml" \
    --set llmRequestRouter.image.repository=stargate \
    "$@" \
    > /dev/null 2> "${error_file}"; then
    fail "expected render failure: ${expected_error}"
  fi
  grep -Fq "${expected_error}" "${error_file}" || fail "render did not return expected error: ${expected_error}"
}

default_manifest="${tmp_dir}/default.yaml"
render "${default_manifest}"

[ "$(workload_field "${default_manifest}" Deployment .kind)" = "Deployment" ] || fail "default render did not create Deployment"
[ -z "$(workload_field "${default_manifest}" StatefulSet .kind)" ] || fail "default render also created StatefulSet"
[ "$(workload_field "${default_manifest}" Deployment .spec.replicas)" = "3" ] || fail "default replica count is not 3"
[ "$(workload_field "${default_manifest}" Deployment .spec.strategy.type)" = "RollingUpdate" ] || fail "default Deployment strategy is not RollingUpdate"
[ "$(workload_field "${default_manifest}" Deployment '.spec.strategy.rollingUpdate.maxSurge')" = "1" ] || fail "default Deployment maxSurge is not 1"
[ "$(workload_field "${default_manifest}" Deployment '.spec.strategy.rollingUpdate.maxUnavailable')" = "0" ] || fail "default Deployment maxUnavailable is not 0"
[ "$(workload_field "${default_manifest}" Deployment '.spec.serviceName // ""')" = "" ] || fail "Deployment must not render StatefulSet serviceName"
[ "$(workload_field "${default_manifest}" Deployment '.spec.podManagementPolicy // ""')" = "" ] || fail "Deployment must not render StatefulSet podManagementPolicy"
[ "$(workload_field "${default_manifest}" Deployment '.spec.updateStrategy // ""')" = "" ] || fail "Deployment must not render StatefulSet updateStrategy"
default_backend_kind="$(yq -r 'select(.kind == "Deployment" and .metadata.name == "llm-request-router-backend-router") | .kind' "${default_manifest}" | head -n1)"
[ "${default_backend_kind}" = "Deployment" ] || fail "default multi-replica Deployment did not infer backend-router enablement"

default_args="$(workload_args "${default_manifest}" Deployment)"
default_backend_args="$(backend_router_args "${default_manifest}")"
printf '%s\n' "${default_args}" | grep -qx -- "--advertised-hostname-template={pod_name}.llm-request-router-headless.${namespace}.svc.cluster.local" || fail "default Deployment missing per-pod advertised hostname template"
printf '%s\n' "${default_args}" | grep -qx -- "--grpc-pylon-dial-addr=llm-request-router-backend-router.${namespace}.svc.cluster.local:50071" || fail "default Deployment missing inferred backend-router gRPC dial address"
printf '%s\n' "${default_args}" | grep -qx -- "--watch-heartbeat-ms=5000" || fail "default Deployment missing Watch heartbeat arg"
printf '%s\n' "${default_backend_args}" | grep -qx -- "--watch-heartbeat-ms=5000" || fail "backend router missing Watch heartbeat arg"

multi_deployment_manifest="${tmp_dir}/multi-deployment.yaml"
render "${multi_deployment_manifest}" \
  --set llmRequestRouter.replicaCount=3 \
  --set llmRequestRouter.backendRouter.enabled=true

multi_deployment_args="$(workload_args "${multi_deployment_manifest}" Deployment)"
printf '%s\n' "${multi_deployment_args}" | grep -qx -- "--advertised-hostname-template={pod_name}.llm-request-router-headless.${namespace}.svc.cluster.local" || fail "multi-replica Deployment missing per-pod advertised hostname template"
printf '%s\n' "${multi_deployment_args}" | grep -qx -- "--grpc-pylon-dial-addr=llm-request-router-backend-router.${namespace}.svc.cluster.local:50071" || fail "multi-replica Deployment missing backend-router gRPC dial address"

statefulset_manifest="${tmp_dir}/statefulset.yaml"
render "${statefulset_manifest}" \
  --set llmRequestRouter.workload.kind=StatefulSet \
  --set llmRequestRouter.replicaCount=3

[ "$(workload_field "${statefulset_manifest}" StatefulSet .kind)" = "StatefulSet" ] || fail "explicit StatefulSet render did not create StatefulSet"
[ -z "$(workload_field "${statefulset_manifest}" Deployment .kind)" ] || fail "explicit StatefulSet render also created Deployment"
[ "$(workload_field "${statefulset_manifest}" StatefulSet .spec.serviceName)" = "llm-request-router-headless" ] || fail "StatefulSet serviceName is not llm-request-router-headless"
[ "$(workload_field "${statefulset_manifest}" StatefulSet .spec.podManagementPolicy)" = "Parallel" ] || fail "StatefulSet podManagementPolicy is not Parallel"
[ "$(workload_field "${statefulset_manifest}" StatefulSet .spec.updateStrategy.type)" = "RollingUpdate" ] || fail "StatefulSet updateStrategy is not RollingUpdate"
[ "$(workload_field "${statefulset_manifest}" StatefulSet '.spec.strategy // ""')" = "" ] || fail "StatefulSet must not render Deployment strategy"
statefulset_backend_kind="$(yq -r 'select(.kind == "Deployment" and .metadata.name == "llm-request-router-backend-router") | .kind' "${statefulset_manifest}" | head -n1)"
[ -z "${statefulset_backend_kind}" ] || fail "pinned StatefulSet unexpectedly inferred backend-router enablement"

statefulset_args="$(workload_args "${statefulset_manifest}" StatefulSet)"
printf '%s\n' "${statefulset_args}" | grep -qx -- "--stargate-discovery-dns-name=llm-request-router-headless.${namespace}.svc.cluster.local" || fail "StatefulSet direct mode missing headless discovery DNS arg"
printf '%s\n' "${statefulset_args}" | grep -qx -- '--reverse-tunnel-pylon-dial-addr=$(POD_IP):50072' || fail "StatefulSet direct mode missing per-pod reverse tunnel address"

assert_render_fails "llmRequestRouter.workload.kind must be Deployment or StatefulSet, got \"DaemonSet\"" \
  --set llmRequestRouter.workload.kind=DaemonSet

assert_render_fails "llmRequestRouter.backendRouter.enabled must be true when llmRequestRouter.workload.kind is Deployment and replicaCount is greater than 1" \
  --set llmRequestRouter.workload.kind=Deployment \
  --set llmRequestRouter.replicaCount=2 \
  --set llmRequestRouter.backendRouter.enabled=false

single_deployment_manifest="${tmp_dir}/single-deployment.yaml"
render "${single_deployment_manifest}" \
  --set llmRequestRouter.workload.kind=Deployment \
  --set llmRequestRouter.replicaCount=1 \
  --set llmRequestRouter.backendRouter.enabled=false

single_deployment_args="$(workload_args "${single_deployment_manifest}" Deployment)"
printf '%s\n' "${single_deployment_args}" | grep -qx -- "--disable-dns-discovery" || fail "single-replica direct Deployment missing --disable-dns-discovery"

assert_render_fails "llmRequestRouter.discovery.disableDnsDiscovery cannot be true when llmRequestRouter.replicaCount is greater than 1; multi-replica routers require DNS discovery" \
  --set llmRequestRouter.workload.kind=StatefulSet \
  --set llmRequestRouter.replicaCount=3 \
  --set llmRequestRouter.backendRouter.enabled=false \
  --set llmRequestRouter.discovery.disableDnsDiscovery=true

assert_render_fails "llmRequestRouter.discovery.watchHeartbeatMs must be greater than 0" \
  --set llmRequestRouter.discovery.watchHeartbeatMs=0

echo "dual workload render checks passed"
