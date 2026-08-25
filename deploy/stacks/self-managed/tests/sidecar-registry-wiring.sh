#!/usr/bin/env bash
# Test that the worker sidecar registry override threads from an environment
# file through global.yaml.gotmpl into the rendered control-plane env, and that
# leaving it unset keeps the global.image default.
#
# The NVCF_SIDECARS_* / NVCT_SIDECARS_* env values tell the control plane which
# registry host and repository to advertise to workers for the worker sidecar
# images. They default to global.image so a mirror-everything install is
# unchanged; global.sidecars.{hostname,repository} redirects only the worker
# sidecars to a separate registry/org while control-plane images stay put.
#
# One render is enough: the shared global.yaml.gotmpl emits both the NVCF and
# NVCT env blocks, so rendering nvct-api (which has no release "needs") exposes
# every key under test.
set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work_dir="$(mktemp -d)"
test_stack_dir="$work_dir/self-managed"
environment_name="sidecar-registry-wiring-test"
environment_file="$test_stack_dir/environments/$environment_name.yaml"
secrets_file="$test_stack_dir/secrets/$environment_name-secrets.yaml"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "sidecar-registry-wiring: $*" >&2
  exit 1
}

mkdir -p "$test_stack_dir"
cp -R "$stack_dir"/. "$test_stack_dir"
printf '{}\n' >"$secrets_file"

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

write_env() {
  cat >"$environment_file"
}

render_values() {
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
      --selector name=nvct-api \
      write-values \
      --output-file-template "$output_file" >/dev/null
}

# Read one scalar out of the rendered values by key, unquoted, compared
# literally so periods in a hostname are not treated as regex wildcards.
field_value() {
  local values_file="$1" key="$2"
  sed -n "s/^[[:space:]]*${key}:[[:space:]]*//p" "$values_file" |
    head -n1 | sed -e 's/^"//' -e 's/"$//'
}

assert_env() {
  local values_file="$1" key="$2" want="$3" label="$4" got
  got="$(field_value "$values_file" "$key")"
  [[ "$got" == "$want" ]] ||
    fail "$label: expected ${key}: ${want}, got: ${got:-<none>}"
}

# Every scenario checks all four keys, so assert them together.
assert_sidecars() {
  local values_file="$1" host="$2" repo="$3" label="$4"
  assert_env "$values_file" NVCF_SIDECARS_HOSTNAME "$host" "$label"
  assert_env "$values_file" NVCF_SIDECARS_REPOSITORY "$repo" "$label"
  assert_env "$values_file" NVCT_SIDECARS_HOSTNAME "$host" "$label"
  assert_env "$values_file" NVCT_SIDECARS_REPOSITORY "$repo" "$label"
}

# ---------------------------------------------------------------------------
# 1. No override — the sidecars inherit global.image
# ---------------------------------------------------------------------------
write_env <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
EOF

render_values "$work_dir/default-values.yaml"
assert_sidecars "$work_dir/default-values.yaml" nvcr.io test/nvcf "no override"

# ---------------------------------------------------------------------------
# 2. Full override — global.sidecars redirects both host and repository
# ---------------------------------------------------------------------------
write_env <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
  sidecars:
    hostname: private.nvcr.io
    repository: my-org/nvcf-core
EOF

render_values "$work_dir/override-values.yaml"
assert_sidecars "$work_dir/override-values.yaml" private.nvcr.io my-org/nvcf-core "full override"

# ---------------------------------------------------------------------------
# 3. Partial override — an unset key falls back to global.image independently
# ---------------------------------------------------------------------------
write_env <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
  sidecars:
    repository: my-org/nvcf-core
EOF

render_values "$work_dir/partial-values.yaml"
assert_sidecars "$work_dir/partial-values.yaml" nvcr.io my-org/nvcf-core "repository-only override"

# ---------------------------------------------------------------------------
# 4. Explicit empty values are treated as unset, not as an empty host/repo
# ---------------------------------------------------------------------------
write_env <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
  sidecars:
    hostname: ""
    repository: ""
EOF

render_values "$work_dir/empty-values.yaml"
assert_sidecars "$work_dir/empty-values.yaml" nvcr.io test/nvcf "explicit empty"

echo "sidecar-registry-wiring: OK"
