#!/usr/bin/env bash
# Test that arbitrary api.env entries reach the rendered nvcf-api values,
# override built-in entries on key conflicts, and leave unrelated built-ins
# intact. This protects worker-sidecar image pins supplied by BDD and operator
# environment files from silently falling back to chart defaults.
set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
repo_dir="$(cd "$stack_dir/../../.." && pwd)"
work_dir="$(mktemp -d)"
test_stack_dir="$work_dir/self-managed"
environment_name="api-env-wiring-test"
environment_file="$test_stack_dir/environments/$environment_name.yaml"
secrets_file="$test_stack_dir/secrets/$environment_name-secrets.yaml"
values_file="$work_dir/api-values.yaml"
manifest_file="$work_dir/api-manifest.yaml"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "api-env-wiring: $*" >&2
  exit 1
}

mkdir -p "$test_stack_dir"
cp -R "$stack_dir"/. "$test_stack_dir"
printf '{}\n' >"$secrets_file"

cat >"$environment_file" <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: nvidia/nvcf
api:
  env:
    CUSTOM_API_ENV: configured
    LITERAL_TEMPLATE_VALUE: '{{ requiredEnv "DO_NOT_EVALUATE_API_ENV" }}'
    MANAGEMENT_OTLP_TRACING_ENDPOINT: http://collector.example.test:4318/v1/traces
    NVCF_NATS_REGION_PLACEMENT_TAG: custom-dc
    NVCF_SIDECARS_LLM_ROUTER_CLIENT_IMAGE: nvcr.io/nvidia/nvcf/pylon:0.14.1
EOF

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
    --selector name=api \
    write-values \
    --output-file-template "$values_file" >/dev/null

helm template api "$repo_dir/deploy/helm/cloud-functions/nvcf-api" \
  --namespace nvcf \
  --values "$values_file" >"$manifest_file"

field_value() {
  local input_file="$1" key="$2"
  sed -n "s/^[[:space:]]*${key}:[[:space:]]*//p" "$input_file" |
    head -n1 | sed -e 's/^"//' -e 's/"$//' -e "s/^'//" -e "s/'$//"
}

assert_env() {
  local input_file="$1" key="$2" want="$3" got
  got="$(field_value "$input_file" "$key")"
  [[ "$got" == "$want" ]] ||
    fail "expected ${key}: ${want}, got: ${got:-<none>}"
}

# User-supplied values include the Pylon pin and an otherwise unknown key.
assert_env "$values_file" NVCF_SIDECARS_LLM_ROUTER_CLIENT_IMAGE nvcr.io/nvidia/nvcf/pylon:0.14.1
assert_env "$values_file" CUSTOM_API_ENV configured
assert_env "$values_file" LITERAL_TEMPLATE_VALUE '{{ requiredEnv "DO_NOT_EVALUATE_API_ENV" }}'
assert_env "$values_file" MANAGEMENT_OTLP_TRACING_ENDPOINT http://collector.example.test:4318/v1/traces

# Explicit api.env wins on collision, while unrelated built-ins remain.
assert_env "$values_file" NVCF_NATS_REGION_PLACEMENT_TAG custom-dc
assert_env "$values_file" NVCF_SIDECARS_HOSTNAME nvcr.io
assert_env "$values_file" NVCF_SIDECARS_REPOSITORY nvidia/nvcf
assert_env "$values_file" MANAGEMENT_TRACING_ENABLED false
[[ "$(grep -c '^[[:space:]]*NVCF_NATS_REGION_PLACEMENT_TAG:' "$values_file")" == 1 ]] ||
  fail "NVCF_NATS_REGION_PLACEMENT_TAG must render exactly once"

# The chart consumes the generated values and emits the same Pylon pin in its
# API environment ConfigMap, closing the stack-to-workload boundary.
grep -qF 'kind: ConfigMap' "$manifest_file" ||
  fail "nvcf-api chart did not render an environment ConfigMap"
assert_env "$manifest_file" NVCF_SIDECARS_LLM_ROUTER_CLIENT_IMAGE nvcr.io/nvidia/nvcf/pylon:0.14.1

echo "api-env-wiring: OK"
