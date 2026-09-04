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

# Read the pinned version from the ingress release itself rather than
# hardcoding it, so this check does not need updating every pin bump.
pinned_version="$(
  awk '/- name: ingress/{f=1} f && /^ *version:/{print; exit}' \
    "$stack_dir/helmfile.d/02-core.yaml.gotmpl" |
    sed 's/.*version: *//'
)"
test -n "$pinned_version" || {
  echo "gateway-routes-local-chart: could not read the pinned version from 02-core.yaml.gotmpl" >&2
  exit 1
}
test "$default_version" = "$pinned_version" || {
  echo "gateway-routes-local-chart: expected default version $pinned_version, got ${default_version:-missing}" >&2
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

echo "gateway-routes-local-chart: all checks passed"
