#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work_dir="$(mktemp -d)"
test_stack_dir="$work_dir/self-managed"
environment_name="java-tracing-env-wiring-test"
environment_file="$test_stack_dir/environments/$environment_name.yaml"
secrets_file="$test_stack_dir/secrets/$environment_name-secrets.yaml"
old_tracing_enabled_key='MANAGEMENT_TRACING_'"ENABLED"
old_tracing_endpoint_key='MANAGEMENT_OTLP_TRACING_'"ENDPOINT"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "java-tracing-env-wiring: $*" >&2
  exit 1
}

mkdir -p "$test_stack_dir"
cp -R "$stack_dir"/. "$test_stack_dir"
printf '{}\n' >"$secrets_file"

write_environment() {
  cat >"$environment_file"
}

render_release_values() {
  local release_name="$1"
  local output_file="$2"

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
      --selector "name=$release_name" \
      write-values \
      --output-file-template "$output_file"
}

assert_yaml_value() {
  local input_file="$1"
  local expression="$2"
  local expected="$3"
  local actual

  actual="$(yq ea -r "$expression" "$input_file")"
  [[ "$actual" == "$expected" ]] ||
    fail "expected $expression to equal '$expected', got '$actual'"
}

values_root() {
  case "$1" in
    api) echo api ;;
    nvct-api) echo nvctApi ;;
    ess-api) echo ess ;;
    notary-service) echo notary ;;
    sis) echo sis ;;
    *) fail "unknown release $1" ;;
  esac
}

assert_disabled_values() {
  local release_name="$1"
  local values_file="$2"
  local root
  root="$(values_root "$release_name")"

  assert_yaml_value "$values_file" \
    ".$root.env.MANAGEMENT_TRACING_EXPORT_ENABLED" false
  assert_yaml_value "$values_file" \
    ".$root.env.MANAGEMENT_OPENTELEMETRY_TRACING_EXPORT_OTLP_ENDPOINT" null
  assert_yaml_value "$values_file" \
    ".$root.env.\"$old_tracing_enabled_key\"" null
  assert_yaml_value "$values_file" \
    ".$root.env.\"$old_tracing_endpoint_key\"" null
}

assert_enabled_values() {
  local release_name="$1"
  local values_file="$2"
  local root
  root="$(values_root "$release_name")"

  assert_yaml_value "$values_file" \
    ".$root.env.MANAGEMENT_TRACING_EXPORT_ENABLED" true
  assert_yaml_value "$values_file" \
    ".$root.env.MANAGEMENT_OPENTELEMETRY_TRACING_EXPORT_OTLP_ENDPOINT" \
    http://collector.monitoring.svc.cluster.local:4317
  assert_yaml_value "$values_file" \
    ".$root.env.MANAGEMENT_OPENTELEMETRY_TRACING_EXPORT_OTLP_TRANSPORT" grpc
  assert_yaml_value "$values_file" \
    ".$root.env.\"$old_tracing_enabled_key\"" null
  assert_yaml_value "$values_file" \
    ".$root.env.\"$old_tracing_endpoint_key\"" null
}

write_environment <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
EOF

for release_name in api nvct-api ess-api notary-service sis; do
  values_file="$work_dir/$release_name-disabled.yaml"
  render_release_values "$release_name" "$values_file" >/dev/null
  assert_disabled_values "$release_name" "$values_file"
done

write_environment <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
  observability:
    tracing:
      enabled: true
      collectorProtocol: http
      collectorEndpoint: collector.monitoring.svc.cluster.local
      collectorPort: 4317
EOF

for release_name in api nvct-api ess-api notary-service sis; do
  values_file="$work_dir/$release_name-enabled.yaml"
  render_release_values "$release_name" "$values_file" >/dev/null
  assert_enabled_values "$release_name" "$values_file"
done

echo "java-tracing-env-wiring: all checks passed"
