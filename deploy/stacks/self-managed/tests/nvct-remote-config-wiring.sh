#!/usr/bin/env bash
# Test that nvctApi.remoteConfig.configData reaches the NVCT API chart. The stack
# forwarded no remote config for NVCT, so the chart's packaged defaults were the
# only values the service could see -- and the otel sidecar images ship as
# placeholders. An env var cannot substitute: the remote-config ConfigMap loads
# through the bootstrap context and outranks the environment.
#
# global.yaml.gotmpl emits every top-level block into any release it renders, so
# the api release exposes nvctApi: for assertion. The chart render uses the
# nvct-api release so the values carry the chart defaults to merge against.
set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
chart_dir="$stack_dir/../../helm/cloud-tasks/nvct-api"
work_dir="$(mktemp -d)"
test_stack_dir="$work_dir/self-managed"
environment_name="nvct-remote-config-wiring-test"
environment_file="$test_stack_dir/environments/$environment_name.yaml"
secrets_file="$test_stack_dir/secrets/$environment_name-secrets.yaml"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "nvct-remote-config-wiring: $*" >&2
  exit 1
}

mkdir -p "$test_stack_dir"
cp -R "$stack_dir"/. "$test_stack_dir"
printf '{}\n' >"$secrets_file"

write_environment() {
  cat >"$environment_file"
}

# Leaves helmfile output in write-values.log so negative cases can assert on it.
render_values() {
  local output_file="$1"
  local selector="$2"
  HELMFILE_ENV="$environment_name" HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" \
    helmfile \
      --file "$test_stack_dir/helmfile.d/02-core.yaml.gotmpl" \
      --environment default \
      --state-values-set ingress.gatewayApi.controllerNamespace=envoy-gateway-system \
      --state-values-set ingress.gatewayApi.gateways.shared.name=shared-gw \
      --state-values-set ingress.gatewayApi.gateways.shared.namespace=envoy-gateway-system \
      --state-values-set ingress.gatewayApi.gateways.grpc.name=grpc-gw \
      --state-values-set ingress.gatewayApi.gateways.grpc.namespace=envoy-gateway-system \
      --selector "name=$selector" \
      write-values \
      --output-file-template "$output_file" >"$work_dir/write-values.log" 2>&1
}

render_values_or_fail() {
  local output_file="$1"
  local selector="$2"
  if ! render_values "$output_file" "$selector"; then
    cat "$work_dir/write-values.log" >&2
    fail "helmfile could not render the stack"
  fi
  test -s "$output_file" || fail "helmfile wrote no values to $output_file"
}

assert_value() {
  local file="$1" expression="$2" want="$3" label="$4" got
  got="$(yq -r "$expression" "$file")"
  [[ "$got" == "$want" ]] ||
    fail "$label: expected $want, got ${got:-<none>}"
}

# The ConfigMap holds the whole profile as one YAML string, so parse it twice.
read_remote_config_key() {
  local manifest="$1" expression="$2"
  yq ea -r \
    'select(.kind == "ConfigMap" and .metadata.name == "nvct-api-remote-config")
      | .data["nvct-api.yaml"]' "$manifest" |
    yq -r "$expression" -
}

assert_remote_config_key() {
  local manifest="$1" expression="$2" want="$3" label="$4" got
  got="$(read_remote_config_key "$manifest" "$expression")"
  [[ "$got" == "$want" ]] ||
    fail "$label: expected $want, got ${got:-<none>}"
}

otel_image='${nvct.sidecars.hostname}/${nvct.sidecars.repository}/otel-collector:9.9.9'

# ---------------------------------------------------------------------------
# 1. No override: the stack emits nothing, leaving the chart defaults in place.
# ---------------------------------------------------------------------------
write_environment <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
EOF

default_values="$work_dir/default-values.yaml"
render_values_or_fail "$default_values" api
assert_value "$default_values" '.nvctApi.remoteConfig' null \
  "default: no nvctApi remote config is forwarded"

# ---------------------------------------------------------------------------
# 2. Override reaches the chart verbatim, ${...} placeholders included.
# ---------------------------------------------------------------------------
write_environment <<EOF
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
nvctApi:
  remoteConfig:
    configData:
      nvct:
        sidecars:
          otel-container: "$otel_image"
EOF

override_values="$work_dir/override-values.yaml"
render_values_or_fail "$override_values" api
assert_value "$override_values" \
  '.nvctApi.remoteConfig.configData.nvct.sidecars.otel-container' "$otel_image" \
  "override: otel-container is forwarded to the chart"
assert_value "$override_values" '.api.remoteConfig' null \
  "override: nvctApi remote config does not leak into the API chart"

# ---------------------------------------------------------------------------
# 3. Helm merges per key, so sibling chart defaults must survive the override.
# ---------------------------------------------------------------------------
nvct_values="$work_dir/nvct-values.yaml"
render_values_or_fail "$nvct_values" nvct-api

manifest="$work_dir/nvct-api.yaml"
helm template nvct-api "$chart_dir" \
  --namespace nvcf \
  --values "$nvct_values" >"$manifest" ||
  fail "helm template could not render the nvct-api chart"

assert_remote_config_key "$manifest" '.nvct.sidecars."otel-container"' "$otel_image" \
  "merge: the override reaches the rendered ConfigMap"

# Expected values come from the chart so a version bump there does not break this.
for sidecar_key in init-container utils-container ess-agent-container; do
  chart_default="$(yq -r \
    ".nvctApi.remoteConfig.configData.nvct.sidecars.\"$sidecar_key\"" \
    "$chart_dir/values.yaml")"
  [[ -n "$chart_default" && "$chart_default" != "null" ]] ||
    fail "merge: the chart no longer defaults $sidecar_key, update this test"
  assert_remote_config_key "$manifest" ".nvct.sidecars.\"$sidecar_key\"" \
    "$chart_default" "merge: the chart default for $sidecar_key survives"
done

# ---------------------------------------------------------------------------
# 4. A non-map configData fails the render instead of producing an unparseable
#    ConfigMap.
# ---------------------------------------------------------------------------
write_environment <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
nvctApi:
  remoteConfig:
    configData: "not-a-map"
EOF

if render_values "$work_dir/invalid-values.yaml" api; then
  fail "a string nvctApi.remoteConfig.configData was accepted"
fi
grep -Fq 'nvctApi.remoteConfig.configData must be a map' "$work_dir/write-values.log" ||
  fail "a string nvctApi.remoteConfig.configData did not return the expected error"

echo "nvct-remote-config-wiring: OK"
