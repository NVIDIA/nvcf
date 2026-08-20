#!/usr/bin/env bash
# Test that the supporting-image overrides thread from an environment file
# through global.yaml.gotmpl into the rendered chart values, and that leaving
# them unset keeps the mirrored <global.image.repository>/<name> default.
#
# The NATS config reloader and the account-bootstrap alpine-k8s image are not
# republished under the public nvidia/nvcf catalog, so a public-catalog install
# has to redirect them to Docker Hub without editing the template.
set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work_dir="$(mktemp -d)"
test_stack_dir="$work_dir/self-managed"
environment_name="image-override-wiring-test"
environment_file="$test_stack_dir/environments/$environment_name.yaml"
secrets_file="$test_stack_dir/secrets/$environment_name-secrets.yaml"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "image-override-wiring: $*" >&2
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
      --file "$test_stack_dir/helmfile.d/01-dependencies.yaml.gotmpl" \
      --environment default \
      --state-values-set ingress.gatewayApi.controllerNamespace=envoy-gateway-system \
      --state-values-set ingress.gatewayApi.gateways.shared.name=shared-gw \
      --state-values-set ingress.gatewayApi.gateways.shared.namespace=envoy-gateway-system \
      --state-values-set ingress.gatewayApi.gateways.grpc.name=grpc-gw \
      --state-values-set ingress.gatewayApi.gateways.grpc.namespace=envoy-gateway-system \
      --selector name=nats \
      write-values \
      --output-file-template "$output_file" >/dev/null
}

# Rendered values are normalized YAML with alphabetically sorted keys, so an
# image block always reads registry -> repository -> tag.
assert_image() {
  local values_file="$1" repository="$2" registry="$3" tag="$4" label="$5"
  local block

  block="$(grep -B1 -A1 -F "repository: $repository" "$values_file" || true)"
  [[ -n "$block" ]] || fail "$label: repository: $repository is not in the rendered values"
  grep -qF "registry: $registry" <<<"$block" ||
    fail "$label: expected registry: $registry beside repository: $repository, got:"$'\n'"$block"
  if [[ -n "$tag" ]]; then
    grep -qE "tag: \"?${tag}\"?\$" <<<"$block" ||
      fail "$label: expected tag: $tag beside repository: $repository, got:"$'\n'"$block"
  fi
}

assert_absent() {
  local values_file="$1" repository="$2" label="$3"
  grep -qF "repository: $repository" "$values_file" &&
    fail "$label: repository: $repository should not be in the rendered values"
  return 0
}

# ---------------------------------------------------------------------------
# 1. No overrides — every image keeps the mirrored global.image default
# ---------------------------------------------------------------------------
write_env <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
EOF

render_values "$work_dir/default-values.yaml"
assert_image "$work_dir/default-values.yaml" \
  test/nvcf/nats-server-config-reloader nvcr.io 0.23.0 "nats.reloader default"
assert_image "$work_dir/default-values.yaml" \
  test/nvcf/alpine-k8s nvcr.io 1.36.1 "api.accountBootstrap default"
assert_image "$work_dir/default-values.yaml" \
  test/nvcf/cassandra nvcr.io "" "cassandra default"

# ---------------------------------------------------------------------------
# 2. Public-catalog install — redirect upstream-only images to Docker Hub
# ---------------------------------------------------------------------------
write_env <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: nvidia/nvcf
nats:
  reloader:
    image:
      registry: docker.io
      repository: natsio/nats-server-config-reloader
      tag: "0.23.0"
api:
  accountBootstrap:
    image:
      registry: docker.io
      repository: alpine/k8s
      tag: "1.36.1"
EOF

render_values "$work_dir/upstream-values.yaml"
assert_image "$work_dir/upstream-values.yaml" \
  natsio/nats-server-config-reloader docker.io 0.23.0 "nats.reloader upstream"
assert_image "$work_dir/upstream-values.yaml" \
  alpine/k8s docker.io 1.36.1 "api.accountBootstrap upstream"
assert_absent "$work_dir/upstream-values.yaml" \
  nvidia/nvcf/nats-server-config-reloader "nats.reloader upstream"
# alpine-k8s is intentionally still resolved from global.image.repository for
# cassandra.initialization and nats.nkeyJob; those values are inert today.
# The Cassandra server keeps the public-catalog default without an override.
assert_image "$work_dir/upstream-values.yaml" \
  nvidia/nvcf/cassandra nvcr.io "" "cassandra public catalog default"

# ---------------------------------------------------------------------------
# 3. Partial override — unset keys fall back to their defaults
# ---------------------------------------------------------------------------
write_env <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
nats:
  reloader:
    image:
      repository: mirror/nats-server-config-reloader
cassandra:
  image:
    tag: "5.0.8-nv-2.0.1"
EOF

render_values "$work_dir/partial-values.yaml"
assert_image "$work_dir/partial-values.yaml" \
  mirror/nats-server-config-reloader nvcr.io 0.23.0 "nats.reloader partial"
assert_image "$work_dir/partial-values.yaml" \
  test/nvcf/cassandra nvcr.io 5.0.8-nv-2.0.1 "cassandra tag-only override"

echo "image-override-wiring: OK"
