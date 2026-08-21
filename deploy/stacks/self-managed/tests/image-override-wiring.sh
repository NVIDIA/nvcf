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

  local actual_registry
  actual_registry="$(field_value registry <<<"$block")"
  [[ "$actual_registry" == "$registry" ]] ||
    fail "$label: expected registry: $registry beside repository: $repository, got: ${actual_registry:-<none>}"

  if [[ -n "$tag" ]]; then
    local actual_tag
    actual_tag="$(field_value tag <<<"$block")"
    [[ "$actual_tag" == "$tag" ]] ||
      fail "$label: expected tag: $tag beside repository: $repository, got: ${actual_tag:-<none>}"
  fi
}

# Read one scalar out of a rendered block, unquoted, compared literally so that
# periods in a version are not treated as regex wildcards.
field_value() {
  local key="$1"
  sed -n "s/^[[:space:]]*${key}:[[:space:]]*//p" | head -n1 | sed -e 's/^"//' -e 's/"$//'
}

assert_absent() {
  local values_file="$1" repository="$2" label="$3"
  grep -qF "repository: $repository" "$values_file" &&
    fail "$label: repository: $repository should not be in the rendered values"
  return 0
}

# Both Cassandra image blocks share one default repository, so a count keeps the
# server and dynamicSeedDiscovery containers honest when they are not overridden.
assert_repository_count() {
  local values_file="$1" repository="$2" want="$3" label="$4" got
  got="$(grep -cF "repository: $repository" "$values_file" || true)"
  [[ "$got" == "$want" ]] ||
    fail "$label: expected $want occurrences of repository: $repository, got $got"
}

# ---------------------------------------------------------------------------
# 1. No overrides — every image keeps its mirrored global.image default
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
# Cassandra server + dynamicSeedDiscovery.
assert_repository_count "$work_dir/default-values.yaml" \
  test/nvcf/cassandra 2 "cassandra default"

# ---------------------------------------------------------------------------
# 2. Full override — every registry, repository, and tag key is honored
# ---------------------------------------------------------------------------
write_env <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
cassandra:
  image:
    registry: mirror.example.com
    repository: mirror/cassandra-server
    tag: "5.0.8-nv-2.0.1"
  dynamicSeedDiscovery:
    image:
      registry: seeds.example.com
      repository: mirror/cassandra-seeds
      tag: "5.0.8-nv-2.0.2"
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

render_values "$work_dir/override-values.yaml"
assert_image "$work_dir/override-values.yaml" \
  mirror/cassandra-server mirror.example.com 5.0.8-nv-2.0.1 "cassandra full override"
assert_image "$work_dir/override-values.yaml" \
  mirror/cassandra-seeds seeds.example.com 5.0.8-nv-2.0.2 "cassandra.dynamicSeedDiscovery full override"
assert_image "$work_dir/override-values.yaml" \
  natsio/nats-server-config-reloader docker.io 0.23.0 "nats.reloader full override"
assert_image "$work_dir/override-values.yaml" \
  alpine/k8s docker.io 1.36.1 "api.accountBootstrap full override"
assert_absent "$work_dir/override-values.yaml" \
  test/nvcf/cassandra "cassandra full override"
assert_absent "$work_dir/override-values.yaml" \
  test/nvcf/nats-server-config-reloader "nats.reloader full override"

# ---------------------------------------------------------------------------
# 3. Partial override — an unset key keeps its default
# ---------------------------------------------------------------------------
write_env <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
cassandra:
  image:
    repository: mirror/cassandra-server
  dynamicSeedDiscovery:
    image:
      repository: mirror/cassandra-seeds
nats:
  reloader:
    image:
      repository: mirror/nats-server-config-reloader
api:
  accountBootstrap:
    image:
      repository: mirror/alpine-k8s
EOF

render_values "$work_dir/partial-values.yaml"
assert_image "$work_dir/partial-values.yaml" \
  mirror/cassandra-server nvcr.io "" "cassandra repository-only override"
assert_image "$work_dir/partial-values.yaml" \
  mirror/cassandra-seeds nvcr.io "" "cassandra.dynamicSeedDiscovery repository-only override"
assert_image "$work_dir/partial-values.yaml" \
  mirror/nats-server-config-reloader nvcr.io 0.23.0 "nats.reloader repository-only override"
assert_image "$work_dir/partial-values.yaml" \
  mirror/alpine-k8s nvcr.io 1.36.1 "api.accountBootstrap repository-only override"

# Tag-only override: the repository still resolves from global.image.
write_env <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
cassandra:
  image:
    tag: "5.0.8-nv-2.0.1"
EOF

render_values "$work_dir/tag-only-values.yaml"
assert_image "$work_dir/tag-only-values.yaml" \
  test/nvcf/cassandra nvcr.io 5.0.8-nv-2.0.1 "cassandra tag-only override"

# ---------------------------------------------------------------------------
# 4. Explicit empty values are treated as unset, not as an empty path
# ---------------------------------------------------------------------------
write_env <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
cassandra:
  image:
    registry: ""
    repository: ""
    tag: ""
  dynamicSeedDiscovery:
    image:
      registry: ""
      repository: ""
      tag: ""
nats:
  reloader:
    image:
      registry: ""
      repository: ""
      tag: ""
api:
  accountBootstrap:
    image:
      registry: ""
      repository: ""
      tag: ""
EOF

render_values "$work_dir/empty-values.yaml"
grep -q 'repository: ""' "$work_dir/empty-values.yaml" &&
  fail "explicit empty: an empty repository reached the chart values"
grep -q 'registry: ""' "$work_dir/empty-values.yaml" &&
  fail "explicit empty: an empty registry reached the chart values"
assert_image "$work_dir/empty-values.yaml" \
  test/nvcf/nats-server-config-reloader nvcr.io 0.23.0 "nats.reloader explicit empty"
assert_image "$work_dir/empty-values.yaml" \
  test/nvcf/alpine-k8s nvcr.io 1.36.1 "api.accountBootstrap explicit empty"
assert_repository_count "$work_dir/empty-values.yaml" \
  test/nvcf/cassandra 2 "cassandra explicit empty"

# ---------------------------------------------------------------------------
# 5. Public-catalog install — the images nvidia/nvcf does not republish
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

render_values "$work_dir/public-catalog-values.yaml"
assert_image "$work_dir/public-catalog-values.yaml" \
  natsio/nats-server-config-reloader docker.io 0.23.0 "nats.reloader public catalog"
assert_image "$work_dir/public-catalog-values.yaml" \
  alpine/k8s docker.io 1.36.1 "api.accountBootstrap public catalog"
assert_absent "$work_dir/public-catalog-values.yaml" \
  nvidia/nvcf/nats-server-config-reloader "nats.reloader public catalog"
# The Cassandra server keeps the published public-catalog path.
assert_image "$work_dir/public-catalog-values.yaml" \
  nvidia/nvcf/cassandra nvcr.io "" "cassandra public catalog"
# alpine-k8s is intentionally still resolved from global.image.repository for
# cassandra.initialization and nats.nkeyJob; those values are inert today.

echo "image-override-wiring: OK"
