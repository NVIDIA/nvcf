#!/usr/bin/env bash
# Test that the apiKeys, sis, and reval gateway-route enable flags thread from
# environments/<env>.yaml through global.yaml.gotmpl into the gateway-routes
# chart values. These three routes attach to the shared (public) Gateway, so an
# operator must be able to disable them from an environment file -- for example,
# to keep api-keys off the public edge in a split/multi-cluster deployment --
# without patching global.yaml.gotmpl. Each flag defaults to the chart default
# (true), so an unset flag leaves the route enabled and existing installs are
# unchanged.
set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
stacks_dir="$(cd "$stack_dir/.." && pwd)"
work_dir="$(mktemp -d)"
test_stacks_dir="$work_dir/stacks"
test_stack_dir="$test_stacks_dir/self-managed"
environment_name="gateway-routes-enable-wiring-test"
environment_file="$test_stack_dir/environments/$environment_name.yaml"
secrets_file="$test_stack_dir/secrets/$environment_name-secrets.yaml"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "gateway-routes-enable-wiring: $*" >&2
  exit 1
}

# The self-managed helmfile.d reaches a sibling stack through a relative path,
# so copy the whole stacks directory, not just self-managed.
mkdir -p "$test_stacks_dir"
cp -R "$stacks_dir"/. "$test_stacks_dir"
mkdir -p "$(dirname "$secrets_file")"
printf '{}\n' >"$secrets_file"

# Every stack the copy reaches resolves ../environments/$HELMFILE_ENV.yaml
# against its own directory, so each one needs a file under this test's
# environment name.
for stack_environments in "$test_stacks_dir"/*/environments; do
  test -d "$stack_environments" || continue
  test -e "$stack_environments/$environment_name.yaml" ||
    printf '{}\n' >"$stack_environments/$environment_name.yaml"
done

# global.yaml.gotmpl marks the shared and grpc gateway values as required, so
# every invocation has to supply them even though this test renders only the
# ingress release.
helmfile_common=(
  --file "$test_stack_dir/helmfile.d"
  --environment default
  --state-values-set ingress.gatewayApi.controllerNamespace=envoy-gateway-system
  --state-values-set ingress.gatewayApi.gateways.shared.name=shared-gw
  --state-values-set ingress.gatewayApi.gateways.shared.namespace=envoy-gateway-system
  --state-values-set ingress.gatewayApi.gateways.grpc.name=grpc-gw
  --state-values-set ingress.gatewayApi.gateways.grpc.namespace=envoy-gateway-system
)

run_helmfile() {
  HELMFILE_ENV="$environment_name" \
    HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" \
    helmfile "${helmfile_common[@]}" "$@"
}

# Write the values the ingress (gateway-routes) release passes to its chart.
render_ingress_values() {
  local output_file="$1"
  local render_log="$work_dir/ingress-write-values.log"
  if ! run_helmfile \
    --selector name=ingress \
    write-values \
    --output-file-template "$output_file" 2>"$render_log"; then
    cat "$render_log" >&2
    fail "ingress: helmfile could not render the stack"
  fi
  test -s "$output_file" ||
    fail "ingress: helmfile wrote no values to $output_file"
}

# Assert nvcfGatewayRoutes.routes.<route>.enabled equals the expected boolean in
# the rendered release values.
assert_route_enabled() {
  local route="$1" expected="$2" file="$3"
  yq -e ".nvcfGatewayRoutes.routes.${route}.enabled == ${expected}" "$file" >/dev/null 2>&1 ||
    fail "route ${route}: expected enabled=${expected} in $(basename "$file")"
}

write_env() {
  cat >"$environment_file"
}

# ---------------------------------------------------------------------------
# 1. Default: flags unset -> the stack passes enabled: true for all three,
#    matching the chart default (existing installs are unchanged).
# ---------------------------------------------------------------------------
write_env <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
EOF

default_values="$work_dir/ingress-default-values.yaml"
render_ingress_values "$default_values"
assert_route_enabled apiKeys true "$default_values"
assert_route_enabled sis true "$default_values"
assert_route_enabled reval true "$default_values"

# ---------------------------------------------------------------------------
# 2. Override: an environment file disabling the three routes reaches the chart
#    as enabled: false -- the passthrough this change adds.
# ---------------------------------------------------------------------------
write_env <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
ingress:
  gatewayApi:
    routes:
      apiKeys:
        enabled: false
      sis:
        enabled: false
      reval:
        enabled: false
EOF

off_values="$work_dir/ingress-off-values.yaml"
render_ingress_values "$off_values"
assert_route_enabled apiKeys false "$off_values"
assert_route_enabled sis false "$off_values"
assert_route_enabled reval false "$off_values"

echo "gateway-routes-enable-wiring: all checks passed"
