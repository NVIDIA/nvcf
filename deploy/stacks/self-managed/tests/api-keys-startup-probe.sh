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
  '  livenessProbe:' \
  '    timeoutSeconds: 5' \
  '  resources:' \
  '    limits:' \
  '      cpu: 1' \
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
    --state-values-set apikeys.livenessProbe.timeoutSeconds=5 \
    --state-values-set apikeys.resources.limits.cpu=1 \
    --selector name=api-keys \
    write-values \
    --output-file-template "$values_file"

actual="$(yq -r '.apikeys.startupProbe.failureThreshold // "missing"' "$values_file")"
test "$actual" = "60" ||
  fail "expected apikeys.startupProbe.failureThreshold=60, got $actual"
test "$(yq -r '.apikeys.livenessProbe.timeoutSeconds // "missing"' "$values_file")" = "5" ||
  fail "API Keys liveness timeout override was not forwarded"
test "$(yq -r '.apikeys.resources.limits.cpu // "missing"' "$values_file")" = "1" ||
  fail "API Keys CPU limit override was not forwarded"

chart_dir="$stack_dir/../../helm/api-keys-colocated/api-keys"
manifest="$work_dir/api-keys.yaml"
helm template api-keys "$chart_dir" --namespace api-keys \
  --set apikeys.image.registry=example.com \
  --set apikeys.image.repository=api-keys >"$manifest"
test "$(yq -r 'select(.kind == "Deployment") | .spec.template.spec.containers[0].securityContext.runAsNonRoot' "$manifest")" = true ||
  fail "API Keys container must run as non-root"
test "$(yq -r 'select(.kind == "Deployment") | .spec.template.spec.containers[0].securityContext.runAsUser' "$manifest")" = 1000 ||
  fail "API Keys container must use UID 1000"

echo "api-keys-startup-probe: all checks passed"
