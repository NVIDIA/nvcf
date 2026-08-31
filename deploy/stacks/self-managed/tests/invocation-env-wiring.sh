#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work_dir="$(mktemp -d)"
test_stack_dir="$work_dir/self-managed"
environment_name="invocation-env-wiring-test"
environment_file="$test_stack_dir/environments/$environment_name.yaml"
secrets_file="$test_stack_dir/secrets/$environment_name-secrets.yaml"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "invocation-env-wiring: $*" >&2
  exit 1
}

mkdir -p "$test_stack_dir"
cp -R "$stack_dir"/. "$test_stack_dir"
printf '{}\n' >"$secrets_file"

write_environment() {
  cat >"$environment_file"
}

render_invocation_values() {
  local output_file="$1"

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
      --selector name=invocation-service \
      write-values \
      --output-file-template "$output_file"
}

assert_yq_eq() {
  local file="$1"
  local expression="$2"
  local expected="$3"
  local actual

  actual="$(yq ea -r "$expression" "$file")"
  [[ "$actual" == "$expected" ]] ||
    fail "expected $expression to equal '$expected', got '$actual'"
}

default_values="$work_dir/default-values.yaml"
custom_values="$work_dir/custom-values.yaml"
invalid_values="$work_dir/invalid-values.yaml"
invalid_log="$work_dir/invalid.log"

write_environment <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
EOF

render_invocation_values "$default_values" >/dev/null
assert_yq_eq \
  "$default_values" \
  '.invocation.env.NATS_PROPERTIES__REGION' \
  ncp
assert_yq_eq \
  "$default_values" \
  '.invocation.env.RATE_LIMIT_ENABLED' \
  true
assert_yq_eq \
  "$default_values" \
  '.invocation.env.SERVER__ENVFILTER_DIRECTIVE' \
  otel::tracing=off,otel=off,server=trace,info

write_environment <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
invocation:
  env:
    CUSTOM_INVOCATION_ENV: retained
    NATS_PROPERTIES__REGION: example-region
    SERVER__ENVFILTER_DIRECTIVE: custom-filter
EOF

render_invocation_values "$custom_values" >/dev/null
assert_yq_eq \
  "$custom_values" \
  '.invocation.env.CUSTOM_INVOCATION_ENV' \
  retained
assert_yq_eq \
  "$custom_values" \
  '.invocation.env.NATS_PROPERTIES__REGION' \
  example-region
assert_yq_eq \
  "$custom_values" \
  '.invocation.env.SERVER__ENVFILTER_DIRECTIVE' \
  custom-filter
assert_yq_eq \
  "$custom_values" \
  '.invocation.env.RATE_LIMIT_ADDRESS' \
  http://ratelimiter.nvcf.svc.cluster.local:7777

write_environment <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
invocation:
  env: invalid
EOF

if render_invocation_values "$invalid_values" >"$invalid_log" 2>&1; then
  fail "expected a non-map invocation.env value to fail"
fi
grep -Fq 'invocation.env must be a map' "$invalid_log" ||
  fail "missing invocation.env validation error"

echo "invocation-env-wiring: all checks passed"
