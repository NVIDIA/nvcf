#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Loads the rendered Envoy config into the chart's Envoy image, then checks
# that a rotated grpc-proxy CA Secret is picked up without a restart.
# Needs Docker.
#
# Usage:
#   bash tests/chart-render/validate-envoy.sh

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CHART="${ROOT_DIR}/worker-callback-proxy"
VALUES="${ROOT_DIR}/../../../tools/ci/helm-validate-values/worker-callback-proxy.yaml"
IMAGE="envoyproxy/envoy:v$(yq -r .appVersion "${CHART}/Chart.yaml")"
work_dir="$(mktemp -d)"
suffix="$$"
container="worker-callback-proxy-validate-${suffix}"
ca_volume="worker-callback-proxy-ca-${suffix}"

cleanup() {
  docker rm -f "${container}" >/dev/null 2>&1 || true
  docker volume rm -f "${ca_volume}" >/dev/null 2>&1 || true
  rm -rf "${work_dir}"
}
trap cleanup EXIT

fail() {
  echo "FAILED: $*" >&2
  exit 1
}

# Throwaway CAs and a frontend certificate, from Helm's own generators.
mkdir -p "${work_dir}/gen/templates" "${work_dir}/frontend" "${work_dir}/ca"
printf 'apiVersion: v2\nname: gen\nversion: 0.1.0\n' >"${work_dir}/gen/Chart.yaml"
cat >"${work_dir}/gen/templates/certs.yaml" <<'EOF'
{{- $ca := genCA "validate-ca-one" 1 }}
{{- $ca2 := genCA "validate-ca-two" 1 }}
{{- $frontend := genSignedCert "frontend" nil (list "*.grpc-worker.example.com") 1 $ca }}
apiVersion: v1
kind: ConfigMap
metadata:
  name: certs
data:
  ca_one: {{ $ca.Cert | quote }}
  ca_two: {{ $ca2.Cert | quote }}
  tls_crt: {{ $frontend.Cert | quote }}
  tls_key: {{ $frontend.Key | quote }}
EOF
helm template gen "${work_dir}/gen" >"${work_dir}/certs.yaml"
yq -r .data.ca_one "${work_dir}/certs.yaml" >"${work_dir}/ca/ca_one.crt"
yq -r .data.ca_two "${work_dir}/certs.yaml" >"${work_dir}/ca/ca_two.crt"
yq -r .data.tls_crt "${work_dir}/certs.yaml" >"${work_dir}/frontend/tls.crt"
yq -r .data.tls_key "${work_dir}/certs.yaml" >"${work_dir}/frontend/tls.key"

# extract_config NAME [helm args...] writes the ConfigMap files to NAME/.
extract_config() {
  local dir="${work_dir}/$1"
  shift
  mkdir -p "${dir}"
  helm template worker-callback-proxy "${CHART}" --namespace nvcf -f "${VALUES}" "$@" >"${dir}/manifest.yaml"
  local key
  for key in $(yq -r 'select(.kind == "ConfigMap") | .data | keys | .[]' "${dir}/manifest.yaml"); do
    yq -r "select(.kind == \"ConfigMap\") | .data[\"${key}\"]" "${dir}/manifest.yaml" >"${dir}/${key}"
  done
}

# Lays out the CA volume like a kubelet Secret mount: ca.crt -> ..data/ca.crt,
# ..data -> a timestamped directory, swapped atomically on update.
set_ca() {
  docker run --rm -v "${ca_volume}:/ca" -v "${work_dir}/ca:/src:ro" busybox sh -ec "
    dir=/ca/..\$(date +%s%N)
    mkdir \"\$dir\" && cp /src/$1 \"\$dir/ca.crt\"
    ln -s \"\${dir##*/}\" /ca/..data_tmp && mv -T /ca/..data_tmp /ca/..data
    [ -e /ca/ca.crt ] || ln -s ..data/ca.crt /ca/ca.crt
    chmod -R a+rX /ca"
}

# run_envoy CONFIG_DIR DOCKER_OPTS [ENVOY_ARGS...]
run_envoy() {
  local dir="$1" docker_opts="$2"
  shift 2
  # shellcheck disable=SC2086
  docker run ${docker_opts} \
    -v "${dir}:/etc/envoy/config:ro" \
    -v "${work_dir}/frontend:/etc/envoy/tls/frontend:ro" \
    -v "${ca_volume}:/etc/envoy/tls/grpc-proxy-ca:ro" \
    "${IMAGE}" -c /etc/envoy/config/envoy.yaml -l warning "$@"
}

docker volume create "${ca_volume}" >/dev/null
set_ca ca_one.crt

extract_config default
extract_config observability --set workerCallbackProxy.tracing.endpoint=otel-collector.observability.svc.cluster.local:4317
for config in default observability; do
  run_envoy "${work_dir}/${config}" --rm --mode validate >"${work_dir}/${config}.log" 2>&1 ||
    fail "Envoy rejected the ${config} config: $(tail -5 "${work_dir}/${config}.log")"
  echo "ok: Envoy accepts the ${config} config"
done

# admin_get PATH prints the body of an admin API response from inside the
# container, without the headers and chunk-size lines.
admin_get() {
  docker exec "${container}" bash -c "exec 3<>/dev/tcp/127.0.0.1/9901
    printf 'GET $1 HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n' >&3
    cat <&3" | awk 'body && !/^[0-9a-fA-F]+\r?$/ { print } /^\r?$/ { body = 1 }'
}

# Serial of the CA the grpc_proxy_pods upstream trusts.
ca_serial() {
  admin_get /certs | jq -r '[.certificates[].ca_cert[]?.serial_number] | first // ""' | sed 's/^0*//'
}

cert_serial() {
  python3 -c 'import ssl, sys; print(ssl._ssl._test_decode_cert(sys.argv[1])["serialNumber"].lower().lstrip("0"))' "$1"
}

run_envoy "${work_dir}/default" "-d --name ${container}" >/dev/null
for _ in $(seq 1 30); do
  admin_get /ready 2>/dev/null | grep -q LIVE && break
  sleep 1
done
expect_one="$(cert_serial "${work_dir}/ca/ca_one.crt")"
expect_two="$(cert_serial "${work_dir}/ca/ca_two.crt")"
[[ "$(ca_serial)" == "${expect_one}" ]] || fail "Envoy did not load the first CA"

set_ca ca_two.crt
for _ in $(seq 1 30); do
  [[ "$(ca_serial)" == "${expect_two}" ]] && break
  sleep 1
done
[[ "$(ca_serial)" == "${expect_two}" ]] || fail "Envoy did not reload the rotated CA"
[[ "$(docker inspect -f '{{.RestartCount}}' "${container}")" == "0" ]] || fail "Envoy restarted"
echo "ok: rotated grpc-proxy CA applied without a restart"

# invocation_status REQUEST_TARGET HOST prints the status code Envoy returns
# for a request on the invocation listener.
invocation_status() {
  docker exec "${container}" bash -c "exec 3<>/dev/tcp/127.0.0.1/10084
    printf 'GET $1 HTTP/1.1\r\nHost: $2\r\nConnection: close\r\n\r\n' >&3
    head -1 <&3" | awk '{ print $2 }'
}

# An allowed absolute-form request reaches the route; the pod name does not
# resolve here, so the dynamic forward proxy answers 503.
pod_host="10-0-0-1.invocation.nvcf.svc.cluster.local:8080"
[[ "$(invocation_status "http://${pod_host}/v1/x" "${pod_host}")" == "503" ]] ||
  fail "absolute-form request to an allowed pod did not reach the route"
[[ "$(invocation_status "http://other.example.com/x" "other.example.com")" == "404" ]] ||
  fail "absolute-form request to another host was not rejected"
echo "ok: absolute-form invocation requests are routed by host"
