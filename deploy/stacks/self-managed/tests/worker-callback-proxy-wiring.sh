#!/usr/bin/env bash
# Test that workerCallbackProxy wires the Envoy release, grpc-proxy HTTP/3, and
# invocation-service together, leaves the stack unchanged when off, and
# rejects unsupported combinations.
set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
helm_dir="$(cd "$stack_dir/../../helm" && pwd)"
work_dir="$(mktemp -d)"
test_stack_dir="$work_dir/deploy/stacks/self-managed"
test_helm_dir="$work_dir/deploy/helm"
environment_name="worker-callback-proxy-wiring-test"
environment_file="$test_stack_dir/environments/$environment_name.yaml"
chart_path="../../../helm/worker-callback-proxy/worker-callback-proxy"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "worker-callback-proxy-wiring: $*" >&2
  exit 1
}

mkdir -p "$test_stack_dir"
cp -R "$stack_dir"/. "$test_stack_dir"
mkdir -p "$test_helm_dir"
cp -R "$helm_dir"/worker-callback-proxy "$test_helm_dir/"
printf '{}\n' >"$test_stack_dir/secrets/$environment_name-secrets.yaml"

helmfile_core() {
  (cd "$test_stack_dir" && HELMFILE_ENV="$environment_name" \
    HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" \
    helmfile \
      --file helmfile.d/02-core.yaml.gotmpl \
      --environment default \
      --state-values-set ingress.gatewayApi.controllerNamespace=envoy-gateway-system \
      --state-values-set ingress.gatewayApi.gateways.shared.name=shared-gw \
      --state-values-set ingress.gatewayApi.gateways.shared.namespace=envoy-gateway-system \
      --state-values-set ingress.gatewayApi.gateways.grpc.name=grpc-gw \
      --state-values-set ingress.gatewayApi.gateways.grpc.namespace=envoy-gateway-system \
      "$@")
}

render_values() {
  local release="$1" output_file="$2"
  helmfile_core --selector "name=$release" write-values --output-file-template "$output_file" >/dev/null
}

write_env() {
  cat >"$environment_file"
}

enabled_env() {
  cat <<EOF
highAvailability:
  mode: ${1}
workerCallbackProxy:
  enabled: true
  workerDomain: grpc-worker.example.com
  chartPath: ${chart_path}
EOF
}

assert_render_fails() {
  local label="$1" expected="$2"
  local err="$work_dir/$label.err"
  if render_values grpc-proxy "$work_dir/$label.yaml" 2>"$err"; then
    fail "$label: expected render to fail"
  fi
  grep -Fq -- "$expected" "$err" || fail "$label: expected error '$expected', got: $(tail -3 "$err")"
}

echo "== disabled: no release, services unchanged =="
write_env <<'EOF'
highAvailability:
  mode: preferred
EOF
enabled="$(helmfile_core list --skip-charts --output json | jq -r '.[] | select(.name == "worker-callback-proxy") | .enabled')"
test "$enabled" = "false" || fail "release must be disabled by default, got enabled=${enabled:-missing}"
render_values grpc-proxy "$work_dir/grpc-off.yaml"
test "$(yq -r '.grpcproxy.http3 // ""' "$work_dir/grpc-off.yaml")" = "" || fail "HTTP/3 must stay off without the proxy"
test "$(yq -r '.grpcproxy.workerConnectBaseURL // ""' "$work_dir/grpc-off.yaml")" = "" ||
  fail "grpc-proxy must keep advertising its pod address without the proxy"
test "$(yq -r '.workerCallbackProxy // ""' "$work_dir/grpc-off.yaml")" = "" || fail "workerCallbackProxy values must not render when disabled"
render_values invocation-service "$work_dir/inv-off.yaml"
test "$(yq -r '.invocation.env.WORKER_STREAM_PROPERTIES__PROXY_ADDRESS // ""' "$work_dir/inv-off.yaml")" = "" ||
  fail "invocation must not get a proxy address without the proxy"

echo "== enabled with HA preferred =="
enabled_env preferred | write_env
list="$(helmfile_core list --skip-charts --output json)"
test "$(jq -r '.[] | select(.name == "worker-callback-proxy") | .enabled' <<<"$list")" = "true" || fail "release must be enabled"
test "$(jq -r '.[] | select(.name == "worker-callback-proxy") | .chart' <<<"$list")" = "$chart_path" || fail "release must use chartPath"

render_values grpc-proxy "$work_dir/grpc-on.yaml"
g="$work_dir/grpc-on.yaml"
test "$(yq -r '.grpcproxy.replicaCount' "$g")" = "2" || fail "grpc-proxy: HTTP/3 behind the proxy must still scale under HA"
test "$(yq -r '.grpcproxy.podDisruptionBudget.enabled' "$g")" = "true" || fail "grpc-proxy: expected the HA PDB"
test "$(yq -r '.grpcproxy.http3.enabled' "$g")" = "true" || fail "grpc-proxy: expected HTTP/3 on"
test "$(yq -r '.grpcproxy.http3.tls.certificate.issuerRef.name' "$g")" = "nvcf-openbao-pki" ||
  fail "grpc-proxy: expected the OpenBao service PKI issuer"
test "$(yq -r '.grpcproxy.workerConnectBaseURL' "$g")" = "https://grpc-worker.example.com" ||
  fail "grpc-proxy: expected callback base URL from workerDomain"
test "$(yq -r '.grpcproxy.ports // ""' "$g")" = "" || fail "grpc-proxy: the chart's http3 setting owns the ports"
# `list` drops needs and `build` pulls every chart, so read the needs edge
# from the rendered state that debug logging prints as line-numbered YAML.
needs="$(
  helmfile_core --debug list --skip-charts 2>&1 >/dev/null |
    awk '
      /^rendering result of "[^"]+":$/ { print "---"; capture = 1; next }
      capture && /^ *[0-9]+: / { sub(/^ *[0-9]+: ?/, ""); print; next }
      capture { capture = 0 }
    ' |
    yq ea -r 'select(.releases) | .releases[] | select(.name == "worker-callback-proxy") | .needs[]'
)"
grep -Fxq 'nvcf/grpc-proxy' <<<"$needs" || fail "worker-callback-proxy must need nvcf/grpc-proxy, got: $needs"

grpc_manifest="$work_dir/grpc-manifest.yaml"
helm template grpc-proxy "$helm_dir/grpc-proxy/grpc-proxy" --namespace nvcf --values "$g" >"$grpc_manifest" ||
  fail "grpc-proxy chart must render with stack values"
grpc_cert_secret="$(yq ea -r 'select(.kind == "Certificate") | .spec.secretName' "$grpc_manifest")"
grpc_dns_names="$(yq ea -r 'select(.kind == "Certificate") | .spec.dnsNames | join(",")' "$grpc_manifest")"
test "$grpc_dns_names" = '*.grpc-worker.example.com,*.grpc-headless.nvcf.svc.cluster.local' ||
  fail "grpc-proxy: certificate must cover the worker domain and the name Envoy verifies, got $grpc_dns_names"

render_values invocation-service "$work_dir/inv-on.yaml"
i="$work_dir/inv-on.yaml"
test "$(yq -r '.invocation.replicaCount' "$i")" = "2" || fail "invocation: expected 2 replicas"
test "$(yq -r '.invocation.podDisruptionBudget.enabled' "$i")" = "true" || fail "invocation: expected PDB enabled"
test "$(yq -r '.invocation.env.WORKER_STREAM_PROPERTIES__PROXY_ADDRESS' "$i")" = "https://invocation.grpc-worker.example.com:10084" ||
  fail "invocation: expected the TLS proxy address under the worker domain"
test "$(yq -r '.invocation.env.WORKER_STREAM_PROPERTIES__SELF_ADDRESS // ""' "$i")" = "" || fail "invocation: self address must not be set"
test "$(yq -r '[.invocation.workerTls.enabled, .invocation.workerTls.advertise, .invocation.workerTls.tls.certificate.enabled] | join(",")' "$i")" = "true,true,true" ||
  fail "invocation: expected the worker TLS listener advertised with an issued certificate"
test "$(yq -r '.invocation.workerTls.tls.certificate.issuerRef.name' "$i")" = "nvcf-openbao-pki" ||
  fail "invocation: expected the OpenBao service PKI issuer"

inv_manifest="$work_dir/inv-manifest.yaml"
helm template invocation-service "$helm_dir/http-invocation/nvcf-invocation-service" --namespace nvcf --values "$i" >"$inv_manifest" ||
  fail "invocation-service chart must render with stack values"
yq ea -e 'select(.kind == "Certificate")' "$inv_manifest" >/dev/null 2>&1 ||
  fail "invocation: expected a Certificate for the worker TLS listener"
inv_dns_names="$(yq ea -r 'select(.kind == "Certificate") | .spec.dnsNames | join(",")' "$inv_manifest")"
test "$inv_dns_names" = '*.invocation.nvcf.svc.cluster.local' ||
  fail "invocation: certificate must cover the per-pod names Envoy tunnels to, got $inv_dns_names"
yq ea -e 'select(.kind == "Service") | .spec.ports[] | select(.port == 8443)' "$inv_manifest" >/dev/null 2>&1 ||
  fail "invocation: expected the Service to expose the worker TLS port 8443"

render_values worker-callback-proxy "$work_dir/wcp-on.yaml"
w="$work_dir/wcp-on.yaml"
test "$(yq -r '.workerCallbackProxy.workerDomain' "$w")" = "grpc-worker.example.com" || fail "proxy: expected workerDomain"
test "$(yq -r '.workerCallbackProxy.tls.frontend.certificate.issuerRef.name' "$w")" = "nvcf-openbao-pki" || fail "proxy: expected the OpenBao service PKI issuer"
test "$(yq -r '.workerCallbackProxy.topologySpreadConstraints[0].whenUnsatisfiable' "$w")" = "ScheduleAnyway" ||
  fail "proxy: expected HA zone spread"

manifest="$work_dir/wcp-manifest.yaml"
helm template worker-callback-proxy "$test_helm_dir/worker-callback-proxy/worker-callback-proxy" --namespace nvcf --values "$w" >"$manifest" ||
  fail "proxy chart must render with stack values"
grep -Fq -- '- "*.grpc-worker.example.com"' "$manifest" || fail "proxy: expected frontend certificate for the worker domain"
envoy_regex="$(yq ea -r 'select(.kind == "ConfigMap") | .data["envoy.yaml"]' "$manifest" | yq -r '.. | select(has("safe_regex")) | .safe_regex.regex' | sort -u)"
[[ "$envoy_regex" == *'invocation\.nvcf\.svc\.cluster\.local:8443$' ]] ||
  fail "proxy: invocation tunnels must target the invocation-service TLS port, got $envoy_regex"
test "$(yq ea -r 'select(.kind == "Service" and .metadata.name == "worker-callback-proxy") | .metadata.name' "$manifest")" = "worker-callback-proxy" ||
  fail "proxy: Service name must match the invocation proxy address"
grep -Fq -- "$grpc_cert_secret" "$manifest" ||
  fail "proxy: upstream CA must come from the grpc-proxy certificate Secret $grpc_cert_secret"
grep -Fq -- '*.grpc-headless.nvcf.svc.cluster.local' "$manifest" ||
  fail "proxy: upstream SAN must be the grpc-headless name the grpc-proxy certificate covers"

echo "== the proxy Service can be exposed =="
{ enabled_env preferred; cat <<'EOF'
  service:
    type: LoadBalancer
EOF
} | write_env
render_values worker-callback-proxy "$work_dir/wcp-lb.yaml"
test "$(yq -r '.workerCallbackProxy.service.type' "$work_dir/wcp-lb.yaml")" = "LoadBalancer" ||
  fail "proxy: workerCallbackProxy.service must reach the chart"

echo "== enabled without HA keeps chart replica defaults =="
enabled_env none | write_env
render_values grpc-proxy "$work_dir/grpc-none.yaml"
test "$(yq -r '.grpcproxy.replicaCount // ""' "$work_dir/grpc-none.yaml")" = "" || fail "grpc-proxy: replicaCount must not be forced without HA"
test "$(yq -r '.grpcproxy.podDisruptionBudget.enabled' "$work_dir/grpc-none.yaml")" = "false" || fail "grpc-proxy: PDB must stay off without HA"

echo "== operator proxy address wins =="
{ enabled_env preferred; cat <<'EOF'
invocation:
  env:
    WORKER_STREAM_PROPERTIES__PROXY_ADDRESS: http://custom-proxy:3128
EOF
} | write_env
render_values invocation-service "$work_dir/inv-custom.yaml"
test "$(yq -r '.invocation.env.WORKER_STREAM_PROPERTIES__PROXY_ADDRESS' "$work_dir/inv-custom.yaml")" = "http://custom-proxy:3128" ||
  fail "invocation: operator proxy address must win"

echo "== invocationProxyURL sets the worker proxy address =="
{ enabled_env preferred; cat <<'EOF'
global:
  workerEndpoints:
    invocationProxyURL: http://invocation-proxy.example.com:10084
EOF
} | write_env
render_values invocation-service "$work_dir/inv-proxy-url.yaml"
test "$(yq -r '.invocation.env.WORKER_STREAM_PROPERTIES__PROXY_ADDRESS' "$work_dir/inv-proxy-url.yaml")" = "http://invocation-proxy.example.com:10084" ||
  fail "invocation: invocationProxyURL must set the proxy address"

{ enabled_env preferred; cat <<'EOF'
global:
  workerEndpoints:
    invocationProxyURL: http://invocation-proxy.example.com:10084
invocation:
  env:
    WORKER_STREAM_PROPERTIES__PROXY_ADDRESS: http://custom-proxy:3128
EOF
} | write_env
render_values invocation-service "$work_dir/inv-proxy-url-env.yaml"
test "$(yq -r '.invocation.env.WORKER_STREAM_PROPERTIES__PROXY_ADDRESS' "$work_dir/inv-proxy-url-env.yaml")" = "http://custom-proxy:3128" ||
  fail "invocation: invocation.env must win over invocationProxyURL"

write_env <<'EOF'
highAvailability:
  mode: preferred
global:
  workerEndpoints:
    invocationProxyURL: http://invocation-proxy.example.com:10084
EOF
render_values invocation-service "$work_dir/inv-proxy-url-off.yaml"
test "$(yq -r '.invocation.env.WORKER_STREAM_PROPERTIES__PROXY_ADDRESS // ""' "$work_dir/inv-proxy-url-off.yaml")" = "" ||
  fail "invocation: invocationProxyURL must be ignored without the proxy"

echo "== enabled without the LLM add-on =="
write_env <<EOF
workerCallbackProxy:
  enabled: true
  workerDomain: grpc-worker.example.com
  chartPath: ${chart_path}
EOF
render_values worker-callback-proxy "$work_dir/wcp-no-llm.yaml" || fail "the proxy must render without the LLM add-on"
test "$(yq -r '.workerCallbackProxy.tls.frontend.certificate.issuerRef.name' "$work_dir/wcp-no-llm.yaml")" = "nvcf-openbao-pki" ||
  fail "proxy: expected the OpenBao service PKI issuer without LLM"
render_values grpc-proxy "$work_dir/grpc-no-llm.yaml" || fail "a worker domain outside addons.llm.pki.allowedDomains must render"

echo "== invalid combinations fail =="
write_env <<EOF
workerCallbackProxy:
  enabled: true
EOF
assert_render_fails no-domain "workerCallbackProxy.workerDomain is required"

write_env <<EOF
workerCallbackProxy:
  enabled: "true"
  workerDomain: grpc-worker.example.com
EOF
assert_render_fails string-enabled "workerCallbackProxy.enabled must be a YAML boolean"

write_env <<EOF
workerCallbackProxy:
  enabled: true
  workerDomain: https://grpc-worker.example.com
EOF
assert_render_fails bad-domain "must be a DNS name without a scheme or wildcard"

{ enabled_env preferred; cat <<'EOF'
global:
  workerEndpoints:
    invocationServiceURL: https://invocation.example.com
EOF
} | write_env
assert_render_fails split-invocation "cannot be combined with global.workerEndpoints.invocationServiceURL"

{ enabled_env preferred; cat <<'EOF'
grpcproxy:
  workerConnectBaseURL: https://grpc-worker.example.com
EOF
} | write_env
assert_render_fails split-grpc "cannot be combined with global.workerEndpoints.invocationServiceURL"

{ enabled_env preferred; cat <<'EOF'
global:
  workerEndpoints:
    grpcProxyWorkerConnectURL: https://tcp-proxy.grpc.example.com:443
EOF
} | write_env
assert_render_fails split-grpc-endpoint "cannot be combined with global.workerEndpoints.invocationServiceURL"

{ enabled_env preferred; cat <<'EOF'
invocation:
  env:
    WORKER_STREAM_PROPERTIES__SELF_ADDRESS: https://invocation.example.com
EOF
} | write_env
assert_render_fails fixed-self-address "invocation.env.WORKER_STREAM_PROPERTIES__SELF_ADDRESS"

echo "worker-callback-proxy-wiring: ok"
