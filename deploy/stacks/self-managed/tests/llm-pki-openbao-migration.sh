#!/usr/bin/env bash
set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work_dir="$(mktemp -d)"
test_stack_dir="$work_dir/self-managed"
environment_name="llm-pki-openbao-migration-test"
environment_file="$test_stack_dir/environments/$environment_name.yaml"
secrets_file="$test_stack_dir/secrets/$environment_name-secrets.yaml"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "llm-pki-openbao-migration: $*" >&2
  exit 1
}

mkdir -p "$test_stack_dir"
cp -R "$stack_dir"/. "$test_stack_dir"
printf '%s\n' \
  'openbao:' \
  '  migrations:' \
  '    env:' \
  '      - name: EXISTING_SECRET_ENV' \
  '        value: preserved' \
  >"$secrets_file"
printf '%s\n' \
  'addons:' \
  '  llm:' \
  '    enabled: true' \
  '    pki:' \
  '      enabled: true' \
  '      allowedDomains: cluster.local' \
  '      dnsNames:' \
  '        - llm-request-router.nvcf.svc.cluster.local' \
  '      image:' \
  '        tag: test' \
  >"$environment_file"

values_file="$work_dir/openbao-values.yaml"
HELMFILE_ENV="$environment_name" \
  HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" \
  helmfile \
    --file "$test_stack_dir/helmfile.d/01-dependencies.yaml.gotmpl" \
    --environment default \
    --state-values-set ingress.gatewayApi.controllerNamespace=envoy-gateway-system \
    --state-values-set ingress.gatewayApi.gateways.shared.name=shared-gw \
    --state-values-set ingress.gatewayApi.gateways.shared.namespace=envoy-gateway-system \
    --state-values-set ingress.gatewayApi.gateways.grpc.name=grpc-gw \
    --state-values-set ingress.gatewayApi.gateways.grpc.namespace=envoy-gateway-system \
    --selector name=openbao-server \
    write-values \
    --output-file-template "$values_file"

environment_value() {
  local name="$1"
  yq -r ".openbao.migrations.env[]? | select(.name == \"$name\") | .value" "$values_file"
}

actual="$(environment_value ADDONS_LLM_ENABLED)"
test "$actual" = "true" ||
  fail "expected ADDONS_LLM_ENABLED=true in the OpenBao migration environment, got ${actual:-missing}"

actual="$(environment_value NVCF_SERVICE_PKI_ALLOWED_DOMAINS)"
test "$actual" = "cluster.local" ||
  fail "expected NVCF_SERVICE_PKI_ALLOWED_DOMAINS=cluster.local in the OpenBao migration environment, got ${actual:-missing}"

actual="$(environment_value EXISTING_SECRET_ENV)"
test "$actual" = "preserved" ||
  fail "expected existing migration environment entries to be preserved, got ${actual:-missing}"

echo "llm-pki-openbao-migration: all checks passed"
