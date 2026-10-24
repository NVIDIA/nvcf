#!/usr/bin/env bash
# Test that the NVCF UI OpenBao migrations image tag is a supported SemVer and
# a valid OCI image tag before the stack passes it to the chart.
set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work_dir="$(mktemp -d)"
test_stack_dir="$work_dir/self-managed"
environment_name="nvcf-ui-migrations-image-tag-test"
environment_file="$test_stack_dir/environments/$environment_name.yaml"
secrets_file="$test_stack_dir/secrets/$environment_name-secrets.yaml"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "nvcf-ui-migrations-image-tag: $*" >&2
  exit 1
}

mkdir -p "$test_stack_dir"
cp -R "$stack_dir"/. "$test_stack_dir"
printf '{}\n' >"$secrets_file"

write_env() {
  local tag="$1"

  cat >"$environment_file" <<YAML
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
addons:
  nvcfUi:
    enabled: true
    openbaoMigrations:
      image:
        tag: "$tag"
YAML
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
      --selector name=nvcf-ui \
      write-values \
      --output-file-template "$output_file"
}

assert_tag() {
  local values_file="$1" expected="$2"

  yq -e ".nvcfUi.openbaoMigrations.image.tag == \"$expected\"" "$values_file" >/dev/null 2>&1 ||
    fail "expected migrations image tag $expected"
}

write_env "0.19.6"
render_values "$work_dir/newer-values.yaml" >/dev/null
assert_tag "$work_dir/newer-values.yaml" "0.19.6"

write_env "0.19.4"
render_values "$work_dir/older-values.yaml" >/dev/null
assert_tag "$work_dir/older-values.yaml" "0.19.5"

write_env "0.19.5+build.1"
if render_values "$work_dir/invalid-values.yaml" >"$work_dir/invalid.log" 2>&1; then
  fail "accepted a SemVer build-metadata suffix that is invalid in an OCI image tag"
fi
grep -qF 'must be a valid OCI image tag' "$work_dir/invalid.log" || {
  cat "$work_dir/invalid.log" >&2
  fail "invalid OCI image tag did not produce the expected validation error"
}

echo "nvcf-ui-migrations-image-tag: ok"
