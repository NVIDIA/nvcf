#!/usr/bin/env bash
set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
repo_dir="$(cd "$stack_dir/../../.." && pwd)"
work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "llm-request-router-external-exposure: $*" >&2
  exit 1
}

# The api release inherits secrets/<env>-secrets.yaml, so render from a copy of
# the stack with a stub secrets file rather than writing into the source tree.
sandbox="$work_dir/stack"
cp -r "$stack_dir" "$sandbox"
printf '{}\n' >"$sandbox/secrets/base-secrets.yaml"

state_values=(
  --state-values-set ingress.gatewayApi.gateways.shared.name=shared
  --state-values-set ingress.gatewayApi.gateways.shared.namespace=envoy
  --state-values-set ingress.gatewayApi.gateways.grpc.name=grpc
  --state-values-set ingress.gatewayApi.gateways.grpc.namespace=envoy
)

write_values() {
  local release="$1"
  local output_file="$2"
  shift 2

  HELMFILE_ENV=base HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" helmfile \
    --file "$sandbox/helmfile.d/02-core.yaml.gotmpl" \
    --environment default \
    "${state_values[@]}" \
    "$@" \
    --selector "name=$release" \
    write-values \
    --output-file-template "$output_file" >/dev/null
}

gateway_values=(
  --state-values-set ingress.gatewayApi.gateways.llmRequestRouter.name=grpc-gw
  --state-values-set ingress.gatewayApi.gateways.llmRequestRouter.namespace=envoy-gateway-system
)

# Single-cluster default: the router keeps in-cluster behavior and none of the
# external dial settings reach the chart.
default_router="$work_dir/router-default.yaml"
write_values llm-request-router "$default_router" --state-values-set addons.llm.enabled=true
for key in grpcPylonDialAddr reverseTunnelPylonDialAddr advertisedHostnameTemplate; do
  if grep -q "$key" "$default_router"; then
    fail "default install leaked external router setting: $key"
  fi
done

# External exposure: pylon dials the shared addresses while the advertised
# hostname stays the per-pod gRPC authority and QUIC SNI.
external_router="$work_dir/router-external.yaml"
write_values llm-request-router "$external_router" \
  --state-values-set addons.llm.enabled=true \
  --state-values-set-string addons.llm.requestRouter.external.grpcDialAddress=llm-router.example.com:50071 \
  --state-values-set-string addons.llm.requestRouter.external.quicDialAddress=llm-router.example.com:50072 \
  --state-values-set-string 'addons.llm.requestRouter.external.advertisedHostnameTemplate={pod_name}.llm-router.example.com'
grep -q 'grpcPylonDialAddr: llm-router.example.com:50071' "$external_router" ||
  fail "external exposure did not render the gRPC pylon dial address"
grep -q 'reverseTunnelPylonDialAddr: llm-router.example.com:50072' "$external_router" ||
  fail "external exposure did not render the QUIC pylon dial address"
grep -q 'advertisedHostnameTemplate:' "$external_router" ||
  fail "external exposure did not render the advertised hostname template"

# Routes are opt-in and must carry the Gateway coordinates through to the chart.
routes_on="$work_dir/ingress-routes-on.yaml"
write_values ingress "$routes_on" \
  --state-values-set addons.llm.enabled=true \
  --state-values-set ingress.gatewayApi.routes.llmRequestRouter.enabled=true \
  "${gateway_values[@]}"
grep -q 'grpcListenerName: llm-router-grpc' "$routes_on" ||
  fail "router routes did not render the gRPC listener name"
grep -q 'quicListenerName: llm-router-quic' "$routes_on" ||
  fail "router routes did not render the QUIC listener name"

# Without the LLM addon there is no llm-request-router Service, so the routes
# must stay off even when an operator opts in.
routes_no_addon="$work_dir/ingress-routes-no-addon.yaml"
write_values ingress "$routes_no_addon" \
  --state-values-set addons.llm.enabled=false \
  --state-values-set ingress.gatewayApi.routes.llmRequestRouter.enabled=true \
  "${gateway_values[@]}"
awk '/^  routes:/,0' "$routes_no_addon" |
  grep -A1 'llmRequestRouter:' |
  grep -q 'enabled: false' ||
  fail "router routes were enabled without the LLM addon"

# A missing Gateway name is an operator error and must fail the render rather
# than produce a route with no parent.
if write_values ingress "$work_dir/ingress-missing-gateway.yaml" \
  --state-values-set addons.llm.enabled=true \
  --state-values-set ingress.gatewayApi.routes.llmRequestRouter.enabled=true 2>/dev/null; then
  fail "enabling router routes without a Gateway name did not fail the render"
fi

# Chart level: both route kinds and the shared ReferenceGrant render together,
# and nothing renders when the routes stay disabled.
chart_dir="$repo_dir/deploy/helm/gateway-routes/chart"
cat >"$work_dir/gw-values.yaml" <<'YAML'
nvcfGatewayRoutes:
  gateways:
    llmRequestRouter:
      name: grpc-gw
      namespace: envoy-gateway-system
  routes:
    llmRequestRouter:
      enabled: true
YAML
helm template ingress "$chart_dir" -f "$work_dir/gw-values.yaml" >"$work_dir/gw-on.yaml"
for kind in "kind: TCPRoute" "kind: UDPRoute" "kind: ReferenceGrant"; do
  grep -q "$kind" "$work_dir/gw-on.yaml" ||
    fail "router routes did not render $kind"
done
grep -q 'sectionName: llm-router-quic' "$work_dir/gw-on.yaml" ||
  fail "UDPRoute did not attach to the QUIC listener"

helm template ingress "$chart_dir" >"$work_dir/gw-off.yaml"
if grep -qE '^  name: llm-request-router-(grpc|quic)$|^kind: UDPRoute$' "$work_dir/gw-off.yaml"; then
  fail "router routes rendered while disabled"
fi

echo "llm-request-router-external-exposure: ok"
