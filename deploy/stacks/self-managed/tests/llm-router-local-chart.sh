#!/usr/bin/env bash
set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
chart_path="../../../helm/llm-request-router/llm-request-router"
work_dir="$(mktemp -d)"
values_file="$work_dir/llm-request-router-values.yaml"
local_manifest="$work_dir/llm-request-router.yaml"
stateful_values_file="$work_dir/llm-request-router-stateful-values.yaml"
trap 'rm -rf "$work_dir"' EXIT

result="$(cd "$stack_dir" && HELMFILE_ENV=base helmfile \
  --file helmfile.d/02-core.yaml.gotmpl \
  --environment default \
  --state-values-set addons.llm.enabled=true \
  --state-values-set-string "addons.llm.requestRouter.chartPath=$chart_path" \
  --state-values-set ingress.gatewayApi.gateways.shared.name=shared-gw \
  --state-values-set ingress.gatewayApi.gateways.shared.namespace=envoy-gateway-system \
  --state-values-set ingress.gatewayApi.gateways.grpc.name=grpc-gw \
  --state-values-set ingress.gatewayApi.gateways.grpc.namespace=envoy-gateway-system \
  --selector name=llm-request-router \
  list --skip-charts --output json)"

actual="$(jq -r '.[0].chart' <<<"$result")"
test "$actual" = "$chart_path" || {
  echo "llm-router-local-chart: expected $chart_path, got ${actual:-missing}" >&2
  exit 1
}

(cd "$stack_dir" && HELMFILE_ENV=base helmfile \
  --file helmfile.d/02-core.yaml.gotmpl \
  --environment default \
  --state-values-set addons.llm.enabled=true \
  --state-values-set-string "addons.llm.requestRouter.chartPath=$chart_path" \
  --state-values-set addons.llm.requestRouter.discovery.watchHeartbeatMs=7000 \
  --state-values-set-string 'addons.llm.requestRouter.discovery.remoteWatchUrls[0]=https://region-b.example.invalid:50071' \
  --state-values-set addons.llm.requestRouter.discovery.allowInsecureRemoteWatchHttp=true \
  --state-values-set ingress.gatewayApi.gateways.shared.name=shared-gw \
  --state-values-set ingress.gatewayApi.gateways.shared.namespace=envoy-gateway-system \
  --state-values-set ingress.gatewayApi.gateways.grpc.name=grpc-gw \
  --state-values-set ingress.gatewayApi.gateways.grpc.namespace=envoy-gateway-system \
  --selector name=llm-request-router \
  write-values --output-file-template "$values_file" >/dev/null)

main_repository="$(yq -r '.llmRequestRouter.image.repository' "$values_file")"
backend_repository="$(yq -r '.llmRequestRouter.backendRouter.image.repository' "$values_file")"
test "$backend_repository" = "$main_repository" || {
  echo "llm-router-local-chart: backend router must use the released Stargate image; got ${backend_repository:-missing}, expected ${main_repository:-missing}" >&2
  exit 1
}

main_tag="$(yq -r '.llmRequestRouter.image.tag // ""' "$values_file")"
test -z "$main_tag" || {
  echo "llm-router-local-chart: expected main router tag to inherit the chart default, got $main_tag" >&2
  exit 1
}

backend_tag="$(yq -r '.llmRequestRouter.backendRouter.image.tag // ""' "$values_file")"
test -z "$backend_tag" || {
  echo "llm-router-local-chart: expected backend router tag to inherit the chart default, got $backend_tag" >&2
  exit 1
}

helm template llm-request-router "$stack_dir/../../helm/llm-request-router/llm-request-router" \
  --namespace nvcf \
  --values "$values_file" >"$local_manifest"

main_registry="$(yq -r '.llmRequestRouter.image.registry' "$values_file")"
expected_stargate_image="${main_registry:+${main_registry}/}${main_repository}:0.14.2"
main_image="$(yq ea -r \
  'select(.kind == "Deployment" and .metadata.name == "llm-request-router" and .metadata.namespace == "nvcf") | .spec.template.spec.containers[0].image' \
  "$local_manifest")"
test "$main_image" = "$expected_stargate_image" || {
  echo "llm-router-local-chart: expected main router image $expected_stargate_image, got ${main_image:-missing}" >&2
  exit 1
}

backend_image="$(yq ea -r \
  'select(.kind == "Deployment" and .metadata.name == "llm-request-router-backend-router" and .metadata.namespace == "nvcf") | .spec.template.spec.containers[] | select(.name == "backend-router") | .image' \
  "$local_manifest")"
test "$backend_image" = "$expected_stargate_image" || {
  echo "llm-router-local-chart: expected backend router image $expected_stargate_image, got ${backend_image:-missing}" >&2
  exit 1
}

default_workload_kind="$(yq -r '.llmRequestRouter.workload.kind' "$values_file")"
test "$default_workload_kind" = "Deployment" || {
  echo "llm-router-local-chart: expected default workload kind Deployment, got ${default_workload_kind:-missing}" >&2
  exit 1
}

remote_watch_url="$(yq -r '.llmRequestRouter.discovery.remoteWatchUrls[0]' "$values_file")"
test "$remote_watch_url" = "https://region-b.example.invalid:50071" || {
  echo "llm-router-local-chart: expected remote Watch URL forwarding, got ${remote_watch_url:-missing}" >&2
  exit 1
}

allow_insecure_remote_watch_http="$(yq -r '.llmRequestRouter.discovery.allowInsecureRemoteWatchHttp' "$values_file")"
test "$allow_insecure_remote_watch_http" = "true" || {
  echo "llm-router-local-chart: expected development HTTP opt-in forwarding, got ${allow_insecure_remote_watch_http:-missing}" >&2
  exit 1
}

watch_heartbeat_ms="$(yq -r '.llmRequestRouter.discovery.watchHeartbeatMs' "$values_file")"
test "$watch_heartbeat_ms" = "7000" || {
  echo "llm-router-local-chart: expected Watch heartbeat forwarding, got ${watch_heartbeat_ms:-missing}" >&2
  exit 1
}

(cd "$stack_dir" && HELMFILE_ENV=base helmfile \
  --file helmfile.d/02-core.yaml.gotmpl \
  --environment default \
  --state-values-set addons.llm.enabled=true \
  --state-values-set-string "addons.llm.requestRouter.chartPath=$chart_path" \
  --state-values-set-string addons.llm.requestRouter.workload.kind=StatefulSet \
  --state-values-set ingress.gatewayApi.gateways.shared.name=shared-gw \
  --state-values-set ingress.gatewayApi.gateways.shared.namespace=envoy-gateway-system \
  --state-values-set ingress.gatewayApi.gateways.grpc.name=grpc-gw \
  --state-values-set ingress.gatewayApi.gateways.grpc.namespace=envoy-gateway-system \
  --selector name=llm-request-router \
  write-values --output-file-template "$stateful_values_file" >/dev/null)

stateful_workload_kind="$(yq -r '.llmRequestRouter.workload.kind' "$stateful_values_file")"
test "$stateful_workload_kind" = "StatefulSet" || {
  echo "llm-router-local-chart: expected explicit workload kind StatefulSet, got ${stateful_workload_kind:-missing}" >&2
  exit 1
}

echo "llm-router-local-chart: all checks passed"
