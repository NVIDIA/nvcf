#!/usr/bin/env bash
# Test that apikeys.env threads from environments/<env>.yaml through
# global.yaml.gotmpl into the api-keys release values. NVCF_NCA_ID is
# operator-specific config (the NCA the api-keys service authorizes keys
# against) and must be settable from the plain env file, not only the secrets
# file. When apikeys.env is unset the stack emits no override, so the chart
# default env (AWS_REGION / SPRING_PROFILES_ACTIVE) stands and existing installs
# render byte-identical.
set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work_dir="$(mktemp -d)"
test_stack_dir="$work_dir/self-managed"
environment_name="apikeys-env-wiring-test"
environment_file="$test_stack_dir/environments/$environment_name.yaml"
secrets_file="$test_stack_dir/secrets/$environment_name-secrets.yaml"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "apikeys-env-wiring: $*" >&2
  exit 1
}

mkdir -p "$test_stack_dir"
cp -R "$stack_dir"/. "$test_stack_dir"
printf '{}\n' >"$secrets_file"

write_environment() {
  cat >"$environment_file"
}

render_apikeys_values() {
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
      --selector name=api-keys \
      write-values \
      --output-file-template "$output_file"
}

assert_yaml_value() {
  local input_file="$1" expression="$2" want="$3" label="$4" got
  got="$(yq -r "$expression" "$input_file")"
  [[ "$got" == "$want" ]] ||
    fail "$label: expected $want, got ${got:-<none>}"
}

# 1. Default: apikeys.env unset. The stack forwards no env override, so the
#    api-keys chart default env stands and nothing is emitted for the release.
write_environment <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: nvidia/nvcf
EOF

default_values="$work_dir/apikeys-default-values.yaml"
render_apikeys_values "$default_values" >/dev/null
assert_yaml_value "$default_values" '.apikeys.env' null \
  "default: apikeys.env must not be emitted when unset in the env file"

# 2. Override: NVCF_NCA_ID in environments/<env>.yaml reaches the release as
#    apikeys.env, alongside any other operator-supplied keys. Helm deep-merges
#    this over the chart default env at template time.
write_environment <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: nvidia/nvcf
apikeys:
  env:
    NVCF_NCA_ID: "nca-test-1234"
    CUSTOM_APIKEYS_ENV: configured
EOF

override_values="$work_dir/apikeys-override-values.yaml"
render_apikeys_values "$override_values" >/dev/null
assert_yaml_value "$override_values" '.apikeys.env.NVCF_NCA_ID' nca-test-1234 \
  "override: NVCF_NCA_ID must reach the api-keys release values"
assert_yaml_value "$override_values" '.apikeys.env.CUSTOM_APIKEYS_ENV' configured \
  "override: arbitrary api-keys env keys must reach the release values"

# Resource overrides follow the same environment-to-chart path. Unset values
# must not replace chart defaults, and partial values remain partial so Helm
# retains the chart's other requests and limits.
assert_yaml_value "$default_values" '.apikeys.resources' null \
  "default: chart resources must remain in control"

write_environment <<'EOF'
apikeys:
  resources:
    requests:
      cpu: 350m
      memory: 768Mi
    limits:
      cpu: "2"
      memory: 1536Mi
EOF
resource_values="$work_dir/apikeys-resource-values.yaml"
render_apikeys_values "$resource_values" >/dev/null
assert_yaml_value "$resource_values" '.apikeys.resources.requests.cpu' 350m "CPU request"
assert_yaml_value "$resource_values" '.apikeys.resources.requests.memory' 768Mi "memory request"
assert_yaml_value "$resource_values" '.apikeys.resources.limits.cpu' 2 "CPU limit"
assert_yaml_value "$resource_values" '.apikeys.resources.limits.memory' 1536Mi "memory limit"

write_environment <<'EOF'
apikeys:
  resources:
    limits:
      cpu: "1"
EOF
partial_values="$work_dir/apikeys-partial-resource-values.yaml"
render_apikeys_values "$partial_values" >/dev/null
assert_yaml_value "$partial_values" '.apikeys.resources.limits.cpu' 1 "partial CPU limit"
assert_yaml_value "$partial_values" '.apikeys.resources.requests' null "chart requests preserved"
assert_yaml_value "$partial_values" '.apikeys.resources.limits.memory' null "chart memory limit preserved"

echo "apikeys-env-wiring: OK"
