#!/usr/bin/env bash
set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
chart_path="../../../helm/gateway-routes/chart"

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
