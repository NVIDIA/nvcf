#!/usr/bin/env bash
# Test that the bundled sis release honors a helmfile condition driven from
# environments/<env>.yaml. The sis release (spot-instance-service) had no
# condition, so it installed on every deploy and icms.enabled had no effect.
# It is now gated by `condition: icms.enabled`, defaulted to true in base.yaml so
# existing installs are unchanged. This guards two things: (a) with the default
# the release stays enabled (backward-compatible), and (b) icms.enabled: false in
# an environment file disables it, while leaving the other releases enabled.
set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
stacks_dir="$(cd "$stack_dir/.." && pwd)"
work_dir="$(mktemp -d)"
test_stacks_dir="$work_dir/stacks"
test_stack_dir="$test_stacks_dir/self-managed"
environment_name="sis-condition-wiring-test"
environment_file="$test_stack_dir/environments/$environment_name.yaml"
secrets_file="$test_stack_dir/secrets/$environment_name-secrets.yaml"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "sis-condition-wiring: $*" >&2
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
# every invocation has to supply them even though this test only lists releases.
helmfile_common=(
  --file "$test_stack_dir/helmfile.d"
  --environment default
  --state-values-set ingress.gatewayApi.controllerNamespace=envoy-gateway-system
  --state-values-set ingress.gatewayApi.gateways.shared.name=shared-gw
  --state-values-set ingress.gatewayApi.gateways.shared.namespace=envoy-gateway-system
  --state-values-set ingress.gatewayApi.gateways.grpc.name=grpc-gw
  --state-values-set ingress.gatewayApi.gateways.grpc.namespace=envoy-gateway-system
)

# helmfile list evaluates each release's condition and reports enabled per
# release, so it exercises the condition without pulling any chart.
render_list() {
  local output_file="$1"
  local render_log="$work_dir/list.log"
  if ! HELMFILE_ENV="$environment_name" HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" \
    helmfile "${helmfile_common[@]}" list --output json >"$output_file" 2>"$render_log"; then
    cat "$render_log" >&2
    fail "helmfile could not list the stack"
  fi
  test -s "$output_file" || fail "helmfile wrote no release list to $output_file"
}

assert_enabled() {
  local file="$1" name="$2" want="$3"
  yq -e -p=json ".[] | select(.name == \"$name\") | .enabled == $want" "$file" >/dev/null 2>&1 ||
    fail "$name: expected enabled == $want in $(basename "$file")"
}

write_env() {
  cat >"$environment_file"
}

# ---------------------------------------------------------------------------
# 1. Default: icms.enabled unset in the env file, so the base.yaml default (true)
#    stands and the release stays enabled -- existing installs are unchanged.
# ---------------------------------------------------------------------------
write_env <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
EOF

default_list="$work_dir/list-default.json"
render_list "$default_list"
assert_enabled "$default_list" sis true

# ---------------------------------------------------------------------------
# 2. Override: icms.enabled: false disables the release, while the other releases
#    (e.g. api) stay enabled -- the condition gates only sis.
# ---------------------------------------------------------------------------
write_env <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
icms:
  enabled: false
EOF

off_list="$work_dir/list-off.json"
render_list "$off_list"
assert_enabled "$off_list" sis false
assert_enabled "$off_list" api true

echo "sis-condition-wiring: all checks passed"
