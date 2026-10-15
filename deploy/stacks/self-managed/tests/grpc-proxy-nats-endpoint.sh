#!/usr/bin/env bash
set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work_dir="$(mktemp -d)"
test_stack_dir="$work_dir/self-managed"
environment_name="grpc-proxy-nats-endpoint-test"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "grpc-proxy-nats-endpoint: $*" >&2
  exit 1
}

mkdir -p "$test_stack_dir"
cp -R "$stack_dir"/. "$test_stack_dir"
: >"$test_stack_dir/secrets/$environment_name-secrets.yaml"
printf '%s\n' \
  'global:' \
  '  workerEndpoints:' \
  '    nvcfNatsServiceURL: nats://control.example.test:4222' \
  >"$test_stack_dir/environments/$environment_name.yaml"

values_file="$work_dir/grpc-proxy-values.yaml"
HELMFILE_ENV="$environment_name" \
  HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" \
  helmfile \
    --file "$test_stack_dir/helmfile.d/02-core.yaml.gotmpl" \
    --environment default \
    --state-values-set ingress.gatewayApi.controllerNamespace=envoy-gateway-system \
    --state-values-set ingress.gatewayApi.gateways.shared.name=shared-gw \
    --state-values-set ingress.gatewayApi.gateways.shared.namespace=envoy-gateway-system \
    --state-values-set ingress.gatewayApi.gateways.grpc.name=grpc-gw \
    --state-values-set ingress.gatewayApi.gateways.grpc.namespace=envoy-gateway-system \
    --selector name=grpc-proxy \
    write-values \
    --output-file-template "$values_file"

actual="$(yq -r '.grpcproxy.env.NATS_FQDN // "missing"' "$values_file")"
expected='nats://nats.nats-system.svc.cluster.local:4222'
test "$actual" = "$expected" ||
  fail "expected control-plane grpc-proxy NATS_FQDN=$expected, got $actual"

echo "grpc-proxy-nats-endpoint: all checks passed"
