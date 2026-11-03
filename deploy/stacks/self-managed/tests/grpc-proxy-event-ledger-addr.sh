#!/usr/bin/env bash
set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work_dir="$(mktemp -d)"
test_stack_dir="$work_dir/self-managed"
environment_name="grpc-proxy-event-ledger-addr-test"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "grpc-proxy-event-ledger-addr: $*" >&2
  exit 1
}

mkdir -p "$test_stack_dir"
cp -R "$stack_dir"/. "$test_stack_dir"
: >"$test_stack_dir/secrets/$environment_name-secrets.yaml"

# render_addr writes stdin as the environment file and prints the rendered
# grpc-proxy EVENT_LEDGER_ADDR, or "absent".
render_addr() {
  cat >"$test_stack_dir/environments/$environment_name.yaml"
  local values_file="$work_dir/grpc-proxy-values.yaml"
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
      --output-file-template "$values_file" >/dev/null
  yq -r '.grpcproxy.env.EVENT_LEDGER_ADDR // "absent"' "$values_file"
}

actual="$(printf '%s\n' '{}' | render_addr)"
test "$actual" = "absent" ||
  fail "grpc-proxy events must be off by default, got EVENT_LEDGER_ADDR=$actual"

actual="$(printf '%s\n' \
  'addons:' \
  '  eventLedger:' \
  '    grpcProxyEvents:' \
  '      enabled: true' | render_addr)"
expected='http://event-ledger.nvcf.svc.cluster.local:8080'
test "$actual" = "$expected" ||
  fail "expected opt-in EVENT_LEDGER_ADDR=$expected, got $actual"

actual="$(printf '%s\n' \
  'addons:' \
  '  eventLedger:' \
  '    enabled: false' \
  '    grpcProxyEvents:' \
  '      enabled: true' | render_addr)"
test "$actual" = "absent" ||
  fail "grpc-proxy events need the Event Ledger addon, got EVENT_LEDGER_ADDR=$actual"

echo "grpc-proxy-event-ledger-addr: all checks passed"
