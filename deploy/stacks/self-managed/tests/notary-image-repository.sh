#!/usr/bin/env bash
# Test that the notary release pulls its image from <global.image.repository>/nvcf-notary.
#
# The release lane publishes the notary image as nvcf-notary. The stack used to
# point at notary-service, a repository that stopped receiving tags at 1.9.4, so
# every stack release from the 1.14.0 chart bump onward referenced an image that
# does not exist and the stack package failed to resolve it.
set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work_dir="$(mktemp -d)"
test_stack_dir="$work_dir/self-managed"
environment_name="notary-image-repository-test"
environment_file="$test_stack_dir/environments/$environment_name.yaml"
secrets_file="$test_stack_dir/secrets/$environment_name-secrets.yaml"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "notary-image-repository: $*" >&2
  exit 1
}

mkdir -p "$test_stack_dir"
cp -R "$stack_dir"/. "$test_stack_dir"
printf '{}\n' >"$secrets_file"

cat >"$environment_file" <<'YAML'
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
YAML

values_file="$work_dir/notary-values.yaml"
render_log="$work_dir/write-values.log"
if ! HELMFILE_ENV="$environment_name" HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" \
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
    --output-file-template "$values_file" 2>"$render_log"; then
  cat "$render_log" >&2
  fail "helmfile could not render the notary release"
fi
test -s "$values_file" || fail "helmfile wrote no values to $values_file"

# Rendered values are normalized YAML with sorted keys, so the notary image
# block reads registry -> repository -> tag on adjacent lines.
block="$(grep -B1 -A1 -F 'repository: test/nvcf/nvcf-notary' "$values_file" || true)"
[[ -n "$block" ]] || fail "notary image repository is not <global.image.repository>/nvcf-notary"
grep -q '^[[:space:]]*registry: nvcr.io$' <<<"$block" ||
  fail "notary image registry is not global.image.registry"
if grep -qF 'repository: test/nvcf/notary-service' "$values_file"; then
  fail "notary still references the retired notary-service image repository"
fi

echo "notary-image-repository: ok"
