#!/usr/bin/env bash
set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work_dir="$(mktemp -d)"
test_stack_dir="$work_dir/self-managed"
environment_name="api-keys-startup-probe-test"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "api-keys-startup-probe: $*" >&2
  exit 1
}

mkdir -p "$test_stack_dir"
cp -R "$stack_dir"/. "$test_stack_dir"
: >"$test_stack_dir/secrets/$environment_name-secrets.yaml"
printf '%s\n' \
  'apikeys:' \
  '  startupProbe:' \
  '    failureThreshold: 60' \
  >"$test_stack_dir/environments/$environment_name.yaml"

values_file="$work_dir/api-keys-values.yaml"
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
    --state-values-set apikeys.startupProbe.failureThreshold=60 \
    --selector name=api-keys \
    write-values \
    --output-file-template "$values_file"

actual="$(yq -r '.apikeys.startupProbe.failureThreshold // "missing"' "$values_file")"
test "$actual" = "60" ||
  fail "expected apikeys.startupProbe.failureThreshold=60, got $actual"

echo "api-keys-startup-probe: all checks passed"
