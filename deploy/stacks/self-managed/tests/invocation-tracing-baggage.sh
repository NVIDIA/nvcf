#!/usr/bin/env bash
set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
repo_root="$(cd "$stack_dir/../../.." && pwd)"
chart_dir="$repo_root/deploy/helm/http-invocation/nvcf-invocation-service"
work_dir="$(mktemp -d)"
test_stack_dir="$work_dir/self-managed"
environment_name="invocation-tracing-baggage-test"
environment_file="$test_stack_dir/environments/$environment_name.yaml"
secrets_file="$test_stack_dir/secrets/$environment_name-secrets.yaml"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "invocation-tracing-baggage: $*" >&2
  exit 1
}

mkdir -p "$test_stack_dir"
cp -R "$stack_dir"/. "$test_stack_dir"
printf '{}\n' >"$secrets_file"

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

render_chart() {
  local values_file="$1"
  local output_file="$2"

  helm template invocation-service "$chart_dir" \
    --namespace nvcf \
    --values "$values_file" >"$output_file"
}

assert_yq_eq() {
  local file="$1"
  local expression="$2"
  local expected="$3"
  local actual

  actual="$(yq ea -r "$expression" "$file")"
  if [ "$actual" != "$expected" ]; then
    fail "expected $expression to equal '$expected', got '$actual'"
  fi
}

write_default_environment() {
  cat >"$environment_file" <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
EOF
}

write_configured_environment() {
  local second_key="$1"

  cat >"$environment_file" <<EOF
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
invocation:
  tracing:
    baggageAttributeAllowlist:
      - allowed.one
      - $second_key
EOF
}

default_values="$work_dir/default-values.yaml"
configured_values="$work_dir/configured-values.yaml"
changed_values="$work_dir/changed-values.yaml"
default_render="$work_dir/default-render.yaml"
configured_render="$work_dir/configured-render.yaml"
changed_render="$work_dir/changed-render.yaml"

write_default_environment
render_invocation_values "$default_values" >/dev/null
render_chart "$default_values" "$default_render"
assert_yq_eq \
  "$default_render" \
  'select(.kind == "ConfigMap" and .metadata.name == "invocation-service-env") | .data."SERVER__TRACING__BAGGAGE_ATTRIBUTE_ALLOWLIST" // "absent"' \
  absent

write_configured_environment allowed.two
render_invocation_values "$configured_values" >/dev/null
assert_yq_eq \
  "$configured_values" \
  '.invocation.tracing.baggageAttributeAllowlist | join(",")' \
  allowed.one,allowed.two
render_chart "$configured_values" "$configured_render"
assert_yq_eq \
  "$configured_render" \
  'select(.kind == "ConfigMap" and .metadata.name == "invocation-service-env") | .data."SERVER__TRACING__BAGGAGE_ATTRIBUTE_ALLOWLIST"' \
  allowed.one,allowed.two

write_configured_environment allowed.three
render_invocation_values "$changed_values" >/dev/null
render_chart "$changed_values" "$changed_render"

configured_checksum="$(yq ea -r 'select(.kind == "Deployment" and .metadata.name == "invocation-service") | .spec.template.metadata.annotations."checksum/config-env"' "$configured_render")"
changed_checksum="$(yq ea -r 'select(.kind == "Deployment" and .metadata.name == "invocation-service") | .spec.template.metadata.annotations."checksum/config-env"' "$changed_render")"
if [ -z "$configured_checksum" ] || [ "$configured_checksum" = null ]; then
  fail "configured baggage allowlist did not render a deployment checksum"
fi
if [ "$configured_checksum" = "$changed_checksum" ]; then
  fail "deployment checksum did not change when the baggage allowlist changed"
fi

echo "invocation-tracing-baggage: all checks passed"
