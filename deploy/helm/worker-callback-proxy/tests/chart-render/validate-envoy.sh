#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Loads the rendered Envoy config into the chart's Envoy image, then checks
# that a rotated grpc-proxy CA Secret is picked up without a restart, that
# invocation tunnels are routed and idle-closed, and that an invocation
# callback runs TLS to Envoy and TLS to the pod through the tunnel.
# It also checks POST and streamed callbacks through Envoy's own TLS session to
# the pod, and that a pod certificate naming another host is refused.
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
wrong_ca_container="worker-callback-proxy-wrongca-${suffix}"
bad_upstream="worker-callback-proxy-bad-upstream-${suffix}"
bad_envoy="worker-callback-proxy-bad-envoy-${suffix}"
nc_envoy="worker-callback-proxy-noconnect-${suffix}"
tls_upstream="worker-callback-proxy-tls-upstream-${suffix}"
network="worker-callback-proxy-${suffix}"
ca_volume="worker-callback-proxy-ca-${suffix}"

cleanup() {
  docker rm -f "${container}" "${idle_container}" "${upstream}" "${tls_container}" "${tls_upstream}" "${plain_container}" "${wrong_ca_container}" "${bad_upstream}" "${bad_envoy}" "${nc_envoy}" >/dev/null 2>&1 || true
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
mkdir -p "${work_dir}/bad" "${work_dir}/gen/templates" "${work_dir}/frontend" "${work_dir}/ca" "${work_dir}/invocation"
printf 'apiVersion: v2\nname: gen\nversion: 0.1.0\n' >"${work_dir}/gen/Chart.yaml"
cat >"${work_dir}/gen/templates/certs.yaml" <<'EOF'
{{- $ca := genCA "validate-ca-one" 1 }}
{{- $ca2 := genCA "validate-ca-two" 1 }}
{{- $frontend := genSignedCert "frontend" nil (list "*.grpc-worker.example.com") 1 $ca }}
{{- $invocation := genSignedCert "invocation" nil (list "*.invocation.nvcf.svc.cluster.local") 1 $ca }}
{{- $bad := genSignedCert "bad" nil (list "*.other.example.com") 1 $ca }}
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
  bad_crt: {{ $bad.Cert | quote }}
  bad_key: {{ $bad.Key | quote }}
EOF
helm template gen "${work_dir}/gen" >"${work_dir}/certs.yaml"
yq -r .data.ca_one "${work_dir}/certs.yaml" >"${work_dir}/ca/ca_one.crt"
yq -r .data.ca_two "${work_dir}/certs.yaml" >"${work_dir}/ca/ca_two.crt"
yq -r .data.tls_crt "${work_dir}/certs.yaml" >"${work_dir}/frontend/tls.crt"
yq -r .data.tls_key "${work_dir}/certs.yaml" >"${work_dir}/frontend/tls.key"
yq -r .data.invocation_crt "${work_dir}/certs.yaml" >"${work_dir}/invocation/tls.crt"
yq -r .data.invocation_key "${work_dir}/certs.yaml" >"${work_dir}/invocation/tls.key"
yq -r .data.bad_crt "${work_dir}/certs.yaml" >"${work_dir}/bad/tls.crt"
yq -r .data.bad_key "${work_dir}/certs.yaml" >"${work_dir}/bad/tls.key"
chmod -R a+rX "${work_dir}/bad"
# The CA bundles Envoy trusts for the invocation-service pod: the CA that signed
# the pod certificate, and a different CA for the rejection check.
mkdir -p "${work_dir}/invocation-ca" "${work_dir}/invocation-ca-wrong"
cp "${work_dir}/ca/ca_one.crt" "${work_dir}/invocation-ca/ca.crt"
cp "${work_dir}/ca/ca_two.crt" "${work_dir}/invocation-ca-wrong/ca.crt"
chmod -R a+rX "${work_dir}/frontend" "${work_dir}/ca" "${work_dir}/invocation" "${work_dir}/invocation-ca" "${work_dir}/invocation-ca-wrong"

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
    -v "${INVOCATION_CA_DIR:-${work_dir}/invocation-ca}:/etc/envoy/tls/invocation-ca:ro" \
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

# Serials of every CA Envoy has loaded, one per line. The grpc-proxy and
# invocation CAs are both loaded, and /certs does not say which is which, so the
# rotation check below looks for the rotated serial among them.
ca_serials() {
  admin_get /certs | jq -r '[.certificates[].ca_cert[]?.serial_number] | unique | .[]' | sed 's/^0*//'
}

has_ca() {
  ca_serials | grep -qx "$1"
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
has_ca "${expect_one}" || fail "Envoy did not load the first CA"

set_ca ca_two.crt
for _ in $(seq 1 30); do
  has_ca "${expect_two}" && break
  sleep 1
done
has_ca "${expect_two}" || fail "Envoy did not reload the rotated CA"
[[ "$(docker inspect -f '{{.RestartCount}}' "${container}")" == "0" ]] || fail "Envoy restarted"
echo "ok: rotated grpc-proxy CA applied without a restart"

# Routing and idle checks speak raw HTTP/1 to the listener, so they run on a
# plaintext listener with a plaintext pod port; TLS is checked last.
extract_config plaintext --set workerCallbackProxy.invocation.tls.enabled=false \
  --set workerCallbackProxy.invocation.upstream.tls.enabled=false --set workerCallbackProxy.invocation.port=8080
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
[[ "$(invocation_status "http://${pod_host}/v2/nvcf/worker/request-attach" "${pod_host}")" == "503" ]] ||
  fail "absolute-form request to an allowed pod did not reach the route"
[[ "$(invocation_status "http://${pod_host}/v2/nvcf/pexec/functions/x" "${pod_host}")" == "404" ]] ||
  fail "a request to a path other than the worker callback path was routed to the pod"
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
  --set workerCallbackProxy.invocation.tls.enabled=false --set workerCallbackProxy.invocation.upstream.tls.enabled=false \
  --set workerCallbackProxy.invocation.port=8080
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
import http.server, ssl, time
class H(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    def do_GET(self):
        if "mode=stream" in self.path:
            self.send_response(200)
            self.send_header("Transfer-Encoding", "chunked")
            self.end_headers()
            self.wfile.write(b"5\r\nfirst\r\n"); self.wfile.flush()
            time.sleep(6)
            self.wfile.write(b"6\r\nsecond\r\n0\r\n\r\n"); self.wfile.flush()
            return
        self.send_response(200); self.send_header("Content-Length", "4"); self.end_headers(); self.wfile.write(b"pong")
    def do_POST(self):
        n = int(self.headers.get("Content-Length", "0"))
        body = self.rfile.read(n)
        out = ("len=%d" % len(body)).encode()
        self.send_response(200); self.send_header("Content-Length", str(len(out))); self.end_headers(); self.wfile.write(out)
    def log_message(self, *a):
        pass
server = http.server.ThreadingHTTPServer(("0.0.0.0", 8443), H)
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

# An invocation callback from a worker that cannot trust the stack CA: TLS to Envoy, which ends it
# and opens its own TLS to the pod. The worker sends an absolute-form http://
# request (no CONNECT) and never sees the pod certificate, so it needs no CA
# for the pod. Envoy verifies the pod with the invocation CA.
fwd_out="$(worker_get "https://${proxy_host}:10084" "http://${tls_pod}:8443/v2/nvcf/worker/request-attach" || true)"
[[ "${fwd_out}" == "pong" ]] ||
  fail "a forward-proxy callback did not reach the TLS pod through Envoy's own TLS session: ${fwd_out}"
echo "ok: forward-proxy callbacks run TLS to Envoy and a separate verified TLS to the pod"

# The worker's trust store never contains the pod CA: with only the frontend CA
# available the request still works, because the pod certificate is Envoy's
# concern. A pod certificate from another CA must be refused.
INVOCATION_CA_DIR="${work_dir}/invocation-ca-wrong" run_envoy "${work_dir}/default" \
  "-d --name ${wrong_ca_container} --network ${network} --add-host ${tls_pod}:${tls_upstream_ip}" >/dev/null
for _ in $(seq 1 30); do
  docker exec "${wrong_ca_container}" bash -c 'exec 3<>/dev/tcp/127.0.0.1/8081' 2>/dev/null && break
  sleep 1
done
wrong_ip="$(docker inspect -f "{{(index .NetworkSettings.Networks \"${network}\").IPAddress}}" "${wrong_ca_container}")"
wrong_out="$(docker run --rm --network "${network}" --add-host "${proxy_host}:${wrong_ip}" \
  -v "${work_dir}/ca:/ca:ro" "${CURL_IMAGE}" -sS --max-time 10 \
  --proxy "https://${proxy_host}:10084" --proxy-cacert /ca/ca_one.crt "http://${tls_pod}:8443/v2/nvcf/worker/request-attach" 2>&1 || true)"
[[ "${wrong_out}" != *pong* ]] ||
  fail "Envoy accepted a pod certificate that the invocation CA did not sign"
echo "ok: Envoy refuses a pod certificate from another CA"

# POST and streamed callbacks use the same TLS session to the pod as a GET, and
# the pod certificate must match the pod name as well as chain to the CA.
head -c 1048576 /dev/zero | tr '\0' a >"${work_dir}/body.txt"
chmod a+r "${work_dir}/body.txt"
post_out="$(docker run --rm --network "${network}" --add-host "${proxy_host}:${envoy_ip}" \
  -v "${work_dir}/ca:/ca:ro" -v "${work_dir}/body.txt:/body.txt:ro" "${CURL_IMAGE}" -sS --max-time 20 \
  --proxy "https://${proxy_host}:10084" --proxy-cacert /ca/ca_one.crt -X POST --data-binary @/body.txt "http://${tls_pod}:8443/v2/nvcf/worker/request-attach" 2>&1 || true)"
[[ "${post_out}" == "len=1048576" ]] || fail "a 1 MiB POST callback did not reach the TLS pod intact: ${post_out}"
echo "ok: POST callbacks (1 MiB body) run through Envoy's own TLS session"

# The pod sends one chunk, waits six seconds, then sends another. A client that
# reads for three seconds sees only the first if Envoy forwards chunks as they arrive.
stream_out="$(docker run --rm --network "${network}" --add-host "${proxy_host}:${envoy_ip}" \
  -v "${work_dir}/ca:/ca:ro" "${CURL_IMAGE}" -sN --max-time 3 \
  --proxy "https://${proxy_host}:10084" --proxy-cacert /ca/ca_one.crt "http://${tls_pod}:8443/v2/nvcf/worker/request-attach?mode=stream" 2>&1 || true)"
[[ "${stream_out}" == *first* && "${stream_out}" != *second* ]] ||
  fail "a streamed response was buffered instead of forwarded as it arrived: ${stream_out}"
echo "ok: streamed responses are forwarded chunk by chunk"

docker run -d --name "${bad_upstream}" --network "${network}" -v "${work_dir}/bad:/tls:ro" "${PYTHON_IMAGE}" \
  python -c '
import http.server, ssl
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200); self.send_header("Content-Length", "4"); self.end_headers(); self.wfile.write(b"pong")
    def log_message(self, *a):
        pass
server = http.server.HTTPServer(("0.0.0.0", 8443), H)
ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
ctx.load_cert_chain("/tls/tls.crt", "/tls/tls.key")
server.socket = ctx.wrap_socket(server.socket, server_side=True)
server.serve_forever()' >/dev/null
bad_ip="$(docker inspect -f "{{(index .NetworkSettings.Networks \"${network}\").IPAddress}}" "${bad_upstream}")"
bad_pod="10-0-0-9.invocation.nvcf.svc.cluster.local"
run_envoy "${work_dir}/default" \
  "-d --name ${bad_envoy} --network ${network} --add-host ${bad_pod}:${bad_ip}" >/dev/null
for _ in $(seq 1 30); do
  docker exec "${bad_envoy}" bash -c 'exec 3<>/dev/tcp/127.0.0.1/8081' 2>/dev/null && break
  sleep 1
done
bad_envoy_ip="$(docker inspect -f "{{(index .NetworkSettings.Networks \"${network}\").IPAddress}}" "${bad_envoy}")"
bad_out="$(docker run --rm --network "${network}" --add-host "${proxy_host}:${bad_envoy_ip}" \
  -v "${work_dir}/ca:/ca:ro" "${CURL_IMAGE}" -sS --max-time 10 \
  --proxy "https://${proxy_host}:10084" --proxy-cacert /ca/ca_one.crt "http://${bad_pod}:8443/v2/nvcf/worker/request-attach" 2>&1 || true)"
[[ "${bad_out}" != *pong* ]] || fail "Envoy accepted a pod certificate that the right CA signed but that names another host"
echo "ok: Envoy refuses a pod certificate from the right CA that does not match the pod name"

# Only the worker callback path reaches the pod: its worker port also serves the
# rest of the invocation API.
other_code="$(docker run --rm --network "${network}" --add-host "${proxy_host}:${envoy_ip}" \
  -v "${work_dir}/ca:/ca:ro" "${CURL_IMAGE}" -s -o /dev/null -w '%{http_code}' --max-time 10 \
  --proxy "https://${proxy_host}:10084" --proxy-cacert /ca/ca_one.crt "http://${tls_pod}:8443/v2/nvcf/pexec/functions/x" 2>&1 || true)"
[[ "${other_code}" == "404" ]] || fail "a request to another invocation API path was not refused: ${other_code}"
echo "ok: only the worker callback path is routed to the pod"

# With CONNECT off the forward-proxy callback still works and a CONNECT tunnel
# is refused.
extract_config no-connect --set workerCallbackProxy.invocation.connect.enabled=false
run_envoy "${work_dir}/no-connect" --rm --mode validate >"${work_dir}/no-connect.log" 2>&1 ||
  fail "Envoy rejected the config with CONNECT off: $(tail -5 "${work_dir}/no-connect.log")"
run_envoy "${work_dir}/no-connect" \
  "-d --name ${nc_envoy} --network ${network} --add-host ${tls_pod}:${tls_upstream_ip}" >/dev/null
for _ in $(seq 1 30); do
  docker exec "${nc_envoy}" bash -c 'exec 3<>/dev/tcp/127.0.0.1/8081' 2>/dev/null && break
  sleep 1
done
nc_ip="$(docker inspect -f "{{(index .NetworkSettings.Networks \"${network}\").IPAddress}}" "${nc_envoy}")"
nc_get() {
  docker run --rm --network "${network}" --add-host "${proxy_host}:${nc_ip}" \
    -v "${work_dir}/ca:/ca:ro" "${CURL_IMAGE}" -sS --max-time 10 \
    --proxy "https://${proxy_host}:10084" --proxy-cacert /ca/ca_one.crt --cacert /ca/ca_one.crt "$1" 2>&1
}
[[ "$(nc_get "http://${tls_pod}:8443/v2/nvcf/worker/request-attach" || true)" == "pong" ]] ||
  fail "the forward-proxy callback failed with CONNECT off"
connect_out="$(nc_get "https://${tls_pod}:8443/ping" || true)"
[[ "${connect_out}" != *pong* ]] || fail "a CONNECT tunnel was accepted with invocation.connect.enabled false"
echo "ok: with CONNECT off, forward-proxy callbacks work and CONNECT tunnels are refused"
