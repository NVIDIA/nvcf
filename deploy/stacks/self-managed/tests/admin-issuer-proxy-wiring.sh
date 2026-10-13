#!/usr/bin/env bash
# Test that admin-issuer-proxy can be turned off from environments/<env>.yaml.
# It mints admin JWTs without authenticating the caller, yet its release had no
# condition and its Gateway route was hardcoded on. adminIssuerProxy.enabled now
# gates the release and its UI health entry, and adminIssuerProxy.gateway.enabled
# gates the route; both default to true so existing installs are unchanged.
set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work_dir="$(mktemp -d)"
test_stack_dir="$work_dir/self-managed"
environment_name="admin-issuer-proxy-wiring-test"
environment_file="$test_stack_dir/environments/$environment_name.yaml"
secrets_file="$test_stack_dir/secrets/$environment_name-secrets.yaml"
ui_entries='[.nvcfUi.controlPlane.components[] | select(.name == "admin-issuer-proxy")] | length'
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "admin-issuer-proxy-wiring: $*" >&2
  exit 1
}

mkdir -p "$test_stack_dir"
cp -R "$stack_dir"/. "$test_stack_dir"
mkdir -p "$(dirname "$secrets_file")"
printf '{}\n' >"$secrets_file"

# global.yaml.gotmpl marks the shared and grpc gateway values as required.
helmfile_common=(
  --file "$test_stack_dir/helmfile.d/02-core.yaml.gotmpl"
  --environment default
  --state-values-set ingress.gatewayApi.controllerNamespace=envoy-gateway-system
  --state-values-set ingress.gatewayApi.gateways.shared.name=shared-gw
  --state-values-set ingress.gatewayApi.gateways.shared.namespace=envoy-gateway-system
  --state-values-set ingress.gatewayApi.gateways.grpc.name=grpc-gw
  --state-values-set ingress.gatewayApi.gateways.grpc.namespace=envoy-gateway-system
)

run_helmfile() {
  local log="$work_dir/helmfile.log"
  if ! HELMFILE_ENV="$environment_name" HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" \
    helmfile "${helmfile_common[@]}" "$@" 2>"$log"; then
    cat "$log" >&2
    fail "helmfile $* failed"
  fi
}

# helmfile list evaluates each release's condition without pulling a chart.
render_list() {
  run_helmfile list --output json >"$1"
  test -s "$1" || fail "helmfile wrote no release list to $1"
}

# Every release renders the whole of global.yaml.gotmpl, so the api release
# carries the admin-issuer-proxy values even when that release is off.
render_values() {
  run_helmfile --selector name=api write-values --output-file-template "$1" >/dev/null
  test -s "$1" || fail "helmfile wrote no values to $1"
}

assert_enabled() {
  local file="$1" name="$2" want="$3"
  yq -e -p=json ".[] | select(.name == \"$name\") | .enabled == $want" "$file" >/dev/null 2>&1 ||
    fail "$name: expected enabled == $want in $(basename "$file")"
}

assert_yaml_value() {
  local file="$1" expression="$2" want="$3" label="$4" got
  got="$(yq -r "$expression" "$file")"
  [[ "$got" == "$want" ]] || fail "$label: expected $want, got ${got:-<none>}"
}

write_env() {
  cat >"$environment_file"
}

# 1. Default: the base.yaml defaults stand, so the release, its route and its UI
#    health entry all stay on and existing installs are unchanged. The UI health
#    list only renders with the UI add-on on, so cases 1 and 2 turn it on.
write_env <<'YAML'
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
addons:
  nvcfUi:
    enabled: true
YAML

render_list "$work_dir/list-default.json"
assert_enabled "$work_dir/list-default.json" admin-issuer-proxy true
render_values "$work_dir/values-default.yaml"
assert_yaml_value "$work_dir/values-default.yaml" '.adminIssuerProxy.gateway.enabled' true \
  "default: the route must stay enabled"
assert_yaml_value "$work_dir/values-default.yaml" "$ui_entries" 1 \
  "default: the UI must health-check admin-issuer-proxy"

# 2. adminIssuerProxy.enabled: false disables the release and drops its UI health
#    entry, while the other releases stay enabled.
write_env <<'YAML'
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
addons:
  nvcfUi:
    enabled: true
adminIssuerProxy:
  enabled: false
YAML

render_list "$work_dir/list-off.json"
assert_enabled "$work_dir/list-off.json" admin-issuer-proxy false
assert_enabled "$work_dir/list-off.json" api true
render_values "$work_dir/values-off.yaml"
assert_yaml_value "$work_dir/values-off.yaml" "$ui_entries" 0 \
  "off: the UI must not health-check a release that is not installed"

# 3. adminIssuerProxy.gateway.enabled: false drops only the route; the release
#    stays enabled for in-cluster callers.
write_env <<'YAML'
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
adminIssuerProxy:
  gateway:
    enabled: false
YAML

render_list "$work_dir/list-route-off.json"
assert_enabled "$work_dir/list-route-off.json" admin-issuer-proxy true
render_values "$work_dir/values-route-off.yaml"
assert_yaml_value "$work_dir/values-route-off.yaml" '.adminIssuerProxy.gateway.enabled' false \
  "route off: adminIssuerProxy.gateway.enabled must reach the release values"

echo "admin-issuer-proxy-wiring: all checks passed"
