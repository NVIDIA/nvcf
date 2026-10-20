#!/usr/bin/env bash
# Test that workerCallbackProxy wires the Envoy release, grpc-proxy, and
# invocation-service together, lifts their single-replica HA pin, and rejects
# unsupported combinations.
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
  workerDomain: grpc-worker.nvcf.svc.cluster.local
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
test "$(yq -r '.grpcproxy.replicaCount' "$work_dir/grpc-off.yaml")" = "1" || fail "grpc-proxy must stay at 1 replica without the proxy"
test "$(yq -r '.grpcproxy.ports // "" ' "$work_dir/grpc-off.yaml")" = "" || fail "grpc-proxy ports must not change without the proxy"
test "$(yq -r '.grpcproxy.env.ENABLE_HTTP3_CONNECT // ""' "$work_dir/grpc-off.yaml")" = "" || fail "HTTP/3 must stay off without the proxy"
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
test "$(yq -r '.grpcproxy.replicaCount' "$g")" = "2" || fail "grpc-proxy: expected 2 replicas"
test "$(yq -r '.grpcproxy.podDisruptionBudget.enabled' "$g")" = "true" || fail "grpc-proxy: expected PDB enabled"
test "$(yq -r '.grpcproxy.env.ENABLE_HTTP3_CONNECT' "$g")" = "true" || fail "grpc-proxy: expected HTTP/3 on"
test "$(yq -r '.grpcproxy.env.ENABLE_HTTP1_CONNECT' "$g")" = "false" || fail "grpc-proxy: expected HTTP/1 off"
test "$(yq -r '.grpcproxy.env.PUBLIC_CERT_PATH' "$g")" = "/etc/grpc-proxy/worker-tls/tls.crt" || fail "grpc-proxy: expected cert path"
test "$(yq -r '.grpcproxy.workerConnectBaseURL' "$g")" = "https://grpc-worker.nvcf.svc.cluster.local" ||
  fail "grpc-proxy: expected callback base URL from workerDomain"
test "$(yq -r '.grpcproxy.ports[] | select(.name == "worker-quic") | .protocol + "/" + (.containerPort | tostring)' "$g")" = "UDP/10084" ||
  fail "grpc-proxy: expected UDP 10084 worker-quic port"
test "$(yq -r '.grpcproxy.ports | length' "$g")" = "4" || fail "grpc-proxy: chart default ports must be kept"
test "$(yq -r '.grpcproxy.volumes[0].secret.secretName' "$g")" = "grpc-proxy-pod-tls" || fail "grpc-proxy: expected pod certificate volume"
# `list` drops needs and `build` pulls every chart, so read the needs edge
# from the rendered state that debug logging prints as line-numbered YAML.
needs="$(
  helmfile_core --debug list --skip-charts 2>&1 >/dev/null |
    awk '
      /^rendering result of "[^"]+":$/ { print "---"; capture = 1; next }
      capture && /^ *[0-9]+: / { sub(/^ *[0-9]+: ?/, ""); print; next }
      capture { capture = 0 }
    ' |
    yq ea -r 'select(.releases) | .releases[] | select(.name == "grpc-proxy") | .needs[]'
)"
grep -Fxq 'nvcf/worker-callback-proxy' <<<"$needs" || fail "grpc-proxy must need nvcf/worker-callback-proxy, got: $needs"

render_values invocation-service "$work_dir/inv-on.yaml"
i="$work_dir/inv-on.yaml"
test "$(yq -r '.invocation.replicaCount' "$i")" = "2" || fail "invocation: expected 2 replicas"
test "$(yq -r '.invocation.podDisruptionBudget.enabled' "$i")" = "true" || fail "invocation: expected PDB enabled"
test "$(yq -r '.invocation.env.WORKER_STREAM_PROPERTIES__PROXY_ADDRESS' "$i")" = "http://worker-callback-proxy.nvcf.svc.cluster.local:10084" ||
  fail "invocation: expected proxy address"
test "$(yq -r '.invocation.env.WORKER_STREAM_PROPERTIES__SELF_ADDRESS // ""' "$i")" = "" || fail "invocation: self address must not be set"

render_values worker-callback-proxy "$work_dir/wcp-on.yaml"
w="$work_dir/wcp-on.yaml"
test "$(yq -r '.workerCallbackProxy.workerDomain' "$w")" = "grpc-worker.nvcf.svc.cluster.local" || fail "proxy: expected workerDomain"
test "$(yq -r '.workerCallbackProxy.tls.frontend.certificate.issuerRef.name' "$w")" = "nvcf-openbao-pki" || fail "proxy: expected LLM PKI issuer"
test "$(yq -r '.workerCallbackProxy.topologySpreadConstraints[0].whenUnsatisfiable' "$w")" = "ScheduleAnyway" ||
  fail "proxy: expected HA zone spread"

manifest="$work_dir/wcp-manifest.yaml"
helm template worker-callback-proxy "$test_helm_dir/worker-callback-proxy/worker-callback-proxy" --namespace nvcf --values "$w" >"$manifest" ||
  fail "proxy chart must render with stack values"
grep -Fq -- '- "*.grpc-worker.nvcf.svc.cluster.local"' "$manifest" || fail "proxy: expected frontend certificate for the worker domain"
test "$(yq ea -r 'select(.kind == "Service") | .metadata.name' "$manifest")" = "worker-callback-proxy" ||
  fail "proxy: Service name must match the invocation proxy address"

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

echo "worker-callback-proxy-wiring: ok"
