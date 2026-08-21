#!/usr/bin/env bash
set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work_dir="$(mktemp -d)"
test_stack_dir="$work_dir/self-managed"
environment_name="llm-router-split-cluster-test"
environment_file="$test_stack_dir/environments/$environment_name.yaml"
secrets_file="$test_stack_dir/secrets/$environment_name-secrets.yaml"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "llm-router-split-cluster: $*" >&2
  exit 1
}

mkdir -p "$test_stack_dir"
cp -R "$stack_dir"/. "$test_stack_dir"
printf '{}\n' >"$secrets_file"

printf '%s\n' \
  'addons:' \
  '  llm:' \
  '    enabled: true' \
  '    requestRouter:' \
  '      backendRouter:' \
  '        pylonGrpcDialAddress: control.example.test:50071' \
  '        pylonReverseTunnelDialAddress: control.example.test:50072' \
  'ingress:' \
  '  gatewayApi:' \
  '    controllerNamespace: envoy-gateway-system' \
  '    routes:' \
  '      llmWorker:' \
  '        enabled: true' \
  '        backend:' \
  '          namespace: nvcf' \
  '    gateways:' \
  '      shared:' \
  '        name: shared-gw' \
  '        namespace: envoy-gateway-system' \
  '      grpc:' \
  '        name: grpc-gw' \
  '        namespace: envoy-gateway-system' \
  '      llmGrpc:' \
  '        name: llm-grpc-gw' \
  '        namespace: envoy-gateway-system' \
  '        listenerName: llm-grpc' \
  '      llmQuic:' \
  '        name: llm-quic-gw' \
  '        namespace: envoy-gateway-system' \
  '        listenerName: llm-quic' \
  >"$environment_file"

HELMFILE_ENV="$environment_name" \
  HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" \
  helmfile \
    --file "$test_stack_dir/helmfile.d/02-core.yaml.gotmpl" \
    --environment default \
    --selector name=ingress \
    write-values \
    --output-file-template "$work_dir/ingress-values.yaml"

values_file="$work_dir/ingress-values.yaml"
test -f "$values_file" || fail "ingress values were not rendered"

assert_value() {
  local path="$1"
  local expected="$2"
  local actual
  actual="$(yq -r "$path" "$values_file")"
  test "$actual" = "$expected" ||
    fail "expected $path to be $expected, got $actual"
}

assert_value '.nvcfGatewayRoutes.routes.llmWorker.enabled' 'true'
assert_value '.nvcfGatewayRoutes.routes.llmWorker.backend.namespace' 'nvcf'
assert_value '.nvcfGatewayRoutes.gateways.llmGrpc.name' 'llm-grpc-gw'
assert_value '.nvcfGatewayRoutes.gateways.llmGrpc.listenerName' 'llm-grpc'
assert_value '.nvcfGatewayRoutes.gateways.llmQuic.name' 'llm-quic-gw'
assert_value '.nvcfGatewayRoutes.gateways.llmQuic.listenerName' 'llm-quic'

echo "llm-router-split-cluster: all checks passed"
