#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Checks that the rendered objects agree with each other: Services target real
# container ports, selectors match the pod, Envoy listens on the ports the pod
# declares, every file Envoy reads is mounted, and the mounted Secrets are the
# ones the chart issues.
#
# Usage:
#   bash tests/chart-render/verify-render.sh

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CHART="${ROOT_DIR}/worker-callback-proxy"
VALUES="${ROOT_DIR}/../../../tools/ci/helm-validate-values/worker-callback-proxy.yaml"
tmp_dir="$(mktemp -d)"
trap 'rm -rf "${tmp_dir}"' EXIT

render() {
  helm template worker-callback-proxy "${CHART}" --namespace nvcf -f "${VALUES}" "$@"
}

fail() {
  echo "FAILED: $*" >&2
  exit 1
}

expect_eq() {
  [[ "$2" == "$3" ]] || fail "$1: expected '$3', got '$2'"
}

assert_render_fails() {
  local expected="$1" err
  shift
  if err="$(render "$@" 2>&1)"; then
    fail "expected render to fail with: ${expected}"
  fi
  grep -F -q -- "${expected}" <<<"${err}" || fail "expected error '${expected}', got: ${err}"
}

# q MANIFEST EXPR runs a yq expression over every document.
q() {
  yq -N -r "$2" "$1" | { grep -v '^$' || true; }
}

# envoy MANIFEST EXPR runs a yq expression over the rendered envoy.yaml.
envoy() {
  q "$1" 'select(.kind == "ConfigMap") | .data["envoy.yaml"]' | yq -r "$2"
}

pod() {
  q "$1" "select(.kind == \"Deployment\") | .spec.template.spec | $2"
}

# Every port a Service targets is a named container port with the same
# protocol, and every selector matches the pod template labels.
check_wiring() {
  local m="$1" svc port target protocol
  local pod_labels
  pod_labels="$(q "${m}" 'select(.kind == "Deployment") | .spec.template.metadata.labels | to_entries | .[] | .key + "=" + .value' | sort)"

  while IFS=$'\t' read -r svc port target protocol; do
    [[ -n "${svc}" ]] || continue
    expect_eq "Service ${svc} port ${port} protocol" \
      "$(pod "${m}" ".containers[0].ports[] | select(.name == \"${target}\") | .protocol")" "${protocol}"
  done < <(q "${m}" 'select(.kind == "Service") | .metadata.name as $n | .spec.ports[] | [$n, .port, .targetPort, .protocol] | @tsv')

  local kind selector sel
  for kind in Service:.spec.selector PodDisruptionBudget:.spec.selector.matchLabels; do
    selector="${kind#*:}"
    kind="${kind%%:*}"
    while read -r sel; do
      [[ -n "${sel}" ]] || continue
      grep -qxF "${sel}" <<<"${pod_labels}" || fail "${kind} selector ${sel} does not match the pod labels"
    done < <(q "${m}" "select(.kind == \"${kind}\") | ${selector} | to_entries | .[] | .key + \"=\" + .value")
  done

  # Envoy listens on exactly the ports the container declares.
  local listeners container
  listeners="$(envoy "${m}" '.static_resources.listeners[].address.socket_address | (.protocol // "TCP") + "/" + (.port_value | tostring)' | sort)"
  container="$(pod "${m}" '.containers[0].ports[] | .protocol + "/" + (.containerPort | tostring)' | sort)"
  expect_eq "Envoy listener ports vs container ports" "${listeners}" "${container}"

  # The admin API is pod-local and not a container port.
  expect_eq "admin address" "$(envoy "${m}" '.admin.address.socket_address.address')" "127.0.0.1"
  local admin_port
  admin_port="$(envoy "${m}" '.admin.address.socket_address.port_value')"
  grep -qx "TCP/${admin_port}" <<<"${container}" && fail "admin port ${admin_port} is a container port"
  pod "${m}" '.containers[0].lifecycle.preStop.exec.command[2]' | grep -qF "/dev/tcp/127.0.0.1/${admin_port}" ||
    fail "preStop does not drain through the admin port ${admin_port}"

  # Probes hit the health listener, which only forwards /ready to the admin API.
  local health_port
  health_port="$(pod "${m}" '.containers[0].ports[] | select(.name == "health") | .containerPort')"
  expect_eq "readiness probe port" "$(pod "${m}" '.containers[0].readinessProbe.httpGet.port')" "health"
  expect_eq "health listener route" \
    "$(envoy "${m}" ".static_resources.listeners[] | select(.address.socket_address.port_value == ${health_port}) | .filter_chains[0].filters[0].typed_config.route_config.virtual_hosts[0].routes[0] | .match.path + \">\" + .route.cluster")" \
    "/ready>admin"

  # Every file Envoy reads through filesystem SDS is a key of the mounted
  # ConfigMap, and every file an SDS resource names is under a mounted volume.
  local config_mount sds_path key
  config_mount="$(pod "${m}" '.containers[0].volumeMounts[] | select(.name == "config") | .mountPath')"
  expect_eq "config volume ConfigMap" "$(pod "${m}" '.volumes[] | select(.name == "config") | .configMap.name')" \
    "$(q "${m}" 'select(.kind == "ConfigMap") | .metadata.name')"
  while read -r sds_path; do
    [[ -n "${sds_path}" ]] || continue
    [[ "${sds_path}" == "${config_mount}/"* ]] || fail "SDS path ${sds_path} is outside the config mount ${config_mount}"
    key="${sds_path#"${config_mount}/"}"
    [[ "$(q "${m}" "select(.kind == \"ConfigMap\") | .data | has(\"${key}\")")" == "true" ]] ||
      fail "SDS file ${key} is not in the ConfigMap"
    q "${m}" "select(.kind == \"ConfigMap\") | .data[\"${key}\"]" |
      yq -r '.. | select(has("filename")) | .filename' | while read -r file; do
        local volume
        volume="$(pod "${m}" ".containers[0].volumeMounts[] | select(.mountPath as \$p | \"${file}\" | test(\"^\" + \$p + \"/\")) | .name")"
        [[ -n "${volume}" ]] || fail "${key} reads ${file}, which is not under any mount"
        local item="${file##*/}"
        expect_eq "${file} is projected by volume ${volume}" \
          "$(pod "${m}" ".volumes[] | select(.name == \"${volume}\") | .secret.items[] | select(.path == \"${item}\") | .path")" "${item}"
      done
  done < <(envoy "${m}" '.. | select(has("path_config_source")) | .path_config_source.path' | sort -u)
}

# --- Default render: both tunnels, cert-manager frontend certificate.
m="${tmp_dir}/default.yaml"
render > "${m}"
check_wiring "${m}"
expect_eq "kinds" "$(q "${m}" '.kind' | sort | tr '\n' ' ')" \
  "Certificate ConfigMap Deployment PodDisruptionBudget Service Service "
expect_eq "frontend Secret is the issued one" \
  "$(pod "${m}" '.volumes[] | select(.name == "frontend-tls") | .secret.secretName')" \
  "$(q "${m}" 'select(.kind == "Certificate") | .spec.secretName')"
expect_eq "Certificate names" "$(q "${m}" 'select(.kind == "Certificate") | .spec.dnsNames | join(",")')" \
  "*.grpc-worker.example.com"
expect_eq "Certificate issuer" "$(q "${m}" 'select(.kind == "Certificate") | .spec.issuerRef.name')" "nvcf-worker-callback"
envoy "${m}" '.. | select(has("inline_string")) | .inline_string' | grep -qF 'domain ~= "grpc-worker.example.com"' ||
  fail "Lua filter does not match workerDomain"
expect_eq "grpc-proxy CA Secret" "$(pod "${m}" '.volumes[] | select(.name == "grpc-proxy-ca") | .secret.secretName')" \
  "grpc-proxy-worker-tls"
upstream="$(envoy "${m}" '.static_resources.clusters[] | select(.name == "grpc_proxy_pods") | .transport_socket.typed_config.upstream_tls_context')"
expect_eq "upstream SNI equals the accepted SAN" "$(yq -r '.sni' <<<"${upstream}")" \
  "$(yq -r '.common_tls_context.combined_validation_context.default_validation_context.match_typed_subject_alt_names[0].matcher.exact' <<<"${upstream}")"
expect_eq "upstream CA comes from SDS" \
  "$(yq -r '.common_tls_context.combined_validation_context.validation_context_sds_secret_config.name' <<<"${upstream}")" "grpc_proxy_ca"
expect_eq "CA SDS watches its mount" \
  "$(q "${m}" 'select(.kind == "ConfigMap") | .data["sds_grpc_proxy_ca.yaml"]' | yq -r '.resources[0].validation_context.watched_directory.path')" \
  "$(pod "${m}" '.containers[0].volumeMounts[] | select(.name == "grpc-proxy-ca") | .mountPath')"
expect_eq "invocation route" \
  "$(envoy "${m}" '.. | select(has("safe_regex")) | .safe_regex.regex' | sort -u)" \
  '^\d+-\d+-\d+-\d+\.invocation\.nvcf\.svc\.cluster\.local:8443$'
# Both invocation TLS layers: the worker's TLS to Envoy ends on the frontend
# certificate, and the tunnel only reaches the pod's TLS port.
invocation_tls() {
  envoy "$1" '.static_resources.listeners[] | select(.name == "invocation_worker_tcp") | .filter_chains[0].transport_socket.typed_config.common_tls_context.tls_certificate_sds_secret_configs[0].name // "none"'
}
expect_eq "invocation listener TLS" "$(invocation_tls "${m}")" "frontend_tls"
expect_eq "drain time" "$(pod "${m}" '.containers[0].args | .[(to_entries[] | select(.value == "--drain-time-s") | .key) + 1]')" "30"
expect_eq "access log format" "$(envoy "${m}" '[.. | select(has("json_format")) | .json_format.x_request_id] | unique | .[]')" \
  "%REQ(X-REQUEST-ID)%"
expect_eq "no affinity by default" "$(pod "${m}" 'has("affinity")')" "false"
expect_eq "no spread by default" "$(pod "${m}" 'has("topologySpreadConstraints")')" "false"
expect_eq "no tracing by default" "$(envoy "${m}" '[.. | select(has("tracing"))] | length')" "0"
# Without an explicit value Envoy closes quiet streams after 5m.
stream_idle() {
  envoy "$1" "[.static_resources.listeners[] | {\"n\": .name, \"t\": .filter_chains[0].filters[0].typed_config.stream_idle_timeout} | select(.t) | .n + \"=\" + .t] | join(\",\")"
}
expect_eq "stream idle defaults" "$(stream_idle "${m}")" "grpc_worker_quic=1800s,invocation_worker_tcp=3605s"
render --set-string workerCallbackProxy.timeouts.grpcStreamIdle=0s \
  --set-string workerCallbackProxy.timeouts.invocationStreamIdle=7200s > "${tmp_dir}/stream-idle.yaml"
expect_eq "stream idle overrides" "$(stream_idle "${tmp_dir}/stream-idle.yaml")" "grpc_worker_quic=0s,invocation_worker_tcp=7200s"

# The config checksum follows the ConfigMap, so a config change rolls the pods.
checksum() { pod "$1" '.' >/dev/null; q "$1" 'select(.kind == "Deployment") | .spec.template.metadata.annotations["checksum/config"]'; }
render > "${tmp_dir}/again.yaml"
expect_eq "checksum is stable" "$(checksum "${tmp_dir}/again.yaml")" "$(checksum "${m}")"
render --set workerCallbackProxy.timeouts.connect=3s > "${tmp_dir}/changed.yaml"
[[ "$(checksum "${tmp_dir}/changed.yaml")" != "$(checksum "${m}")" ]] || fail "checksum did not change with the Envoy config"

# --- Existing frontend Secret: no Certificate, the Secret is mounted.
m="${tmp_dir}/existing.yaml"
render --set-string workerCallbackProxy.tls.frontend.existingSecret=worker-tls > "${m}"
check_wiring "${m}"
expect_eq "no Certificate" "$(q "${m}" 'select(.kind == "Certificate") | .kind')" ""
expect_eq "existing frontend Secret" "$(pod "${m}" '.volumes[] | select(.name == "frontend-tls") | .secret.secretName')" "worker-tls"

# --- Numeric-looking names still render as strings.
m="${tmp_dir}/numeric-names.yaml"
render --set-string workerCallbackProxy.tls.frontend.existingSecret=123 \
  --set-string workerCallbackProxy.grpcProxy.caSecret.name=456 \
  --set-string workerCallbackProxy.grpcProxy.caSecret.key=789 > "${m}"
expect_eq "Secret names and key are strings" \
  "$(pod "${m}" '[.volumes[] | select(.secret) | (.secret.secretName, .secret.items[0].key) | tag] | unique | join(",")')" "!!str"
m="${tmp_dir}/numeric-release.yaml"
helm template 1234 "${CHART}" --namespace 5678 -f "${VALUES}" \
  --set workerCallbackProxy.service.type=LoadBalancer \
  --set-string workerCallbackProxy.service.loadBalancerClass=90 > "${m}"
expect_eq "labels, namespaces and Service fields are strings" \
  "$(q "${m}" '[(.metadata.labels[], .metadata.namespace, .spec.selector.matchLabels[]?, .spec.loadBalancerClass?) | select(. != null) | tag] | unique | join(",")' | sort -u | paste -sd, -)" "!!str"

# --- Single tunnels.
m="${tmp_dir}/invocation-only.yaml"
render --set workerCallbackProxy.grpcProxy.enabled=false > "${m}"
check_wiring "${m}"
expect_eq "invocation-only keeps the frontend Certificate" "$(q "${m}" 'select(.kind == "Certificate") | .kind')" "Certificate"
expect_eq "invocation-only mounts only the frontend Secret" "$(pod "${m}" '[.volumes[].name] | join(",")')" "config,frontend-tls"
expect_eq "invocation-only listener TLS" "$(invocation_tls "${m}")" "frontend_tls"
m="${tmp_dir}/invocation-plaintext.yaml"
render --set workerCallbackProxy.grpcProxy.enabled=false --set workerCallbackProxy.invocation.tls.enabled=false \
  --set workerCallbackProxy.invocation.port=8080 > "${m}"
check_wiring "${m}"
expect_eq "no Certificate without a TLS listener" "$(q "${m}" 'select(.kind == "Certificate") | .kind')" ""
expect_eq "no TLS volumes without a TLS listener" "$(pod "${m}" '[.volumes[].name] | join(",")')" "config"
expect_eq "plaintext invocation listener" "$(invocation_tls "${m}")" "none"
expect_eq "plaintext invocation route" \
  "$(envoy "${m}" '.. | select(has("safe_regex")) | .safe_regex.regex' | sort -u)" \
  '^\d+-\d+-\d+-\d+\.invocation\.nvcf\.svc\.cluster\.local:8080$'
m="${tmp_dir}/grpc-only.yaml"
render --set workerCallbackProxy.invocation.enabled=false > "${m}"
check_wiring "${m}"
expect_eq "no invocation listener" "$(envoy "${m}" '[.static_resources.listeners[] | select(.name == "invocation_worker_tcp")] | length')" "0"

# --- Supplied placement renders as given.
m="${tmp_dir}/custom-placement.yaml"
render --set 'workerCallbackProxy.topologySpreadConstraints[0].maxSkew=2' \
  --set 'workerCallbackProxy.topologySpreadConstraints[0].topologyKey=kubernetes.io/hostname' \
  --set 'workerCallbackProxy.topologySpreadConstraints[0].whenUnsatisfiable=DoNotSchedule' \
  --set 'workerCallbackProxy.affinity.podAntiAffinity.preferredDuringSchedulingIgnoredDuringExecution[0].weight=100' \
  --set 'workerCallbackProxy.affinity.podAntiAffinity.preferredDuringSchedulingIgnoredDuringExecution[0].podAffinityTerm.topologyKey=kubernetes.io/hostname' > "${m}"
expect_eq "custom spread" "$(pod "${m}" '.topologySpreadConstraints | length')" "1"
expect_eq "custom spread key" "$(pod "${m}" '.topologySpreadConstraints[0].topologyKey')" "kubernetes.io/hostname"
expect_eq "custom affinity" \
  "$(pod "${m}" '.affinity.podAntiAffinity.preferredDuringSchedulingIgnoredDuringExecution[0].podAffinityTerm.topologyKey')" \
  "kubernetes.io/hostname"
for removed in defaultPodAntiAffinity defaultZoneSpread; do
  assert_render_fails "${removed}" --set "workerCallbackProxy.${removed}=false"
done

# --- Single replica: no PDB.
m="${tmp_dir}/one.yaml"
render --set workerCallbackProxy.replicaCount=1 > "${m}"
expect_eq "no PDB at one replica" "$(q "${m}" 'select(.kind == "PodDisruptionBudget") | .kind')" ""

# --- Metrics and tracing.
m="${tmp_dir}/observability.yaml"
render --set workerCallbackProxy.metrics.serviceMonitor.enabled=true \
  --set workerCallbackProxy.tracing.endpoint=otel-collector.observability.svc.cluster.local:4317 > "${m}"
check_wiring "${m}"
expect_eq "ServiceMonitor selects the metrics Service" \
  "$(q "${m}" 'select(.kind == "ServiceMonitor") | .spec.selector.matchLabels["app.kubernetes.io/component"]')" \
  "$(q "${m}" 'select(.kind == "Service" and .metadata.name == "worker-callback-proxy-metrics") | .metadata.labels["app.kubernetes.io/component"]')"
expect_eq "ServiceMonitor port exists on the metrics Service" \
  "$(q "${m}" 'select(.kind == "Service" and .metadata.name == "worker-callback-proxy-metrics") | .spec.ports[] | select(.name == "metrics") | .name')" \
  "$(q "${m}" 'select(.kind == "ServiceMonitor") | .spec.endpoints[0].port')"
expect_eq "worker Service does not expose metrics" \
  "$(q "${m}" 'select(.kind == "Service" and .metadata.name == "worker-callback-proxy") | .spec.ports[] | select(.name == "metrics") | .name')" ""
expect_eq "tracing on both listeners" "$(envoy "${m}" '[.. | select(has("tracing"))] | length')" "2"
expect_eq "collector address" \
  "$(envoy "${m}" '.static_resources.clusters[] | select(.name == "opentelemetry_collector") | .load_assignment.endpoints[0].lb_endpoints[0].endpoint.address.socket_address | .address + ":" + (.port_value | tostring)')" \
  "otel-collector.observability.svc.cluster.local:4317"

# --- Rejected values.
assert_render_fails "workerCallbackProxy.workerDomain is required" --set-string workerCallbackProxy.workerDomain=
assert_render_fails "must be a lowercase DNS name" --set-string workerCallbackProxy.workerDomain=Worker
assert_render_fails "enable grpcProxy, invocation, or both" \
  --set workerCallbackProxy.grpcProxy.enabled=false --set workerCallbackProxy.invocation.enabled=false
assert_render_fails "need drainTimeSeconds (40) <= preStopSleepSeconds (30)" --set workerCallbackProxy.drainTimeSeconds=40
assert_render_fails "preStopSleepSeconds (60) < terminationGracePeriodSeconds (60)" --set workerCallbackProxy.preStopSleepSeconds=60
assert_render_fails "loadBalancerClass requires service.type LoadBalancer" \
  --set-string workerCallbackProxy.service.loadBalancerClass=service.k8s.aws/nlb
assert_render_fails "externalTrafficPolicy requires service.type NodePort or LoadBalancer" \
  --set-string workerCallbackProxy.service.externalTrafficPolicy=Local
assert_render_fails "health, metrics, admin and invocationTcp must differ" --set workerCallbackProxy.ports.metrics=8081
assert_render_fails "issuerRef.name is required" --set-string workerCallbackProxy.tls.frontend.certificate.issuerRef.name=
assert_render_fails "serviceMonitor.enabled requires metrics.service.enabled" \
  --set workerCallbackProxy.metrics.serviceMonitor.enabled=true --set workerCallbackProxy.metrics.service.enabled=false
assert_render_fails "additional properties 'admin' not allowed" --set-string workerCallbackProxy.admin.address=0.0.0.0
assert_render_fails "/workerCallbackProxy/tracing/endpoint" --set-string workerCallbackProxy.tracing.endpoint=http://collector
assert_render_fails "/workerCallbackProxy/timeouts/grpcIdle" --set-string workerCallbackProxy.timeouts.grpcIdle=30m
assert_render_fails "/workerCallbackProxy/timeouts/connect" --set-string workerCallbackProxy.timeouts.connect=500ms
assert_render_fails "/workerCallbackProxy/timeouts/invocationStreamIdle" --set-string workerCallbackProxy.timeouts.invocationStreamIdle=1h

echo "ok: worker-callback-proxy render checks passed"
