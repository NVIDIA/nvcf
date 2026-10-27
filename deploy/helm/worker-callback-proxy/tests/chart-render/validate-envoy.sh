#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Loads the rendered Envoy config into the chart's Envoy image, then checks
# that a rotated grpc-proxy CA Secret is picked up without a restart, that
# invocation tunnels are routed and idle-closed, and that an invocation
# callback runs TLS to Envoy and TLS to the pod through the tunnel.
# Needs Docker.
#
# Usage:
#   bash tests/chart-render/validate-envoy.sh

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CHART="${ROOT_DIR}/worker-callback-proxy"
VALUES="${ROOT_DIR}/../../../tools/ci/helm-validate-values/worker-callback-proxy.yaml"
IMAGE="envoyproxy/envoy:v$(yq -r .appVersion "${CHART}/Chart.yaml")"
PYTHON_IMAGE="python:3.13-slim@sha256:b3575dcf816cdacc5292df77a6e3e59b8941631078373a045efa2ac1e9c5c3b2"
CURL_IMAGE="curlimages/curl:8.17.0@sha256:935d9100e9ba842cdb060de42472c7ca90cfe9a7c96e4dacb55e79e560b3ff40"
work_dir="$(mktemp -d)"
suffix="$$"
container="worker-callback-proxy-validate-${suffix}"
idle_container="worker-callback-proxy-idle-${suffix}"
upstream="worker-callback-proxy-upstream-${suffix}"
tls_container="worker-callback-proxy-tls-${suffix}"
plain_container="worker-callback-proxy-plain-${suffix}"
tls_upstream="worker-callback-proxy-tls-upstream-${suffix}"
network="worker-callback-proxy-${suffix}"
ca_volume="worker-callback-proxy-ca-${suffix}"

cleanup() {
  docker rm -f "${container}" "${idle_container}" "${upstream}" "${tls_container}" "${tls_upstream}" "${plain_container}" >/dev/null 2>&1 || true
  docker network rm "${network}" >/dev/null 2>&1 || true
  docker volume rm -f "${ca_volume}" >/dev/null 2>&1 || true
  rm -rf "${work_dir}"
}
trap cleanup EXIT

fail() {
  echo "FAILED: $*" >&2
  exit 1
}

# Throwaway CAs, a frontend certificate and an invocation-service pod
# certificate, from Helm's own generators.
mkdir -p "${work_dir}/gen/templates" "${work_dir}/frontend" "${work_dir}/ca" "${work_dir}/invocation"
printf 'apiVersion: v2\nname: gen\nversion: 0.1.0\n' >"${work_dir}/gen/Chart.yaml"
cat >"${work_dir}/gen/templates/certs.yaml" <<'EOF'
{{- $ca := genCA "validate-ca-one" 1 }}
{{- $ca2 := genCA "validate-ca-two" 1 }}
{{- $frontend := genSignedCert "frontend" nil (list "*.grpc-worker.example.com") 1 $ca }}
{{- $invocation := genSignedCert "invocation" nil (list "*.invocation.nvcf.svc.cluster.local") 1 $ca }}
apiVersion: v1
kind: ConfigMap
metadata:
  name: certs
data:
  ca_one: {{ $ca.Cert | quote }}
  ca_two: {{ $ca2.Cert | quote }}
  tls_crt: {{ $frontend.Cert | quote }}
  tls_key: {{ $frontend.Key | quote }}
  invocation_crt: {{ $invocation.Cert | quote }}
  invocation_key: {{ $invocation.Key | quote }}
EOF
helm template gen "${work_dir}/gen" >"${work_dir}/certs.yaml"
yq -r .data.ca_one "${work_dir}/certs.yaml" >"${work_dir}/ca/ca_one.crt"
yq -r .data.ca_two "${work_dir}/certs.yaml" >"${work_dir}/ca/ca_two.crt"
yq -r .data.tls_crt "${work_dir}/certs.yaml" >"${work_dir}/frontend/tls.crt"
yq -r .data.tls_key "${work_dir}/certs.yaml" >"${work_dir}/frontend/tls.key"
yq -r .data.invocation_crt "${work_dir}/certs.yaml" >"${work_dir}/invocation/tls.crt"
yq -r .data.invocation_key "${work_dir}/certs.yaml" >"${work_dir}/invocation/tls.key"
chmod -R a+rX "${work_dir}/frontend" "${work_dir}/ca" "${work_dir}/invocation"

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

# Routing and idle checks speak raw HTTP/1 to the listener, so they run on a
# plaintext listener with a plaintext pod port; TLS is checked last.
extract_config plaintext --set workerCallbackProxy.invocation.tls.enabled=false --set workerCallbackProxy.invocation.port=8080
run_envoy "${work_dir}/plaintext" "-d --name ${plain_container}" >/dev/null
for _ in $(seq 1 30); do
  docker exec "${plain_container}" bash -c 'exec 3<>/dev/tcp/127.0.0.1/8081' 2>/dev/null && break
  sleep 1
done

# invocation_status REQUEST_TARGET HOST prints the status code Envoy returns
# for a request on the invocation listener.
invocation_status() {
  docker exec "${plain_container}" bash -c "exec 3<>/dev/tcp/127.0.0.1/10084
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

docker network create "${network}" >/dev/null
# An echo server: one cat per tunnel, so a quiet tunnel stays quiet.
docker run -d --name "${upstream}" --network "${network}" busybox \
  nc -lk -p 8080 -e cat >/dev/null
upstream_ip="$(docker inspect -f "{{(index .NetworkSettings.Networks \"${network}\").IPAddress}}" "${upstream}")"
pod_name="10-0-0-2.invocation.nvcf.svc.cluster.local"
# A short stream idle timeout keeps the check fast; the default is checked in
# verify-render.sh.
extract_config stream-idle --set-string workerCallbackProxy.timeouts.invocationStreamIdle=3s \
  --set workerCallbackProxy.invocation.tls.enabled=false --set workerCallbackProxy.invocation.port=8080
run_envoy "${work_dir}/stream-idle" \
  "-d --name ${idle_container} --network ${network} --add-host ${pod_name}:${upstream_ip}" >/dev/null
for _ in $(seq 1 30); do
  docker exec "${idle_container}" bash -c 'exec 3<>/dev/tcp/127.0.0.1/8081' 2>/dev/null && break
  sleep 1
done

# The stream idle timeout closes a CONNECT tunnel that carries no data, and
# only that one.
#
# connect_tunnel SECONDS SEND opens a CONNECT tunnel and holds it for SECONDS,
# writing a byte every second when SEND is 1. Prints open or closed.
connect_tunnel() {
  docker exec "${idle_container}" bash -c "exec 3<>/dev/tcp/127.0.0.1/10084
    printf 'CONNECT ${pod_name}:8080 HTTP/1.1\r\nHost: ${pod_name}:8080\r\n\r\n' >&3
    read -r _ status _ <&3
    [ \"\$status\" = 200 ] || { echo \"status \$status\"; exit; }
    while read -r line <&3 && [ \${#line} -gt 1 ]; do :; done
    for _ in \$(seq 1 $1); do
      if [ $2 = 1 ]; then printf x >&3 2>/dev/null || { echo closed; exit; }; fi
      read -r -t 1 _ <&3
      [ \$? -gt 128 ] || { echo closed; exit; }
    done
    echo open"
}
[[ "$(connect_tunnel 8 0)" == "closed" ]] ||
  fail "a quiet CONNECT tunnel outlived invocationStreamIdle"
for _ in $(seq 1 10); do
  docker logs "${idle_container}" 2>/dev/null |
    jq -R -e 'fromjson? | select(.method == "CONNECT" and .response_flags == "SI")' >/dev/null && break
  sleep 1
done
docker logs "${idle_container}" 2>/dev/null |
  jq -R -e 'fromjson? | select(.method == "CONNECT" and .response_flags == "SI")' >/dev/null ||
  fail "the quiet CONNECT tunnel was not closed by the stream idle timeout"
[[ "$(connect_tunnel 8 1)" == "open" ]] ||
  fail "a CONNECT tunnel carrying data was closed by invocationStreamIdle"
echo "ok: invocationStreamIdle closes only quiet CONNECT tunnels"

# An invocation callback with both TLS layers, as the worker sends it: TLS to
# Envoy on the frontend certificate, CONNECT, then TLS to the pod's TLS port
# through the tunnel. curl stands in for the worker and a Python HTTPS server
# for the invocation-service pod.
docker run -d --name "${tls_upstream}" --network "${network}" -v "${work_dir}/invocation:/tls:ro" "${PYTHON_IMAGE}" \
  python -c '
import http.server, ssl
class Pong(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200)
        self.send_header("Content-Length", "4")
        self.end_headers()
        self.wfile.write(b"pong")
    def log_message(self, *args):
        pass
server = http.server.HTTPServer(("0.0.0.0", 8443), Pong)
ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
ctx.load_cert_chain("/tls/tls.crt", "/tls/tls.key")
server.socket = ctx.wrap_socket(server.socket, server_side=True)
server.serve_forever()' >/dev/null
tls_upstream_ip="$(docker inspect -f "{{(index .NetworkSettings.Networks \"${network}\").IPAddress}}" "${tls_upstream}")"
tls_pod="10-0-0-3.invocation.nvcf.svc.cluster.local"
run_envoy "${work_dir}/default" \
  "-d --name ${tls_container} --network ${network} --add-host ${tls_pod}:${tls_upstream_ip}" >/dev/null
for _ in $(seq 1 30); do
  docker exec "${tls_container}" bash -c 'exec 3<>/dev/tcp/127.0.0.1/8081' 2>/dev/null && break
  sleep 1
done
envoy_ip="$(docker inspect -f "{{(index .NetworkSettings.Networks \"${network}\").IPAddress}}" "${tls_container}")"
proxy_host="invocation.grpc-worker.example.com"

# worker_get PROXY_URL TARGET_URL runs curl the way the worker calls back.
worker_get() {
  docker run --rm --network "${network}" --add-host "${proxy_host}:${envoy_ip}" \
    -v "${work_dir}/ca:/ca:ro" "${CURL_IMAGE}" -sS --max-time 10 \
    --proxy "$1" --proxy-cacert /ca/ca_one.crt --cacert /ca/ca_one.crt "$2" 2>&1
}
for _ in $(seq 1 10); do
  [[ "$(worker_get "https://${proxy_host}:10084" "https://${tls_pod}:8443/ping")" == "pong" ]] && break
  sleep 1
done
[[ "$(worker_get "https://${proxy_host}:10084" "https://${tls_pod}:8443/ping")" == "pong" ]] ||
  fail "TLS to Envoy and TLS to the pod did not reach the pod: $(worker_get "https://${proxy_host}:10084" "https://${tls_pod}:8443/ping")"
echo "ok: invocation callbacks run TLS to Envoy and TLS to the pod"

plain_out="$(worker_get "http://${proxy_host}:10084" "https://${tls_pod}:8443/ping" || true)"
[[ "$plain_out" != *pong* ]] ||
  fail "the invocation listener accepted a plaintext CONNECT"
port_out="$(worker_get "https://${proxy_host}:10084" "https://${tls_pod}:8080/ping" || true)"
[[ "$port_out" == *"CONNECT tunnel failed, response 404"* ]] ||
  fail "the invocation listener tunnelled to a port other than invocation.port: ${port_out}"
echo "ok: the invocation listener refuses plaintext and other pod ports"
