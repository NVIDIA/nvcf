#!/usr/bin/env bash
# Test that the cpuWorkloads.enabled stack switch reaches ICMS (the sis release) as the
# ICMS_CPUWORKLOADS_ENABLED environment variable, and only when the switch is on. With the
# switch off, which is the default, the render must be unchanged. A value that is not a
# boolean fails the render instead of being passed on silently.
#
# global.yaml.gotmpl emits every top-level block into any release it renders, so the api
# release exposes sis: for assertion.
set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work_dir="$(mktemp -d)"
test_stack_dir="$work_dir/self-managed"
environment_name="cpu-workloads-wiring-test"
environment_file="$test_stack_dir/environments/$environment_name.yaml"
secrets_file="$test_stack_dir/secrets/$environment_name-secrets.yaml"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "cpu-workloads-wiring: $*" >&2
  exit 1
}

mkdir -p "$test_stack_dir"
cp -R "$stack_dir"/. "$test_stack_dir"
printf '{}\n' >"$secrets_file"

write_environment() {
  cat >"$environment_file"
}

# Leaves helmfile output in write-values.log so negative cases can assert on it.
render_values() {
  local output_file="$1"
  local selector="$2"
  HELMFILE_ENV="$environment_name" HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" \
    helmfile \
      --file "$test_stack_dir/helmfile.d/02-core.yaml.gotmpl" \
      --environment default \
      --state-values-set ingress.gatewayApi.controllerNamespace=envoy-gateway-system \
      --state-values-set ingress.gatewayApi.gateways.shared.name=shared-gw \
      --state-values-set ingress.gatewayApi.gateways.shared.namespace=envoy-gateway-system \
      --state-values-set ingress.gatewayApi.gateways.grpc.name=grpc-gw \
      --state-values-set ingress.gatewayApi.gateways.grpc.namespace=envoy-gateway-system \
      --selector "name=$selector" \
      write-values \
      --output-file-template "$output_file" >"$work_dir/write-values.log" 2>&1
}

# The sis release the stack installs, as helmfile lists it: its chart and version.
list_sis_release() {
  HELMFILE_ENV="$environment_name" HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" \
    helmfile \
      --file "$test_stack_dir/helmfile.d/02-core.yaml.gotmpl" \
      --environment default \
      --state-values-set ingress.gatewayApi.controllerNamespace=envoy-gateway-system \
      --state-values-set ingress.gatewayApi.gateways.shared.name=shared-gw \
      --state-values-set ingress.gatewayApi.gateways.shared.namespace=envoy-gateway-system \
      --state-values-set ingress.gatewayApi.gateways.grpc.name=grpc-gw \
      --state-values-set ingress.gatewayApi.gateways.grpc.namespace=envoy-gateway-system \
      --selector "name=sis" \
      list --output json 2>/dev/null
}

render_values_or_fail() {
  local output_file="$1"
  local selector="$2"
  if ! render_values "$output_file" "$selector"; then
    cat "$work_dir/write-values.log" >&2
    fail "helmfile could not render the stack"
  fi
  test -s "$output_file" || fail "helmfile wrote no values to $output_file"
}

assert_value() {
  local file="$1" expression="$2" want="$3" label="$4" got
  got="$(yq -r "$expression" "$file")"
  [[ "$got" == "$want" ]] ||
    fail "$label: expected $want, got ${got:-<none>}"
}

# ---------------------------------------------------------------------------
# 1. Default: the switch is off and ICMS gets no CPU workloads variable.
# ---------------------------------------------------------------------------
write_environment <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
EOF

default_values="$work_dir/default-values.yaml"
render_values_or_fail "$default_values" api
assert_value "$default_values" '.sis.env.ICMS_CPUWORKLOADS_ENABLED' null \
  "default: ICMS_CPUWORKLOADS_ENABLED is not emitted"

# The shipped default must be explicit and false, so the switch is discoverable.
assert_value "$stack_dir/environments/base.yaml" '.cpuWorkloads.enabled' false \
  "base.yaml: cpuWorkloads.enabled defaults to false"

# ---------------------------------------------------------------------------
# 2. Explicitly off renders the same as the default.
# ---------------------------------------------------------------------------
write_environment <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
cpuWorkloads:
  enabled: false
EOF

off_values="$work_dir/off-values.yaml"
render_values_or_fail "$off_values" api
assert_value "$off_values" '.sis.env.ICMS_CPUWORKLOADS_ENABLED' null \
  "off: ICMS_CPUWORKLOADS_ENABLED is not emitted"

# ---------------------------------------------------------------------------
# 3. On: ICMS receives the variable, and the other sis environment is untouched.
# ---------------------------------------------------------------------------
write_environment <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
cpuWorkloads:
  enabled: true
  sisChartVersion: 9.9.9
EOF

on_values="$work_dir/on-values.yaml"
render_values_or_fail "$on_values" api
assert_value "$on_values" '.sis.env.ICMS_CPUWORKLOADS_ENABLED' true \
  "on: ICMS_CPUWORKLOADS_ENABLED is true"
assert_value "$on_values" '.sis.env.MANAGEMENT_TRACING_ENABLED' \
  "$(yq -r '.sis.env.MANAGEMENT_TRACING_ENABLED' "$default_values")" \
  "on: unrelated sis environment is unchanged"

# The switch must not leak into other services' environment.
assert_value "$on_values" '.api.env.ICMS_CPUWORKLOADS_ENABLED' null \
  "on: the variable does not leak into the API release"

# ---------------------------------------------------------------------------
# 4. A value that is not a boolean fails the render.
# ---------------------------------------------------------------------------
write_environment <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
cpuWorkloads:
  enabled: "yes"
EOF

if render_values "$work_dir/invalid-values.yaml" api; then
  fail "a string cpuWorkloads.enabled was accepted"
fi
grep -Fq 'cpuWorkloads.enabled must be true or false' "$work_dir/write-values.log" ||
  fail "a string cpuWorkloads.enabled did not return the expected error"

# ---------------------------------------------------------------------------
# 5. The pinned sis chart predates CPU-only workloads, so the switch is refused on the stale pin and accepted only with
# a chart named explicitly, as a released version or through the sis.chartPath override.
# ---------------------------------------------------------------------------
write_environment <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
EOF
[[ "$(list_sis_release | jq -r '.[0].version')" == "2.6.0" ]] || fail "off: the sis chart pin changed"

write_environment <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
cpuWorkloads:
  enabled: true
EOF
if render_values "$work_dir/stale-values.yaml" api; then
  fail "cpuWorkloads.enabled was accepted on the stale sis chart pin"
fi
grep -Fq 'cpuWorkloads.sisChartVersion' "$work_dir/write-values.log" ||
  fail "the stale pin did not name cpuWorkloads.sisChartVersion in its error"

write_environment <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
cpuWorkloads:
  enabled: true
  sisChartVersion: 9.9.9
EOF
[[ "$(list_sis_release | jq -r '.[0].version')" == "9.9.9" ]] || fail "cpuWorkloads.sisChartVersion did not set the sis chart version"

write_environment <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
cpuWorkloads:
  enabled: true
sis:
  chartPath: /charts/helm-nvcf-sis
EOF
render_values_or_fail "$work_dir/path-values.yaml" api
path_release="$(list_sis_release)"
[[ "$(jq -r '.[0].chart' <<<"$path_release")" == "/charts/helm-nvcf-sis" ]] || fail "sis.chartPath did not set the sis chart"
[[ "$(jq -r '.[0].version // ""' <<<"$path_release")" == "" ]] || fail "a chart path must not carry the pinned version"

write_environment <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
cpuWorkloads:
  enabled: false
  sisChartVersion: 9.9.9
EOF
if render_values "$work_dir/version-only-values.yaml" api; then
  fail "a sis chart version without cpuWorkloads.enabled was accepted"
fi

write_environment <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
cpuWorkloads:
  enabled: true
  sisChartVersion: 9.9.9
sis:
  chartPath: /charts/helm-nvcf-sis
EOF
if render_values "$work_dir/both-values.yaml" api; then
  fail "both a sis chart version and a chart path were accepted"
fi

write_environment <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
cpuWorkloads:
  enabled: true
  sisChartVersion: ""
sis:
  chartPath: /charts/helm-nvcf-sis
EOF
if render_values "$work_dir/empty-version-values.yaml" api; then
  fail "an empty cpuWorkloads.sisChartVersion was accepted"
fi
grep -Fq 'must not be empty' "$work_dir/write-values.log" ||
  fail "the empty sis chart version did not say it must not be empty"

echo "cpu-workloads-wiring: OK"
