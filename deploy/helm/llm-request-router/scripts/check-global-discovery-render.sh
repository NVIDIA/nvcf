#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Render checks for llmRequestRouter.globalDiscovery: the Stargate GDS flags,
# the GDS CA mount, and the export labels on the Service pylons dial.

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
chart_dir="${script_dir}/../llm-request-router"
tmp_dir="$(mktemp -d)"
trap 'rm -rf "${tmp_dir}"' EXIT

for tool in helm yq; do
  command -v "${tool}" >/dev/null 2>&1 || { echo "FAIL: ${tool} not found on PATH" >&2; exit 1; }
done

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

base_args=(
  --namespace nvcf
  --set llmRequestRouter.image.registry=registry.example.invalid
  --set llmRequestRouter.image.repository=nvcf/stargate
)

gds_args=(
  --set llmRequestRouter.globalDiscovery.enabled=true
  --set 'llmRequestRouter.globalDiscovery.urls={https://gds.example.test,https://gds.us-east-1.example.test}'
  --set llmRequestRouter.globalDiscovery.clustersetId=prod
  --set llmRequestRouter.globalDiscovery.clusterId=cluster-a
)

render() {  # render <output> [helm args...]
  local output="$1"
  shift
  helm template llm-request-router "${chart_dir}" "${base_args[@]}" "$@" >"${output}"
}

assert_render_fails() {  # assert_render_fails <expected error> [helm args...]
  local expected_error="$1"
  shift
  local error_file="${tmp_dir}/render-error"
  if helm template llm-request-router "${chart_dir}" "${base_args[@]}" "$@" >/dev/null 2>"${error_file}"; then
    fail "expected Helm render to fail: ${expected_error}"
  fi
  grep -Fq -- "${expected_error}" "${error_file}" ||
    fail "render did not return the expected error: ${expected_error}; got: $(cat "${error_file}")"
}

stargate_field() {  # stargate_field <manifest> <expression>
  yq -r "select(.kind == \"Deployment\" and .metadata.name == \"llm-request-router\") | $2" "$1"
}

stargate_args() {
  stargate_field "$1" '.spec.template.spec.containers[0].args[]'
}

has_arg() {
  printf '%s\n' "$1" | grep -qx -- "$2"
}

service_field() {  # service_field <manifest> <name> <expression>
  yq -r "select(.kind == \"Service\" and .metadata.name == \"$2\") | $3" "$1"
}

# Defaults: no GDS flags, no export labels on any Service.
default_manifest="${tmp_dir}/default.yaml"
render "${default_manifest}"
! stargate_args "${default_manifest}" | grep -q -- '^--gds-' || fail "default must not render GDS flags"
[ "$(yq -r 'select(.kind == "Service") | .metadata.labels["gds.nvidia.com/export"] // ""' "${default_manifest}" | grep -c true || true)" = "0" ] ||
  fail "default must not export any Service"

# enabled renders the Stargate flags; the CA ConfigMap is mounted.
gds_manifest="${tmp_dir}/gds.yaml"
render "${gds_manifest}" "${gds_args[@]}" --set llmRequestRouter.globalDiscovery.tlsCaConfigMap=gds-ca
args="$(stargate_args "${gds_manifest}")"
for expected in \
  --gds-url=https://gds.example.test \
  --gds-url=https://gds.us-east-1.example.test \
  --gds-clusterset=prod \
  --gds-cluster-id=cluster-a \
  --gds-tls-ca-cert-path=/etc/stargate/gds-tls/ca.crt; do
  has_arg "${args}" "${expected}" || fail "enabled must render ${expected}"
done
[ "$(stargate_field "${gds_manifest}" '.spec.template.spec.volumes[] | select(.name == "gds-tls") | .configMap.name')" = "gds-ca" ] ||
  fail "tlsCaConfigMap must be the gds-tls volume"
[ "$(stargate_field "${gds_manifest}" '.spec.template.spec.containers[0].volumeMounts[] | select(.name == "gds-tls") | .mountPath')" = "/etc/stargate/gds-tls" ] ||
  fail "the GDS CA must be mounted at /etc/stargate/gds-tls"
[ "$(yq -r 'select(.kind == "Service") | .metadata.labels["gds.nvidia.com/export"] // ""' "${gds_manifest}" | grep -c true || true)" = "0" ] ||
  fail "enabled alone must not export a Service"

no_ca_manifest="${tmp_dir}/no-ca.yaml"
render "${no_ca_manifest}" "${gds_args[@]}"
! stargate_args "${no_ca_manifest}" | grep -q -- '^--gds-tls-ca-cert-path' || fail "no GDS CA flag without tlsCaConfigMap"
[ -z "$(stargate_field "${no_ca_manifest}" '.spec.template.spec.volumes[] | select(.name == "gds-tls") | .name')" ] ||
  fail "no GDS CA volume without tlsCaConfigMap"

# Plaintext GDS needs the development opt-in.
assert_render_fails "llmRequestRouter.globalDiscovery.urls requires https://" \
  --set llmRequestRouter.globalDiscovery.enabled=true \
  --set 'llmRequestRouter.globalDiscovery.urls={http://gds.gds.svc.cluster.local:9090}' \
  --set llmRequestRouter.globalDiscovery.clustersetId=dev \
  --set llmRequestRouter.globalDiscovery.clusterId=cluster-a
plaintext_manifest="${tmp_dir}/plaintext.yaml"
render "${plaintext_manifest}" \
  --set llmRequestRouter.discovery.allowInsecureRemoteWatchHttp=true \
  --set llmRequestRouter.globalDiscovery.enabled=true \
  --set 'llmRequestRouter.globalDiscovery.urls={http://gds.gds.svc.cluster.local:9090}' \
  --set llmRequestRouter.globalDiscovery.clustersetId=dev \
  --set llmRequestRouter.globalDiscovery.clusterId=cluster-a
plaintext_args="$(stargate_args "${plaintext_manifest}")"
has_arg "${plaintext_args}" "--gds-url=http://gds.gds.svc.cluster.local:9090" || fail "the opt-in must permit a plaintext GDS URL"
has_arg "${plaintext_args}" "--allow-insecure-remote-watch-http" || fail "the opt-in must reach Stargate"

# Incomplete or invalid values fail the render.
assert_render_fails "llmRequestRouter.globalDiscovery.urls is required when globalDiscovery.enabled is true" \
  --set llmRequestRouter.globalDiscovery.enabled=true
assert_render_fails "llmRequestRouter.globalDiscovery.clustersetId is required when globalDiscovery.enabled is true" \
  --set llmRequestRouter.globalDiscovery.enabled=true \
  --set 'llmRequestRouter.globalDiscovery.urls={https://gds.example.test}'
assert_render_fails "llmRequestRouter.globalDiscovery.clusterId is required when globalDiscovery.enabled is true" \
  --set llmRequestRouter.globalDiscovery.enabled=true \
  --set 'llmRequestRouter.globalDiscovery.urls={https://gds.example.test}' \
  --set llmRequestRouter.globalDiscovery.clustersetId=prod
assert_render_fails "'/llmRequestRouter/globalDiscovery/urls/0': 'gds.example.test:443' does not match pattern" \
  "${gds_args[@]}" \
  --set 'llmRequestRouter.globalDiscovery.urls={gds.example.test:443}'
assert_render_fails "'/llmRequestRouter/globalDiscovery/clusterId': 'Cluster_A' does not match pattern" \
  "${gds_args[@]}" \
  --set llmRequestRouter.globalDiscovery.clusterId=Cluster_A
assert_render_fails "'/llmRequestRouter/globalDiscovery/dialScheme': value must be one of 'http', 'https'" \
  --set llmRequestRouter.globalDiscovery.export.enabled=true \
  --set llmRequestRouter.globalDiscovery.dialScheme=grpc
assert_render_fails "additional properties 'url' not allowed" \
  --set llmRequestRouter.globalDiscovery.url=https://gds.example.test

# export labels the backend-router Service when the backend router runs: a
# multi-replica Deployment, the default.
export_manifest="${tmp_dir}/export.yaml"
render "${export_manifest}" --set llmRequestRouter.globalDiscovery.export.enabled=true
backend_router="llm-request-router-backend-router"
[ "$(service_field "${export_manifest}" "${backend_router}" '.metadata.labels["gds.nvidia.com/export"]')" = "true" ] ||
  fail "export must label the backend-router Service gds.nvidia.com/export=true"
[ "$(service_field "${export_manifest}" "${backend_router}" '.metadata.labels["gds.nvidia.com/role"]')" = "router" ] ||
  fail "export must label the backend-router Service gds.nvidia.com/role=router"
[ "$(service_field "${export_manifest}" "${backend_router}" '.metadata.annotations["gds.nvidia.com/dial-scheme"]')" = "http" ] ||
  fail "export must annotate the backend-router Service with dial scheme http by default"
[ "$(service_field "${export_manifest}" "${backend_router}" '.spec.ports[] | select(.name == "grpc") | .port')" = \
  "$(yq -r 'select(.kind == "Deployment" and .metadata.name == "llm-request-router-backend-router") | .spec.template.spec.containers[0].ports[] | select(.name == "grpc") | .containerPort' "${export_manifest}")" ] ||
  fail "the exported gRPC port must equal the backend router's container port, the seed port"
for service in llm-request-router llm-request-router-headless; do
  [ "$(service_field "${export_manifest}" "${service}" '.metadata.labels["gds.nvidia.com/export"] // ""')" = "" ] ||
    fail "with the backend router, ${service} must not be exported"
done

# Without the backend router pylons dial the ClusterIP Service, which is
# exported instead.
single_manifest="${tmp_dir}/single.yaml"
render "${single_manifest}" \
  --set llmRequestRouter.replicaCount=1 \
  --set llmRequestRouter.globalDiscovery.export.enabled=true \
  --set llmRequestRouter.globalDiscovery.dialScheme=https
[ -z "$(service_field "${single_manifest}" "${backend_router}" '.metadata.name')" ] || fail "a single replica runs no backend router"
[ "$(service_field "${single_manifest}" llm-request-router '.metadata.labels["gds.nvidia.com/export"]')" = "true" ] ||
  fail "without the backend router, export must label the ClusterIP Service"
[ "$(service_field "${single_manifest}" llm-request-router '.metadata.labels["gds.nvidia.com/role"]')" = "router" ] ||
  fail "without the backend router, the ClusterIP Service must be the router"
[ "$(service_field "${single_manifest}" llm-request-router '.metadata.annotations["gds.nvidia.com/dial-scheme"]')" = "https" ] ||
  fail "dialScheme must reach the annotation"
[ "$(service_field "${single_manifest}" llm-request-router-headless '.metadata.labels["gds.nvidia.com/export"] // ""')" = "" ] ||
  fail "the headless Service is never exported"

echo "global discovery render checks passed"
