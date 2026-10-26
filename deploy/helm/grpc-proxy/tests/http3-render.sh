#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     https://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
chart_dir="${script_dir}/../grpc-proxy"
repo_root="$(cd "${script_dir}/../../../.." && pwd)"
ci_values="${repo_root}/tools/ci/helm-validate-values/grpc-proxy.yaml"
namespace="nvcf"
tmp_dir="$(mktemp -d)"
trap 'rm -rf "${tmp_dir}"' EXIT

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

render() {
  local output="$1"
  shift
  helm template grpc-proxy "${chart_dir}" --namespace "${namespace}" \
    --values "${ci_values}" "$@" > "${output}"
}

assert_render_fails() {
  local expected="$1"
  shift
  if helm template grpc-proxy "${chart_dir}" --namespace "${namespace}" \
    --values "${ci_values}" "$@" > /dev/null 2> "${tmp_dir}/err"; then
    fail "render succeeded, expected error: ${expected}"
  fi
  grep -qF "${expected}" "${tmp_dir}/err" || fail "missing error '${expected}': $(cat "${tmp_dir}/err")"
}

# workload_env MANIFEST KIND NAME prints the container env value for NAME.
workload_env() {
  yq -r "select(.kind == \"$2\") | .spec.template.spec.containers[0].env[] | select(.name == \"$3\") | .value" "$1"
}

expect_eq() {
  [[ "$2" == "$3" ]] || fail "$1: expected '$3', got '$2'"
}

check_http3_workload() {
  local manifest="$1" kind="$2" secret="$3"
  expect_eq "${kind} ENABLE_HTTP3_CONNECT" "$(workload_env "${manifest}" "${kind}" ENABLE_HTTP3_CONNECT)" "true"
  expect_eq "${kind} ENABLE_HTTP1_CONNECT" "$(workload_env "${manifest}" "${kind}" ENABLE_HTTP1_CONNECT)" "false"
  expect_eq "${kind} ENABLE_HTTP1_CONNECT count" \
    "$(workload_env "${manifest}" "${kind}" ENABLE_HTTP1_CONNECT | wc -l | tr -d ' ')" "1"
  expect_eq "${kind} SELF_WORKER_FQDN count" \
    "$(workload_env "${manifest}" "${kind}" SELF_WORKER_FQDN | wc -l | tr -d ' ')" "1"
  expect_eq "${kind} PUBLIC_CERT_PATH" "$(workload_env "${manifest}" "${kind}" PUBLIC_CERT_PATH)" "/etc/grpc-proxy-tls/tls.crt"
  expect_eq "${kind} PRIVATE_KEY_PATH" "$(workload_env "${manifest}" "${kind}" PRIVATE_KEY_PATH)" "/etc/grpc-proxy-tls/tls.key"
  expect_eq "${kind} POD_IP" \
    "$(yq -r "select(.kind == \"${kind}\") | .spec.template.spec.containers[0].env[] | select(.name == \"POD_IP\") | .valueFrom.fieldRef.fieldPath" "${manifest}")" "status.podIP"
  expect_eq "${kind} UDP port" \
    "$(yq -r "select(.kind == \"${kind}\") | .spec.template.spec.containers[0].ports[] | select(.containerPort == 10084) | .protocol" "${manifest}")" "UDP"
  expect_eq "${kind} TLS mount" \
    "$(yq -r "select(.kind == \"${kind}\") | .spec.template.spec.containers[0].volumeMounts[] | select(.name == \"worker-tls\") | .mountPath" "${manifest}")" "/etc/grpc-proxy-tls"
  expect_eq "${kind} TLS secret" \
    "$(yq -r "select(.kind == \"${kind}\") | .spec.template.spec.volumes[] | select(.name == \"worker-tls\") | .secret.secretName" "${manifest}")" "${secret}"
  expect_eq "ConfigMap ENABLE_HTTP1_CONNECT" \
    "$(yq -r 'select(.kind == "ConfigMap" and (.metadata.name | test("-env$"))) | .data.ENABLE_HTTP1_CONNECT' "${manifest}")" "false"
}

# Disabled by default: no UDP port, TLS mount, or Certificate.
render "${tmp_dir}/default.yaml"
grep -q 'containerPort: 10084' "${tmp_dir}/default.yaml" && fail "default render opens UDP 10084"
grep -q 'worker-tls' "${tmp_dir}/default.yaml" && fail "default render mounts worker TLS"
grep -q 'kind: Certificate' "${tmp_dir}/default.yaml" && fail "default render creates a Certificate"
expect_eq "default ENABLE_HTTP1_CONNECT" "$(workload_env "${tmp_dir}/default.yaml" Deployment ENABLE_HTTP1_CONNECT)" "true"

# Deployment with a cert-manager Certificate and the headless default base URL.
render "${tmp_dir}/cert.yaml" \
  --set grpcproxy.http3.enabled=true \
  --set grpcproxy.http3.tls.certificate.enabled=true \
  --set grpcproxy.http3.tls.certificate.issuerRef.name=nvcf-worker-callback
secret="$(yq -r 'select(.kind == "Certificate") | .spec.secretName' "${tmp_dir}/cert.yaml")"
expect_eq "Certificate secret" "${secret}" "grpc-proxy-helm-nvcf-grpc-proxy-worker-tls"
check_http3_workload "${tmp_dir}/cert.yaml" Deployment "${secret}"
expect_eq "SELF_WORKER_FQDN" "$(workload_env "${tmp_dir}/cert.yaml" Deployment SELF_WORKER_FQDN)" \
  "https://grpc-headless.${namespace}.svc.cluster.local:10084"
expect_eq "Certificate dnsNames" "$(yq -r 'select(.kind == "Certificate") | .spec.dnsNames[0]' "${tmp_dir}/cert.yaml")" \
  "*.grpc-headless.${namespace}.svc.cluster.local"
expect_eq "Certificate issuer" "$(yq -r 'select(.kind == "Certificate") | .spec.issuerRef.name' "${tmp_dir}/cert.yaml")" "nvcf-worker-callback"
for svc in grpc grpc-headless; do
  expect_eq "Service ${svc} UDP port" \
    "$(yq -r "select(.kind == \"Service\" and .metadata.name == \"${svc}\") | .spec.ports[] | select(.port == 10084) | .protocol" "${tmp_dir}/cert.yaml")" "UDP"
done

# DaemonSet with an existing Secret and an explicit base URL.
render "${tmp_dir}/daemonset.yaml" \
  --set grpcproxy.deploymentType=daemonset \
  --set grpcproxy.http3.enabled=true \
  --set grpcproxy.http3.tls.secretName=worker-callback-tls \
  --set grpcproxy.workerConnectBaseURL=https://grpc.example.com:10084
grep -q 'kind: Certificate' "${tmp_dir}/daemonset.yaml" && fail "existing Secret render creates a Certificate"
check_http3_workload "${tmp_dir}/daemonset.yaml" DaemonSet worker-callback-tls
expect_eq "DaemonSet SELF_WORKER_FQDN" "$(workload_env "${tmp_dir}/daemonset.yaml" DaemonSet SELF_WORKER_FQDN)" \
  "https://grpc.example.com:10084"

# Certificate names follow the advertised base host.
render "${tmp_dir}/base-url.yaml" \
  --set grpcproxy.http3.enabled=true \
  --set grpcproxy.http3.tls.certificate.enabled=true \
  --set grpcproxy.http3.tls.certificate.issuerRef.name=nvcf-worker-callback \
  --set grpcproxy.workerConnectBaseURL=https://grpc.example.com:10084
expect_eq "base URL Certificate dnsNames" "$(yq -r 'select(.kind == "Certificate") | .spec.dnsNames[0]' "${tmp_dir}/base-url.yaml")" \
  "*.grpc.example.com"
render "${tmp_dir}/explicit-names.yaml" \
  --set grpcproxy.http3.enabled=true \
  --set grpcproxy.http3.tls.certificate.enabled=true \
  --set grpcproxy.http3.tls.certificate.issuerRef.name=nvcf-worker-callback \
  --set grpcproxy.workerConnectBaseURL=https://grpc.example.com:10084 \
  --set 'grpcproxy.http3.tls.certificate.dnsNames[0]=*.custom.example.org' \
  --set 'grpcproxy.http3.tls.certificate.dnsNames[1]=*.grpc.example.com'
expect_eq "explicit Certificate dnsNames" "$(yq -r 'select(.kind == "Certificate") | .spec.dnsNames | join(",")' "${tmp_dir}/explicit-names.yaml")" \
  "*.custom.example.org,*.grpc.example.com"
assert_render_fails 'dnsNames must include "*.grpc.example.com"' \
  --set grpcproxy.http3.enabled=true \
  --set grpcproxy.http3.tls.certificate.enabled=true \
  --set grpcproxy.http3.tls.certificate.issuerRef.name=nvcf-worker-callback \
  --set grpcproxy.workerConnectBaseURL=https://grpc.example.com:10084 \
  --set 'grpcproxy.http3.tls.certificate.dnsNames[0]=*.custom.example.org'

# One effective callback URL: invalid combinations fail instead of rendering
# an address that HTTP/3-only pods do not serve.
http3_secret=(--set grpcproxy.http3.enabled=true --set grpcproxy.http3.tls.secretName=x)
assert_render_fails 'grpcproxy.workerConnectBaseURL "http://grpc.example.com:10086" is not a valid HTTP/3 callback URL: it must use https' \
  "${http3_secret[@]}" --set grpcproxy.workerConnectBaseURL=http://grpc.example.com:10086
assert_render_fails "its host is an IP address" \
  "${http3_secret[@]}" --set grpcproxy.workerConnectBaseURL=https://10.0.0.1:10084
assert_render_fails "must not have a path" \
  "${http3_secret[@]}" --set grpcproxy.workerConnectBaseURL=https://grpc.example.com/v1/proxy

# workerConnectBaseURL is the only chart input for the callback address. The
# self-managed stack sizes grpc-proxy from it, so env.SELF_WORKER_FQDN must
# fail in every mode rather than reach the pods unseen.
env_error="grpcproxy.env.SELF_WORKER_FQDN is not supported; set grpcproxy.workerConnectBaseURL"
for type in deployment daemonset; do
  assert_render_fails "${env_error}" \
    --set grpcproxy.deploymentType="${type}" --set grpcproxy.env.SELF_WORKER_FQDN=http://grpc.example.com:10086
  assert_render_fails "${env_error}" \
    --set grpcproxy.deploymentType="${type}" --set-string grpcproxy.env.SELF_WORKER_FQDN=
  assert_render_fails "${env_error}" \
    --set grpcproxy.deploymentType="${type}" "${http3_secret[@]}" \
    --set grpcproxy.env.SELF_WORKER_FQDN=https://grpc.example.com:10084
  assert_render_fails "${env_error}" \
    --set grpcproxy.deploymentType="${type}" "${http3_secret[@]}" \
    --set grpcproxy.workerConnectBaseURL=https://a.example.com \
    --set grpcproxy.env.SELF_WORKER_FQDN=https://b.example.com
done

# The chart value becomes the service's SELF_WORKER_FQDN, once, in both modes.
for kind in Deployment DaemonSet; do
  type="$(tr '[:upper:]' '[:lower:]' <<<"${kind}")"
  render "${tmp_dir}/http1-${type}.yaml" \
    --set grpcproxy.deploymentType="${type}" \
    --set grpcproxy.workerConnectBaseURL=http://grpc.example.com:10086
  expect_eq "${kind} HTTP/1 SELF_WORKER_FQDN" \
    "$(workload_env "${tmp_dir}/http1-${type}.yaml" "${kind}" SELF_WORKER_FQDN)" "http://grpc.example.com:10086"
  expect_eq "${kind} HTTP/1 SELF_WORKER_FQDN count" \
    "$(workload_env "${tmp_dir}/http1-${type}.yaml" "${kind}" SELF_WORKER_FQDN | wc -l | tr -d ' ')" "1"
  expect_eq "${kind} ConfigMap SELF_WORKER_FQDN" \
    "$(yq -r 'select(.kind == "ConfigMap" and (.metadata.name | test("-env$"))) | .data.SELF_WORKER_FQDN // ""' "${tmp_dir}/http1-${type}.yaml")" ""
done

assert_render_fails "grpcproxy.http3.tls.secretName is required" \
  --set grpcproxy.http3.enabled=true
assert_render_fails "grpcproxy.http3.tls.certificate.issuerRef.name is required" \
  --set grpcproxy.http3.enabled=true --set grpcproxy.http3.tls.certificate.enabled=true
assert_render_fails "grpcproxy.env.PUBLIC_CERT_PATH cannot be set" \
  --set grpcproxy.http3.enabled=true --set grpcproxy.http3.tls.secretName=x \
  --set grpcproxy.env.PUBLIC_CERT_PATH=/tmp/cert.pem

echo "grpc-proxy HTTP/3 render checks passed"
