#!/usr/bin/env bash
set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
repo_dir="$(cd "$stack_dir/../../.." && pwd)"
work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "llm-gateway-auth-transport: $*" >&2
  exit 1
}

render_stack_gateway() {
  local case_name="$1"
  shift
  local values_file="$work_dir/$case_name-values.yaml"
  local manifests_file="$work_dir/$case_name-manifests.yaml"

  (
    cd "$stack_dir"
    HELMFILE_ENV=base HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" helmfile \
      --file helmfile.d/02-core.yaml.gotmpl \
      --environment default \
      --state-values-set addons.llm.enabled=true \
      --state-values-set ingress.gatewayApi.gateways.shared.name=shared-gw \
      --state-values-set ingress.gatewayApi.gateways.shared.namespace=envoy-gateway-system \
      --state-values-set ingress.gatewayApi.gateways.grpc.name=grpc-gw \
      --state-values-set ingress.gatewayApi.gateways.grpc.namespace=envoy-gateway-system \
      "$@" \
      --selector name=llm-api-gateway \
      write-values --output-file-template "$values_file" >/dev/null
  )

  helm template llm-api-gateway \
    "$repo_dir/deploy/helm/llm-api-gateway/llm-api-gateway" \
    --namespace nvcf \
    --values "$values_file" >"$manifests_file"

  printf '%s\n' "$manifests_file"
}

read_configmap_value() {
  local manifests_file="$1"
  local key="$2"

  awk -v key="$key" '
    /^kind: ConfigMap$/ { is_configmap = 1; in_metadata = in_data = 0; next }
    /^kind:/ { is_configmap = in_metadata = in_data = named = 0 }
    is_configmap && /^metadata:$/ { in_metadata = 1; in_data = 0; next }
    is_configmap && /^data:$/ { in_metadata = 0; in_data = 1; next }
    in_metadata && /^  name: llm-api-gateway$/ { named = 1; next }
    named && in_data && $0 ~ "^  " key ":" {
      value = $0
      sub("^  " key ":[[:space:]]*", "", value)
      gsub(/^"|"$/, "", value)
      print value
      found = 1
      exit
    }
    END { if (!found) exit 1 }
  ' "$manifests_file"
}

assert_configmap_value() {
  local manifests_file="$1"
  local key="$2"
  local expected="$3"
  local actual

  actual="$(read_configmap_value "$manifests_file" "$key")" ||
    fail "$key is missing from the llm-api-gateway ConfigMap"
  test "$actual" = "$expected" ||
    fail "$key expected $expected, got $actual"
}

default_manifests="$(render_stack_gateway default)"
assert_configmap_value "$default_manifests" NVCF_GRPC_INSECURE true
assert_configmap_value "$default_manifests" NVCF_GRPC_ADDR \
  api.nvcf.svc.cluster.local:9090

tls_manifests="$(render_stack_gateway explicit-tls \
  --state-values-set addons.llm.gateway.auth.grpcInsecure=false)"
assert_configmap_value "$tls_manifests" NVCF_GRPC_INSECURE false
assert_configmap_value "$tls_manifests" NVCF_GRPC_ADDR \
  api.nvcf.svc.cluster.local:9090

generic_manifests="$work_dir/generic-chart-manifests.yaml"
helm template llm-api-gateway \
  "$repo_dir/deploy/helm/llm-api-gateway/llm-api-gateway" \
  --namespace nvcf \
  --set-string llmApiGateway.image.repository=example.invalid/llm-api-gateway \
  >"$generic_manifests"
assert_configmap_value "$generic_manifests" NVCF_GRPC_INSECURE false

echo "llm-gateway-auth-transport: all checks passed"
