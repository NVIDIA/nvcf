#!/usr/bin/env bash
set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
repo_dir="$(cd "$stack_dir/../../.." && pwd)"
work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "llm-servicemonitor-defaults: $*" >&2
  exit 1
}

write_values() {
  local global_metrics_enabled="$1"
  local release="$2"
  local values_file="$3"
  shift 3

  (
    cd "$stack_dir"
    HELMFILE_ENV=base HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" helmfile \
      --file helmfile.d/02-core.yaml.gotmpl \
      --environment default \
      --state-values-set addons.llm.enabled=true \
      --state-values-set "global.observability.metrics.enabled=$global_metrics_enabled" \
      --state-values-set ingress.gatewayApi.gateways.shared.name=shared-gw \
      --state-values-set ingress.gatewayApi.gateways.shared.namespace=envoy-gateway-system \
      --state-values-set ingress.gatewayApi.gateways.grpc.name=grpc-gw \
      --state-values-set ingress.gatewayApi.gateways.grpc.namespace=envoy-gateway-system \
      "$@" \
      --selector "name=$release" \
      write-values --output-file-template "$values_file" >/dev/null
  )
}

render_release() {
  local release="$1"
  local values_file="$2"
  local manifests_file="$3"
  local chart_dir

  case "$release" in
    llm-api-gateway) chart_dir="$repo_dir/deploy/helm/llm-api-gateway/llm-api-gateway" ;;
    llm-request-router) chart_dir="$repo_dir/deploy/helm/llm-request-router/llm-request-router" ;;
    *) fail "unknown LLM release: $release" ;;
  esac

  helm template "$release" "$chart_dir" \
    --namespace nvcf \
    --values "$values_file" >"$manifests_file"
}

assert_service_monitor() {
  local name="$1"
  local global_metrics_enabled="$2"
  local release="$3"
  local expected="$4"
  shift 4
  local values_file="$work_dir/$name-values.yaml"
  local manifests_file="$work_dir/$name-manifests.yaml"
  local actual

  write_values "$global_metrics_enabled" "$release" "$values_file" "$@"
  render_release "$release" "$values_file" "$manifests_file"

  if grep -q '^kind: ServiceMonitor$' "$manifests_file"; then
    actual=true
  else
    actual=false
  fi

  test "$actual" = "$expected" ||
    fail "$release ServiceMonitor expected $expected with global metrics $global_metrics_enabled, got $actual"
}

assert_service_monitor gateway-observability-disabled false llm-api-gateway false
assert_service_monitor router-observability-disabled false llm-request-router false
assert_service_monitor gateway-observability-enabled true llm-api-gateway true
assert_service_monitor router-observability-enabled true llm-request-router true
assert_service_monitor gateway-explicit-opt-out true llm-api-gateway false \
  --state-values-set addons.llm.gateway.metrics.serviceMonitor.enabled=false
assert_service_monitor router-explicit-opt-out true llm-request-router false \
  --state-values-set addons.llm.requestRouter.metrics.serviceMonitor.enabled=false

echo "llm-servicemonitor-defaults: all checks passed"
