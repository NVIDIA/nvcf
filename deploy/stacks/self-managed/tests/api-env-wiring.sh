#!/usr/bin/env bash
# Test that the stack owns the API remote-config contract for the Pylon worker
# sidecar while preserving arbitrary API environment and remote-config values.
# The deprecated env key is translated into remote config for one compatibility
# window, omitted from the API env ConfigMap, and rejected when it conflicts
# with the chart-native remote-config property.
set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
repo_dir="$(cd "$stack_dir/../../.." && pwd)"
work_dir="$(mktemp -d)"
test_stack_dir="$work_dir/self-managed"
environment_name="api-env-wiring-test"
environment_file="$test_stack_dir/environments/$environment_name.yaml"
secrets_file="$test_stack_dir/secrets/$environment_name-secrets.yaml"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "api-env-wiring: $*" >&2
  exit 1
}

mkdir -p "$test_stack_dir"
cp -R "$stack_dir"/. "$test_stack_dir"
printf '{}\n' >"$secrets_file"

write_environment() {
  cat >"$environment_file"
}

render_api_values() {
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
      --selector name=api \
      write-values \
      --output-file-template "$output_file"
}

render_api_manifest() {
  local values_file="$1" manifest_file="$2"

  helm template api "$repo_dir/deploy/helm/cloud-functions/nvcf-api" \
    --namespace nvcf \
    --values "$values_file" >"$manifest_file"
}

assert_yaml_value() {
  local input_file="$1" expression="$2" want="$3" label="$4" got
  got="$(yq ea -r "$expression" "$input_file")"
  [[ "$got" == "$want" ]] ||
    fail "$label: expected $want, got ${got:-<none>}"
}

assert_env_absent() {
  local values_file="$1" key="$2" label="$3"
  [[ "$(yq -r ".api.env.\"$key\"" "$values_file")" == null ]] ||
    fail "$label: $key must not reach rendered API env"
}

assert_render_failure() {
  local output_file="$1" expected_error="$2" label="$3"
  local log_file="${output_file%.yaml}.log"

  if render_api_values "$output_file" >"$log_file" 2>&1; then
    fail "$label was accepted"
  fi
  grep -Fq "$expected_error" "$log_file" ||
    fail "$label did not return the expected error"
}

remote_pylon_expression='.api.remoteConfig.configData.nvcf.sidecars."llm-router-client-image"'
remote_worker_address_expression='.api.remoteConfig.configData.nvcf."llm-request-router"."worker-address"'
manifest_pylon_expression='select(.kind == "ConfigMap" and .data."nvcf-api.yaml" != null) | .data."nvcf-api.yaml" | from_yaml | .nvcf.sidecars."llm-router-client-image"'

# The public chart-native path overrides the computed default. Arbitrary remote
# config and API env survive, while the stack-owned worker address wins.
write_environment <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: nvidia/nvcf
addons:
  llm:
    enabled: true
api:
  remoteConfig:
    configData:
      custom:
        retained: keep-me
      nvcf:
        sidecars:
          llm-router-client-image: mirror.example.test/team/pylon:9.9.9
          retained-setting: keep-inside-sidecars
        llm-request-router:
          worker-address: wrong-owner.example.test:1
  env:
    CUSTOM_API_ENV: configured
    LITERAL_TEMPLATE_VALUE: '{{ requiredEnv "DO_NOT_EVALUATE_API_ENV" }}'
    NVCF_NATS_REGION_PLACEMENT_TAG: custom-dc
EOF

explicit_values="$work_dir/explicit-values.yaml"
explicit_manifest="$work_dir/explicit-manifest.yaml"
render_api_values "$explicit_values" >/dev/null
render_api_manifest "$explicit_values" "$explicit_manifest"
assert_yaml_value "$explicit_values" "$remote_pylon_expression" \
  mirror.example.test/team/pylon:9.9.9 "chart-native Pylon override"
assert_yaml_value "$explicit_values" '.api.remoteConfig.configData.custom.retained' \
  keep-me "generic remote config"
assert_yaml_value "$explicit_values" '.api.remoteConfig.configData.nvcf.sidecars.retained-setting' \
  keep-inside-sidecars "nested sidecar remote config"
assert_yaml_value "$explicit_values" "$remote_worker_address_expression" \
  llm-request-router.nvcf.svc.cluster.local:50071 "stack-owned worker address"
assert_yaml_value "$explicit_values" '.api.env.CUSTOM_API_ENV' \
  configured "generic API env"
assert_yaml_value "$explicit_values" '.api.env.LITERAL_TEMPLATE_VALUE' \
  '{{ requiredEnv "DO_NOT_EVALUATE_API_ENV" }}' "literal API env"
assert_yaml_value "$explicit_values" '.api.env.NVCF_NATS_REGION_PLACEMENT_TAG' \
  custom-dc "API env override precedence"
assert_yaml_value "$explicit_values" '.api.env.NVCF_SIDECARS_HOSTNAME' \
  nvcr.io "built-in sidecar hostname"
assert_yaml_value "$explicit_values" '.api.env.NVCF_SIDECARS_REPOSITORY' \
  nvidia/nvcf "built-in sidecar repository"
assert_env_absent "$explicit_values" NVCF_SIDECARS_LLM_ROUTER_CLIENT_IMAGE \
  "chart-native Pylon override"
assert_yaml_value "$explicit_manifest" "$manifest_pylon_expression" \
  mirror.example.test/team/pylon:9.9.9 "rendered API remote ConfigMap"
assert_yaml_value "$explicit_manifest" \
  'select(.kind == "ConfigMap" and .data."nvcf-api.yaml" != null) | .data."nvcf-api.yaml" | from_yaml | .nvcf.sidecars.retained-setting' \
  keep-inside-sidecars "rendered nested sidecar remote config"

# With no explicit value, the enabled LLM addon derives Pylon 0.14.1 from the
# effective worker-sidecar registry rather than the control-plane image path.
write_environment <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: nvidia/nvcf
  sidecars:
    hostname: registry.example.test
    repository: team/sidecars
addons:
  llm:
    enabled: true
EOF

default_values="$work_dir/default-values.yaml"
render_api_values "$default_values" >/dev/null
assert_yaml_value "$default_values" "$remote_pylon_expression" \
  registry.example.test/team/sidecars/pylon:0.14.1 "computed Pylon default"

# Local BDD fixtures rely on the same computed default after their registry
# paths are configured. They must not carry unresolved fixture placeholders
# into the higher-precedence API remote ConfigMap.
for fixture_name in self-managed-local-bdd.yaml self-managed-local-bdd-multi.yaml; do
  cp "$repo_dir/tests/bdd/fixtures/$fixture_name" "$environment_file"
  yq -i \
    '.global.helm.sources.repository = "sample-org/sample-team" |
     .global.image.repository = "sample-org/sample-team"' \
    "$environment_file"

  fixture_values="$work_dir/${fixture_name%.yaml}-values.yaml"
  render_api_values "$fixture_values" >/dev/null
  fixture_pylon_image="$(yq -r "$remote_pylon_expression" "$fixture_values")"
  [[ "$fixture_pylon_image" != *REPLACE_WITH_* ]] ||
    fail "$fixture_name Pylon property retained an unresolved fixture placeholder"
  [[ "$fixture_pylon_image" == nvcr.io/sample-org/sample-team/pylon:0.14.1 ]] ||
    fail "$fixture_name Pylon property: expected computed 0.14.1 image, got $fixture_pylon_image"
done

# The deprecated env key remains accepted for one compatibility window, but
# translates into remote config and never reaches the API environment.
write_environment <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: nvidia/nvcf
api:
  env:
    CUSTOM_API_ENV: legacy-compatible
    NVCF_SIDECARS_LLM_ROUTER_CLIENT_IMAGE: legacy.example.test/team/pylon:0.14.1
EOF

legacy_values="$work_dir/legacy-values.yaml"
render_api_values "$legacy_values" >/dev/null
assert_yaml_value "$legacy_values" "$remote_pylon_expression" \
  legacy.example.test/team/pylon:0.14.1 "legacy Pylon translation"
assert_yaml_value "$legacy_values" '.api.env.CUSTOM_API_ENV' \
  legacy-compatible "legacy generic API env"
assert_env_absent "$legacy_values" NVCF_SIDECARS_LLM_ROUTER_CLIENT_IMAGE \
  "legacy Pylon translation"

# Supplying the same value through both paths is compatible during the
# deprecation window. The legacy env key is still omitted.
write_environment <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: nvidia/nvcf
api:
  remoteConfig:
    configData:
      nvcf:
        sidecars:
          llm-router-client-image: compatible.example.test/team/pylon:0.14.1
  env:
    NVCF_SIDECARS_LLM_ROUTER_CLIENT_IMAGE: compatible.example.test/team/pylon:0.14.1
EOF

compatible_values="$work_dir/compatible-values.yaml"
render_api_values "$compatible_values" >/dev/null
assert_yaml_value "$compatible_values" "$remote_pylon_expression" \
  compatible.example.test/team/pylon:0.14.1 "same-value Pylon compatibility"
assert_env_absent "$compatible_values" NVCF_SIDECARS_LLM_ROUTER_CLIENT_IMAGE \
  "same-value Pylon compatibility"

# Explicit empty values cannot suppress the safe default or create an invalid
# worker-sidecar reference in higher-precedence remote config.
write_environment <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: nvidia/nvcf
api:
  remoteConfig:
    configData:
      nvcf:
        sidecars:
          llm-router-client-image: "   "
EOF

canonical_empty_error='api.remoteConfig.configData.nvcf.sidecars.llm-router-client-image must be a non-empty string'
assert_render_failure "$work_dir/canonical-empty-values.yaml" "$canonical_empty_error" \
  "empty chart-native Pylon value"

write_environment <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: nvidia/nvcf
api:
  env:
    NVCF_SIDECARS_LLM_ROUTER_CLIENT_IMAGE: ""
EOF

legacy_empty_error='api.env.NVCF_SIDECARS_LLM_ROUTER_CLIENT_IMAGE must be a non-empty string'
assert_render_failure "$work_dir/legacy-empty-values.yaml" "$legacy_empty_error" \
  "empty legacy Pylon value"

# Supplying conflicting legacy and chart-native values is ambiguous and fails
# before Helm receives the generated values.
write_environment <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: nvidia/nvcf
addons:
  llm:
    enabled: true
api:
  remoteConfig:
    configData:
      nvcf:
        sidecars:
          llm-router-client-image: new.example.test/team/pylon:0.14.1
  env:
    NVCF_SIDECARS_LLM_ROUTER_CLIENT_IMAGE: legacy.example.test/team/pylon:0.14.1
EOF

conflict_error='api.env.NVCF_SIDECARS_LLM_ROUTER_CLIENT_IMAGE is deprecated and conflicts with api.remoteConfig.configData.nvcf.sidecars.llm-router-client-image'
assert_render_failure "$work_dir/conflict-values.yaml" "$conflict_error" \
  "conflicting legacy and chart-native Pylon values"

echo "api-env-wiring: OK"
