#!/usr/bin/env bash
set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
chart_path="../../../helm/gateway-routes/chart"

default_result="$(cd "$stack_dir" && HELMFILE_ENV=base helmfile \
  --file helmfile.d/02-core.yaml.gotmpl \
  --environment default \
  --state-values-set ingress.gatewayApi.enabled=true \
  --state-values-set ingress.gatewayApi.controllerNamespace=gateway \
  --state-values-set ingress.gatewayApi.routes.llmWorker.enabled=true \
  --state-values-set ingress.gatewayApi.routes.llmWorker.backend.namespace=nvcf \
  --state-values-set-string global.workerEndpoints.llmRequestRouterAddress=http://llm-grpc-gw:50071 \
  --state-values-set-string addons.llm.requestRouter.backendRouter.pylonGrpcDialAddress=http://llm-grpc-gw:50071 \
  --state-values-set-string addons.llm.requestRouter.backendRouter.pylonReverseTunnelDialAddress=llm-quic-gw:50072 \
  --state-values-set addons.llm.requestRouter.grpcTls.allowInsecureHttp=true \
  --state-values-set ingress.gatewayApi.gateways.shared.name=shared-gw \
  --state-values-set ingress.gatewayApi.gateways.shared.namespace=gateway \
  --state-values-set ingress.gatewayApi.gateways.grpc.name=grpc-gw \
  --state-values-set ingress.gatewayApi.gateways.grpc.namespace=gateway \
  --state-values-set ingress.gatewayApi.gateways.llmGrpc.name=llm-grpc-gw \
  --state-values-set ingress.gatewayApi.gateways.llmGrpc.namespace=gateway \
  --state-values-set ingress.gatewayApi.gateways.llmGrpc.listenerName=llm-grpc \
  --state-values-set ingress.gatewayApi.gateways.llmQuic.name=llm-quic-gw \
  --state-values-set ingress.gatewayApi.gateways.llmQuic.namespace=gateway \
  --state-values-set ingress.gatewayApi.gateways.llmQuic.listenerName=llm-quic \
  --selector name=ingress \
  list --skip-charts --output json)"

default_chart="$(jq -r '.[0].chart // ""' <<<"$default_result")"
default_version="$(jq -r '.[0].version // ""' <<<"$default_result")"
test "$default_chart" = 'nvcf/nvcf-gateway-routes' || {
  echo "gateway-routes-local-chart: expected default chart, got ${default_chart:-missing}" >&2
  exit 1
}
test "$default_version" = '1.17.0' || {
  echo "gateway-routes-local-chart: expected default version 1.17.0, got ${default_version:-missing}" >&2
  exit 1
}

default_values="$(mktemp)"
trap 'rm -f "$default_values"' EXIT
(cd "$stack_dir" && HELMFILE_ENV=base helmfile \
  --file helmfile.d/02-core.yaml.gotmpl \
  --environment default \
  --state-values-set ingress.gatewayApi.enabled=true \
  --state-values-set ingress.gatewayApi.controllerNamespace=gateway \
  --state-values-set ingress.gatewayApi.routes.llmWorker.enabled=true \
  --state-values-set ingress.gatewayApi.routes.llmWorker.backend.namespace=nvcf \
  --state-values-set-string global.workerEndpoints.llmRequestRouterAddress=http://llm-grpc-gw:50071 \
  --state-values-set-string addons.llm.requestRouter.backendRouter.pylonGrpcDialAddress=http://llm-grpc-gw:50071 \
  --state-values-set-string addons.llm.requestRouter.backendRouter.pylonReverseTunnelDialAddress=llm-quic-gw:50072 \
  --state-values-set addons.llm.requestRouter.grpcTls.allowInsecureHttp=true \
  --state-values-set ingress.gatewayApi.gateways.shared.name=shared-gw \
  --state-values-set ingress.gatewayApi.gateways.shared.namespace=gateway \
  --state-values-set ingress.gatewayApi.gateways.grpc.name=grpc-gw \
  --state-values-set ingress.gatewayApi.gateways.grpc.namespace=gateway \
  --state-values-set ingress.gatewayApi.gateways.llmGrpc.name=llm-grpc-gw \
  --state-values-set ingress.gatewayApi.gateways.llmGrpc.namespace=gateway \
  --state-values-set ingress.gatewayApi.gateways.llmGrpc.listenerName=llm-grpc \
  --state-values-set ingress.gatewayApi.gateways.llmQuic.name=llm-quic-gw \
  --state-values-set ingress.gatewayApi.gateways.llmQuic.namespace=gateway \
  --state-values-set ingress.gatewayApi.gateways.llmQuic.listenerName=llm-quic \
  --selector name=ingress \
  write-values --output-file-template "$default_values" >/dev/null)

test "$(yq -r '.nvcfGatewayRoutes.routes.llmWorker.enabled' "$default_values")" = 'true' || {
  echo "gateway-routes-local-chart: default chart values did not enable the LLM worker routes" >&2
  exit 1
}
test "$(yq -r '.nvcfGatewayRoutes.gateways.llmGrpc.listenerName' "$default_values")" = 'llm-grpc' || {
  echo "gateway-routes-local-chart: default chart values did not configure the LLM TCP listener" >&2
  exit 1
}
test "$(yq -r '.nvcfGatewayRoutes.gateways.llmQuic.listenerName' "$default_values")" = 'llm-quic' || {
  echo "gateway-routes-local-chart: default chart values did not configure the LLM UDP listener" >&2
  exit 1
}

result="$(cd "$stack_dir" && HELMFILE_ENV=base helmfile \
  --file helmfile.d/02-core.yaml.gotmpl \
  --environment default \
  --state-values-set ingress.gatewayApi.enabled=true \
  --state-values-set ingress.gatewayApi.controllerNamespace=gateway \
  --state-values-set ingress.gatewayApi.gateways.shared.name=shared-gw \
  --state-values-set ingress.gatewayApi.gateways.shared.namespace=gateway \
  --state-values-set ingress.gatewayApi.gateways.grpc.name=grpc-gw \
  --state-values-set ingress.gatewayApi.gateways.grpc.namespace=gateway \
  --state-values-set-string "ingress.gatewayApi.chartPath=$chart_path" \
  --selector name=ingress \
  list --skip-charts --output json)"

actual="$(jq -r '.[0].chart' <<<"$result")"
test "$actual" = "$chart_path" || {
  echo "gateway-routes-local-chart: expected $chart_path, got ${actual:-missing}" >&2
  exit 1
}

fail() {
  echo "gateway-routes-local-chart: $*" >&2
  exit 1
}

# The local source assertion above protects contributor workflows. Render the
# Helmfile-selected published artifact separately so its TCP and UDP worker
# route contract cannot drift behind the pinned chart version.
work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT
published_chart_registry="${NVCF_PUBLISHED_CHART_REGISTRY:-nvcr.io}"
published_chart_repository="${NVCF_PUBLISHED_CHART_REPOSITORY:-nvidia/nvcf}"
published_manifest="$work_dir/published-gateway-routes.yaml"
published_release_list="$work_dir/published-gateway-release.json"
published_source_args=(
  --state-values-set-string "global.helm.sources.registry=$published_chart_registry"
  --state-values-set-string "global.helm.sources.repository=$published_chart_repository"
)
published_values_args=(
  --state-values-set-string global.workerEndpoints.llmRequestRouterAddress=https://llm-grpc.example.com:50071
  --state-values-set addons.llm.enabled=true
  --state-values-set addons.llm.pki.enabled=true
  --state-values-set addons.llm.pki.allowedDomains=cluster.local
  --state-values-set-string addons.llm.requestRouter.backendRouter.pylonGrpcDialAddress=https://llm-grpc.example.com:50071
  --state-values-set-string addons.llm.requestRouter.backendRouter.pylonReverseTunnelDialAddress=llm-quic.example.com:50072
  --state-values-set addons.llm.requestRouter.grpcTls.enabled=true
  --state-values-set addons.llm.requestRouter.grpcTls.mode=certManager
  --state-values-set addons.llm.requestRouter.grpcTls.secretName=llm-grpc-tls
  --state-values-set addons.llm.requestRouter.grpcTls.dnsNames[0]=llm-grpc.example.com
  --state-values-set addons.llm.requestRouter.grpcTls.issuerRef.kind=ClusterIssuer
  --state-values-set addons.llm.requestRouter.grpcTls.issuerRef.name=nvcf-openbao-pki
  --state-values-set ingress.gatewayApi.enabled=true
  --state-values-set ingress.gatewayApi.controllerNamespace=gateway
  --state-values-set ingress.gatewayApi.gateways.shared.name=shared-gw
  --state-values-set ingress.gatewayApi.gateways.shared.namespace=gateway
  --state-values-set ingress.gatewayApi.gateways.grpc.name=grpc-gw
  --state-values-set ingress.gatewayApi.gateways.grpc.namespace=gateway
  --state-values-set ingress.gatewayApi.routes.llmWorker.enabled=true
  --state-values-set ingress.gatewayApi.routes.llmWorker.backend.namespace=nvcf
  --state-values-set ingress.gatewayApi.gateways.llmGrpc.name=llm-grpc-gw
  --state-values-set ingress.gatewayApi.gateways.llmGrpc.namespace=gateway
  --state-values-set ingress.gatewayApi.gateways.llmGrpc.listenerName=llm-grpc
  --state-values-set ingress.gatewayApi.gateways.llmQuic.name=llm-quic-gw
  --state-values-set ingress.gatewayApi.gateways.llmQuic.namespace=gateway
  --state-values-set ingress.gatewayApi.gateways.llmQuic.listenerName=llm-quic
)

(cd "$stack_dir" && HELMFILE_ENV=base HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" helmfile \
  --file helmfile.d/02-core.yaml.gotmpl \
  --environment default \
  "${published_source_args[@]}" \
  "${published_values_args[@]}" \
  --selector name=ingress \
  list --skip-charts --output json >"$published_release_list")

test "$(yq -r '.[0].chart' "$published_release_list")" = \
  'nvcf/nvcf-gateway-routes' ||
  fail "default chart source did not select nvcf/nvcf-gateway-routes"
test "$(yq -r '.[0].version' "$published_release_list")" = '1.17.0' ||
  fail "default chart source did not select gateway-routes 1.17.0"

(cd "$stack_dir" && HELMFILE_ENV=base HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" helmfile \
  --file helmfile.d/02-core.yaml.gotmpl \
  --environment default \
  "${published_source_args[@]}" \
  "${published_values_args[@]}" \
  --selector name=ingress \
  template >"$published_manifest")

assert_route_contract() {
  local kind="$1"
  local name="$2"
  local gateway_name="$3"
  local listener_name="$4"
  local backend_port="$5"
  local route="$work_dir/${kind}-${name}.yaml"

  yq ea -o=yaml \
    "select(.kind == \"$kind\" and .metadata.name == \"$name\")" \
    "$published_manifest" >"$route"
  test -s "$route" || fail "published chart omitted $kind/$name"
  test "$(yq -r '.spec.parentRefs[0].name' "$route")" = "$gateway_name" ||
    fail "$kind/$name selected the wrong Gateway"
  test "$(yq -r '.spec.parentRefs[0].namespace' "$route")" = 'gateway' ||
    fail "$kind/$name selected the wrong Gateway namespace"
  test "$(yq -r '.spec.parentRefs[0].sectionName' "$route")" = "$listener_name" ||
    fail "$kind/$name selected the wrong Gateway listener"
  test "$(yq -r '.spec.rules[0].backendRefs[0].name' "$route")" = \
    'llm-request-router-backend-router' ||
    fail "$kind/$name selected the wrong backend Service"
  test "$(yq -r '.spec.rules[0].backendRefs[0].namespace' "$route")" = 'nvcf' ||
    fail "$kind/$name selected the wrong backend namespace"
  test "$(yq -r '.spec.rules[0].backendRefs[0].port' "$route")" = "$backend_port" ||
    fail "$kind/$name selected the wrong backend port"
}

assert_route_contract GRPCRoute llm-worker-grpc llm-grpc-gw llm-grpc 50071
assert_route_contract UDPRoute llm-worker-quic llm-quic-gw llm-quic 50072

echo "gateway-routes-local-chart: all checks passed"
