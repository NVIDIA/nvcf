#!/usr/bin/env bash
# Test that the cert-manager ACME solver, NATS reloader, and API
# account-bootstrap defaults are owned by their charts and that non-empty
# supporting-image overrides thread from an environment file through
# global.yaml.gotmpl into the rendered chart values.
#
# Cassandra defaults to the mirrored <global.image.repository>/cassandra path.
# The ACME solver, NATS config reloader, and account-bootstrap alpine-k8s image
# are not republished under the public nvidia/nvcf catalog. Their charts default
# to upstream sources. The stack forwards environment overrides only when an
# operator mirrors one of these images.
set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
api_chart_values="$stack_dir/../../helm/cloud-functions/nvcf-api/values.yaml"
cert_manager_chart="$stack_dir/../../helm/cert-manager"
cert_manager_chart_values="$cert_manager_chart/charts/cert-manager/values.yaml"
nats_chart_values="$stack_dir/../../helm/nats/values.yaml"
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

# Read each default from its owner rather than duplicating released versions in
# this stack-level forwarding test.
nats_reloader_default_registry="$(yq -r '.nats.reloader.image.registry // ""' "$nats_chart_values")"
nats_reloader_default_repository="$(yq -r '.nats.reloader.image.repository // ""' "$nats_chart_values")"
nats_reloader_default_tag="$(yq -r '.nats.reloader.image.tag // ""' "$nats_chart_values")"
account_bootstrap_default_registry="$(yq -r '.api.accountBootstrap.image.registry // ""' "$api_chart_values")"
account_bootstrap_default_repository="$(yq -r '.api.accountBootstrap.image.repository // ""' "$api_chart_values")"
account_bootstrap_default_tag="$(yq -r '.api.accountBootstrap.image.tag // ""' "$api_chart_values")"
acme_solver_default_registry="$(yq -r '.imageRegistry // ""' "$cert_manager_chart_values")"
acme_solver_default_namespace="$(yq -r '.imageNamespace // ""' "$cert_manager_chart_values")"
acme_solver_default_name="$(yq -r '.acmesolver.image.name // ""' "$cert_manager_chart_values")"
acme_solver_default_tag="$(yq -r '.appVersion // ""' "$cert_manager_chart/Chart.yaml")"
test "$nats_reloader_default_registry" = "docker.io" ||
  fail "nats chart must own the upstream reloader registry default"
test "$nats_reloader_default_repository" = "natsio/nats-server-config-reloader" ||
  fail "nats chart must own the upstream reloader repository default"
test -n "$nats_reloader_default_tag" ||
  fail "nats chart must own the reloader tag default"
test "$account_bootstrap_default_registry" = "docker.io" ||
  fail "api chart must own the upstream account-bootstrap registry default"
test "$account_bootstrap_default_repository" = "alpine/k8s" ||
  fail "api chart must own the upstream account-bootstrap repository default"
test -n "$account_bootstrap_default_tag" ||
  fail "api chart must own the account-bootstrap tag default"
test "$acme_solver_default_registry" = "quay.io" ||
  fail "cert-manager chart must own the upstream ACME solver registry default"
test "$acme_solver_default_namespace" = "jetstack" ||
  fail "cert-manager chart must own the upstream ACME solver namespace default"
test "$acme_solver_default_name" = "cert-manager-acmesolver" ||
  fail "cert-manager chart must own the upstream ACME solver image name default"
test "$acme_solver_default_tag" = "1.20.2" ||
  fail "cert-manager wrapper must pin the ACME solver app version"

mkdir -p "$test_stack_dir"
cp -R "$stack_dir"/. "$test_stack_dir"
printf '{}\n' >"$secrets_file"

helm template cert-manager "$cert_manager_chart" --namespace cert-manager \
  >"$work_dir/cert-manager-default.yaml"
grep -qF -- \
  '--acme-http01-solver-image=quay.io/jetstack/cert-manager-acmesolver:v1.20.2' \
  "$work_dir/cert-manager-default.yaml" ||
  fail "cert-manager chart must render the upstream ACME solver image"

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

assert_yaml_path_absent() {
  local values_file="$1" path="$2" label="$3" actual
  actual="$(yq -r "$path // \"\"" "$values_file")"
  [[ -z "$actual" ]] || fail "$label: $path should not be in the rendered values"
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
# 1. No overrides. Cassandra keeps its stack default. The stack omits the ACME
#    solver, NATS reloader, and API account-bootstrap images so their charts
#    keep the defaults.
# ---------------------------------------------------------------------------
write_env <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
EOF

render_values "$work_dir/default-values.yaml"
assert_absent "$work_dir/default-values.yaml" \
  natsio/nats-server-config-reloader "nats.reloader chart default"
assert_yaml_path_absent "$work_dir/default-values.yaml" \
  .nats.reloader "nats.reloader chart default"
assert_absent "$work_dir/default-values.yaml" \
  alpine/k8s "api.accountBootstrap chart default"
assert_yaml_path_absent "$work_dir/default-values.yaml" \
  .api.accountBootstrap.image "api.accountBootstrap chart default"
assert_yaml_path_absent "$work_dir/default-values.yaml" \
  '."cert-manager".acmesolver.image' "cert-manager ACME solver chart default"
assert_absent "$work_dir/default-values.yaml" \
  test/nvcf/nats-server-config-reloader "nats.reloader default"
# The only remaining mirrored alpine-k8s paths are the inert
# cassandra.initialization and nats.nkeyJob values; accountBootstrap is not
# one of them any more.
assert_repository_count "$work_dir/default-values.yaml" \
  test/nvcf/alpine-k8s 2 "api.accountBootstrap default"
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
      registry: mirror.example.com
      repository: mirror/nats-server-config-reloader
      tag: "0.23.0-mirror"
api:
  accountBootstrap:
    image:
      registry: mirror.example.com
      repository: mirror/alpine-k8s
      tag: "1.36.1-mirror"
certManager:
  acmesolver:
    image:
      repository: mirror.example.com/mirror/cert-manager-acmesolver
      tag: "v1.20.2-mirror"
EOF

render_values "$work_dir/override-values.yaml"
assert_image "$work_dir/override-values.yaml" \
  mirror/cassandra-server mirror.example.com 5.0.8-nv-2.0.1 "cassandra full override"
assert_image "$work_dir/override-values.yaml" \
  mirror/cassandra-seeds seeds.example.com 5.0.8-nv-2.0.2 "cassandra.dynamicSeedDiscovery full override"
assert_image "$work_dir/override-values.yaml" \
  mirror/nats-server-config-reloader mirror.example.com 0.23.0-mirror "nats.reloader full override"
assert_image "$work_dir/override-values.yaml" \
  mirror/alpine-k8s mirror.example.com 1.36.1-mirror "api.accountBootstrap full override"
assert_image "$work_dir/override-values.yaml" \
  mirror.example.com/mirror/cert-manager-acmesolver "" v1.20.2-mirror \
  "cert-manager ACME solver full override"
assert_absent "$work_dir/override-values.yaml" \
  test/nvcf/cassandra "cassandra full override"
assert_absent "$work_dir/override-values.yaml" \
  natsio/nats-server-config-reloader "nats.reloader full override"
assert_absent "$work_dir/override-values.yaml" \
  alpine/k8s "api.accountBootstrap full override"

# ---------------------------------------------------------------------------
# 3. Partial override — existing image behavior stays unchanged. For the API
#    account-bootstrap image, the stack forwards only the supplied key and the
#    chart supplies the remaining defaults.
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
certManager:
  acmesolver:
    image:
      repository: mirror.example.com/mirror/cert-manager-acmesolver
EOF

render_values "$work_dir/partial-values.yaml"
assert_image "$work_dir/partial-values.yaml" \
  mirror/cassandra-server nvcr.io "" "cassandra repository-only override"
assert_image "$work_dir/partial-values.yaml" \
  mirror/cassandra-seeds nvcr.io "" "cassandra.dynamicSeedDiscovery repository-only override"
assert_image "$work_dir/partial-values.yaml" \
  mirror/nats-server-config-reloader "" "" "nats.reloader repository-only forwarding"
assert_yaml_path_absent "$work_dir/partial-values.yaml" \
  .nats.reloader.image.tag "nats.reloader repository-only forwarding"
assert_image "$work_dir/partial-values.yaml" \
  mirror/alpine-k8s "" "" "api.accountBootstrap repository-only forwarding"
assert_image "$work_dir/partial-values.yaml" \
  mirror.example.com/mirror/cert-manager-acmesolver "" "" \
  "cert-manager ACME solver repository-only forwarding"
assert_yaml_path_absent "$work_dir/partial-values.yaml" \
  '."cert-manager".acmesolver.image.tag' \
  "cert-manager ACME solver repository-only forwarding"

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
certManager:
  acmesolver:
    image:
      repository: ""
      tag: ""
      digest: ""
      pullPolicy: ""
EOF

render_values "$work_dir/empty-values.yaml"
grep -q 'repository: ""' "$work_dir/empty-values.yaml" &&
  fail "explicit empty: an empty repository reached the chart values"
grep -q 'registry: ""' "$work_dir/empty-values.yaml" &&
  fail "explicit empty: an empty registry reached the chart values"
assert_absent "$work_dir/empty-values.yaml" \
  natsio/nats-server-config-reloader "nats.reloader explicit empty"
assert_yaml_path_absent "$work_dir/empty-values.yaml" \
  .nats.reloader "nats.reloader explicit empty"
assert_absent "$work_dir/empty-values.yaml" \
  alpine/k8s "api.accountBootstrap explicit empty"
assert_yaml_path_absent "$work_dir/empty-values.yaml" \
  .api.accountBootstrap.image "api.accountBootstrap explicit empty"
assert_yaml_path_absent "$work_dir/empty-values.yaml" \
  '."cert-manager".acmesolver.image' "cert-manager ACME solver explicit empty"
assert_repository_count "$work_dir/empty-values.yaml" \
  test/nvcf/cassandra 2 "cassandra explicit empty"

# ---------------------------------------------------------------------------
# 5. Public-catalog install — no override needed for the images nvidia/nvcf
#    does not republish
# ---------------------------------------------------------------------------
write_env <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: nvidia/nvcf
EOF

render_values "$work_dir/public-catalog-values.yaml"
assert_absent "$work_dir/public-catalog-values.yaml" \
  natsio/nats-server-config-reloader "nats.reloader public catalog"
assert_yaml_path_absent "$work_dir/public-catalog-values.yaml" \
  .nats.reloader "nats.reloader public catalog"
assert_absent "$work_dir/public-catalog-values.yaml" \
  alpine/k8s "api.accountBootstrap public catalog"
assert_yaml_path_absent "$work_dir/public-catalog-values.yaml" \
  .api.accountBootstrap.image "api.accountBootstrap public catalog"
assert_yaml_path_absent "$work_dir/public-catalog-values.yaml" \
  '."cert-manager".acmesolver.image' "cert-manager ACME solver public catalog"
assert_absent "$work_dir/public-catalog-values.yaml" \
  nvidia/nvcf/nats-server-config-reloader "nats.reloader public catalog"
# The Cassandra server keeps the published public-catalog path.
assert_image "$work_dir/public-catalog-values.yaml" \
  nvidia/nvcf/cassandra nvcr.io "" "cassandra public catalog"
# alpine-k8s is intentionally still resolved from global.image.repository for
# cassandra.initialization and nats.nkeyJob; those values are inert today.

# ---------------------------------------------------------------------------
# 6. Mirror install — an operator that copied both images into one registry
#    redirects them from the environment file
# ---------------------------------------------------------------------------
write_env <<'EOF'
global:
  image:
    registry: mirror.example.com
    repository: mirror/nvcf
nats:
  reloader:
    image:
      registry: mirror.example.com
      repository: mirror/nvcf/nats-server-config-reloader
api:
  accountBootstrap:
    image:
      registry: mirror.example.com
      repository: mirror/nvcf/alpine-k8s
certManager:
  acmesolver:
    image:
      repository: mirror.example.com/mirror/nvcf/cert-manager-acmesolver
EOF

render_values "$work_dir/mirror-values.yaml"
assert_image "$work_dir/mirror-values.yaml" \
  mirror/nvcf/nats-server-config-reloader mirror.example.com "" "nats.reloader mirror forwarding"
assert_yaml_path_absent "$work_dir/mirror-values.yaml" \
  .nats.reloader.image.tag "nats.reloader mirror forwarding"
assert_image "$work_dir/mirror-values.yaml" \
  mirror/nvcf/alpine-k8s mirror.example.com "" "api.accountBootstrap mirror forwarding"
assert_image "$work_dir/mirror-values.yaml" \
  mirror.example.com/mirror/nvcf/cert-manager-acmesolver "" "" \
  "cert-manager ACME solver mirror forwarding"
assert_absent "$work_dir/mirror-values.yaml" \
  natsio/nats-server-config-reloader "nats.reloader mirror install"
assert_absent "$work_dir/mirror-values.yaml" \
  alpine/k8s "api.accountBootstrap mirror install"

echo "image-override-wiring: OK"
