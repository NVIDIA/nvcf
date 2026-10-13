#!/usr/bin/env bash
# Test that notary.env threads from environments/<env>.yaml through
# global.yaml.gotmpl into the notary release values, so a deployment can set or
# override any notary environment variable. The stack also computes two
# MANAGEMENT_* tracing keys, so the operator map is merged over those rather
# than replacing them, and an unset notary.env renders byte-identical to before.
set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work_dir="$(mktemp -d)"
test_stack_dir="$work_dir/self-managed"
environment_name="notary-env-wiring-test"
environment_file="$test_stack_dir/environments/$environment_name.yaml"
secrets_file="$test_stack_dir/secrets/$environment_name-secrets.yaml"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "notary-env-wiring: $*" >&2
  exit 1
}

mkdir -p "$test_stack_dir"
cp -R "$stack_dir"/. "$test_stack_dir"
printf '{}\n' >"$secrets_file"

write_environment() {
  cat >"$environment_file"
}

render_notary_values() {
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
      --selector name=notary-service \
      write-values \
      --output-file-template "$output_file"
}

assert_yaml_value() {
  local input_file="$1" expression="$2" want="$3" label="$4" got
  got="$(yq -r "$expression" "$input_file")"
  [[ "$got" == "$want" ]] ||
    fail "$label: expected $want, got ${got:-<none>}"
}

# 1. Default: notary.env unset. The computed tracing key still renders and no
#    operator key appears, so existing installs are unchanged.
write_environment <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: nvidia/nvcf
EOF

default_values="$work_dir/notary-default-values.yaml"
render_notary_values "$default_values" >/dev/null
assert_yaml_value "$default_values" '.notary.env.MANAGEMENT_TRACING_ENABLED' false \
  "default: the computed tracing key must still render when notary.env is unset"
assert_yaml_value "$default_values" '.notary.env.ASSERTION_ISSUER_URL' null \
  "default: no issuer override must be emitted when notary.env is unset"

# 2. Override: ASSERTION_ISSUER_URL and arbitrary keys from the environment file
#    reach the release, and the computed tracing key survives the merge.
write_environment <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: nvidia/nvcf
notary:
  env:
    ASSERTION_ISSUER_URL: "https://notary.example.test"
    CUSTOM_NOTARY_ENV: configured
EOF

override_values="$work_dir/notary-override-values.yaml"
render_notary_values "$override_values" >/dev/null
assert_yaml_value "$override_values" '.notary.env.ASSERTION_ISSUER_URL' https://notary.example.test \
  "override: ASSERTION_ISSUER_URL must reach the notary release values"
assert_yaml_value "$override_values" '.notary.env.CUSTOM_NOTARY_ENV' configured \
  "override: arbitrary notary env keys must reach the release values"
assert_yaml_value "$override_values" '.notary.env.MANAGEMENT_TRACING_ENABLED' false \
  "override: the computed tracing key must survive the merge"

# 3. Precedence: an operator value for a computed key wins, so the merge order is
#    operator-over-computed and not the reverse.
write_environment <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: nvidia/nvcf
notary:
  env:
    MANAGEMENT_TRACING_ENABLED: "true"
EOF

precedence_values="$work_dir/notary-precedence-values.yaml"
render_notary_values "$precedence_values" >/dev/null
assert_yaml_value "$precedence_values" '.notary.env.MANAGEMENT_TRACING_ENABLED' true \
  "precedence: an operator value must override the computed key"

echo "notary-env-wiring: OK"
