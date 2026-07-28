#!/usr/bin/env bash
set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "profile-defaults: $*" >&2
  exit 1
}

assert_contract_value() {
  local manifests="$1"
  local profile="$2"
  local key="$3"
  local value="$4"

  grep -q "$key: \"$value\"" $manifests ||
    fail "$profile profile did not resolve $key to $value"
}

profile_releases() {
  local profile="$1"
  HELMFILE_ENV=local helmfile \
    --file "$stack_dir/helmfile.d" \
    --environment default \
    --allow-no-matching-release \
    --state-values-set "observability.profile=$profile" \
    list 2>/dev/null |
    awk 'NR > 1 {print $1}' |
    sort
}

render_monitors() {
  local profile="$1"
  local output_dir="$work_dir/$profile"

  HELMFILE_ENV=local helmfile \
    --file "$stack_dir/helmfile.d" \
    --environment default \
    --state-values-set "observability.profile=$profile" \
    --selector name=default-monitors \
    template --output-dir "$output_dir" >/dev/null
}

render_monitor_overrides() {
  local profile="$1"
  local output_name="$2"
  local control_enabled="$3"
  local compute_enabled="$4"
  local output_dir="$work_dir/$output_name"

  HELMFILE_ENV=local helmfile \
    --file "$stack_dir/helmfile.d" \
    --environment default \
    --state-values-set "observability.profile=$profile" \
    --state-values-set "defaultMonitors.controlPlane.enabled=$control_enabled" \
    --state-values-set "defaultMonitors.computePlane.enabled=$compute_enabled" \
    --selector name=default-monitors \
    template --output-dir "$output_dir" >/dev/null
}

render_compute_monitor_override() {
  local output_dir="$work_dir/compute-worker-disabled"

  HELMFILE_ENV=local helmfile \
    --file "$stack_dir/helmfile.d" \
    --environment default \
    --state-values-set observability.profile=compute \
    --state-values-set defaultMonitors.computePlane.worker.enabled=false \
    --selector name=default-monitors \
    template --output-dir "$output_dir" >/dev/null
}

service_monitor_template_count="$(
  find "$stack_dir/charts/nvcf-default-monitors/templates" \
    -maxdepth 1 -type f -name '*servicemonitor*.yaml' |
    wc -l |
    tr -d ' '
)"
test "$service_monitor_template_count" = "1" ||
  fail "default monitors chart must use one generic ServiceMonitor template"

enabled_releases="$(
  cat <<'EOF'
default-monitors
observability-contract
opentelemetry-operator
otel-collector
prometheus-operator-crds
victoria-metrics
EOF
)"

test -z "$(profile_releases disabled)" ||
  fail "disabled profile rendered releases"

for profile in control compute all; do
  test "$(profile_releases "$profile")" = "$enabled_releases" ||
    fail "$profile profile did not render the enabled release set exactly once"
done

for profile in control compute all; do
  render_monitors "$profile"
done

render_monitor_overrides control control-swapped false true
render_monitor_overrides compute compute-swapped true false
render_compute_monitor_override

helm template default-monitors "$stack_dir/charts/nvcf-default-monitors" \
  --set controlPlane.enabled=true >"$work_dir/chart-control-defaults.yaml"
helm template default-monitors "$stack_dir/charts/nvcf-default-monitors" \
  --set computePlane.enabled=true >"$work_dir/chart-compute-defaults.yaml"

control_manifests="$(find "$work_dir/control" -type f -name '*.yaml' -print)"
compute_manifests="$(find "$work_dir/compute" -type f -name '*.yaml' -print)"
all_manifests="$(find "$work_dir/all" -type f -name '*.yaml' -print)"
control_swapped_manifests="$(find "$work_dir/control-swapped" -type f -name '*.yaml' -print)"
compute_swapped_manifests="$(find "$work_dir/compute-swapped" -type f -name '*.yaml' -print)"
compute_worker_disabled_manifests="$(find "$work_dir/compute-worker-disabled" -type f -name '*.yaml' -print)"
chart_control_manifests="$work_dir/chart-control-defaults.yaml"
chart_compute_manifests="$work_dir/chart-compute-defaults.yaml"

for monitor in state-metrics invocation-service grpc-proxy llm-api-gateway; do
  grep -q "nvcf-default-monitors-$monitor" "$chart_control_manifests" ||
    fail "monitor chart control defaults are missing $monitor monitor"
  grep -q "nvcf-default-monitors-$monitor" $control_manifests ||
    fail "control profile is missing $monitor monitor"
  grep -q "nvcf-default-monitors-$monitor" $all_manifests ||
    fail "all profile is missing $monitor monitor"
  grep -q "nvcf-default-monitors-$monitor" $compute_swapped_manifests ||
    fail "explicit control monitor override did not win for compute profile"
  if grep -q "nvcf-default-monitors-$monitor" $control_swapped_manifests; then
    fail "explicit control monitor override did not disable $monitor"
  fi
  if grep -q "nvcf-default-monitors-$monitor" $compute_manifests; then
    fail "compute profile rendered $monitor control-plane monitor"
  fi
done

for monitor in nvca dcgm worker; do
  grep -q "nvcf-default-monitors-$monitor" "$chart_compute_manifests" ||
    fail "monitor chart compute defaults are missing $monitor monitor"
  grep -q "nvcf-default-monitors-$monitor" $compute_manifests ||
    fail "compute profile is missing $monitor monitor"
  grep -q "nvcf-default-monitors-$monitor" $all_manifests ||
    fail "all profile is missing $monitor monitor"
  grep -q "nvcf-default-monitors-$monitor" $control_swapped_manifests ||
    fail "explicit compute monitor override did not win for control profile"
  if grep -q "nvcf-default-monitors-$monitor" $compute_swapped_manifests; then
    fail "explicit compute monitor override did not disable $monitor"
  fi
  if grep -q "nvcf-default-monitors-$monitor" $control_manifests; then
    fail "control profile rendered $monitor compute-plane monitor"
  fi
done

for monitor in nvca dcgm; do
  grep -q "nvcf-default-monitors-$monitor" $compute_worker_disabled_manifests ||
    fail "nested worker override disabled the $monitor monitor"
done
if grep -q "nvcf-default-monitors-worker" $compute_worker_disabled_manifests; then
  fail "nested worker monitor override was ignored"
fi

for profile in control compute all; do
  HELMFILE_ENV=local helmfile \
    --file "$stack_dir/helmfile.d" \
    --environment default \
    --state-values-set "observability.profile=$profile" \
    --selector name=observability-contract \
    template --output-dir "$work_dir/$profile-contract" >/dev/null
done

control_contract="$(find "$work_dir/control-contract" -type f -name '*.yaml' -print)"
compute_contract="$(find "$work_dir/compute-contract" -type f -name '*.yaml' -print)"
all_contract="$(find "$work_dir/all-contract" -type f -name '*.yaml' -print)"

assert_contract_value "$control_contract" control controlTargetsEnabled true
assert_contract_value "$control_contract" control computeTargetsEnabled false
assert_contract_value "$control_contract" control byooSupportEnabled false
assert_contract_value "$control_contract" control nvcaCollectorEnabled false
assert_contract_value "$control_contract" control autoscalerIntegrationEnabled true

assert_contract_value "$compute_contract" compute controlTargetsEnabled false
assert_contract_value "$compute_contract" compute computeTargetsEnabled true
assert_contract_value "$compute_contract" compute byooSupportEnabled true
assert_contract_value "$compute_contract" compute nvcaCollectorEnabled false
assert_contract_value "$compute_contract" compute autoscalerIntegrationEnabled false
if grep -q 'TIMESERIES_DB__TIMESERIES_DB_URL' $compute_contract; then
  fail "compute profile published an autoscaler contract"
fi

assert_contract_value "$all_contract" all controlTargetsEnabled true
assert_contract_value "$all_contract" all computeTargetsEnabled true
assert_contract_value "$all_contract" all byooSupportEnabled true
assert_contract_value "$all_contract" all nvcaCollectorEnabled false
assert_contract_value "$all_contract" all autoscalerIntegrationEnabled true

HELMFILE_ENV=local helmfile \
  --file "$stack_dir/helmfile.d" \
  --environment default \
  --state-values-set observability.profile=control \
  --selector name=observability-contract \
  template --output-dir "$work_dir/bundled-contract" >/dev/null

bundled_contract="$(find "$work_dir/bundled-contract" -type f -name '*.yaml' -print)"
grep -q 'TIMESERIES_DB__TIMESERIES_DB_URL: "http://vmsingle.monitoring.svc.cluster.local:8428"' \
  $bundled_contract ||
  fail "control profile did not publish the bundled PromQL endpoint"
grep -q 'TIMESERIES_DB__AUTH_MODE: "none"' $bundled_contract ||
  fail "control profile did not publish the bundled backend auth mode"

HELMFILE_ENV=local helmfile \
  --file "$stack_dir/helmfile.d" \
  --environment default \
  --state-values-set observability.profile=control \
  --state-values-set observability.components.otelOperator.mode=existing \
  --state-values-set metricsBackend.mode=existing \
  --state-values-set metricsBackend.type=external \
  --state-values-set-string metricsBackend.remoteWriteEndpoint=https://metrics.example.com/write \
  --state-values-set-string metricsBackend.promqlEndpoint=https://metrics.example.com \
  --selector name=otel-collector \
  template --output-dir "$work_dir/external" >/dev/null

external_manifests="$(find "$work_dir/external" -type f -name '*.yaml' -print)"
grep -q 'endpoint: https://metrics.example.com/write' $external_manifests ||
  fail "external remote-write endpoint was not applied to the collector"

HELMFILE_ENV=local helmfile \
  --file "$stack_dir/helmfile.d" \
  --environment default \
  --state-values-set observability.profile=control \
  --state-values-set observability.components.otelOperator.mode=existing \
  --state-values-set metricsBackend.mode=existing \
  --state-values-set metricsBackend.type=external \
  --state-values-set-string metricsBackend.remoteWriteEndpoint=https://metrics.example.com/write \
  --state-values-set-string metricsBackend.promqlEndpoint=https://metrics.example.com \
  --selector name=observability-contract \
  template --output-dir "$work_dir/external-contract" >/dev/null

external_contract="$(find "$work_dir/external-contract" -type f -name '*.yaml' -print)"
grep -q 'TIMESERIES_DB__TIMESERIES_DB_URL: "https://metrics.example.com"' \
  $external_contract ||
  fail "external PromQL endpoint was not published for the autoscaler"

HELMFILE_ENV=local helmfile \
  --file "$stack_dir/helmfile.d" \
  --environment default \
  --state-values-set observability.profile=control \
  --state-values-set observability.components.otelOperator.mode=existing \
  --state-values-set metricsBackend.mode=existing \
  --state-values-set metricsBackend.type=external \
  --state-values-set-string metricsBackend.remoteWriteEndpoint=https://metrics.example.com/write \
  --state-values-set-string metricsBackend.promqlEndpoint=https://metrics.example.com \
  --state-values-set metricsBackend.authentication.mode=token \
  --state-values-set-string metricsBackend.authentication.authnEndpoint=https://auth.example.com \
  --selector name=observability-contract \
  template --output-dir "$work_dir/token-contract" >/dev/null

token_contract="$(find "$work_dir/token-contract" -type f -name '*.yaml' -print)"
grep -q 'TIMESERIES_DB__AUTH_MODE: "token"' $token_contract ||
  fail "token auth mode was not published for the autoscaler"
grep -q 'TIMESERIES_DB__AUTHN_URL: "https://auth.example.com"' $token_contract ||
  fail "token auth endpoint was not published for the autoscaler"

if HELMFILE_ENV=local helmfile \
  --file "$stack_dir/helmfile.d" \
  --environment default \
  --state-values-set observability.profile=invalid \
  list >"$work_dir/invalid-profile.log" 2>&1; then
  fail "invalid profile was accepted"
fi
grep -q 'observability.profile must be disabled, control, compute, or all' \
  "$work_dir/invalid-profile.log" ||
  fail "invalid profile did not return the expected error"

if HELMFILE_ENV=local helmfile \
  --file "$stack_dir/helmfile.d" \
  --environment default \
  --state-values-set observability.profile=disabled \
  --state-values-set observability.components.collector.mode=install \
  list >"$work_dir/disabled-override.log" 2>&1; then
  fail "disabled profile accepted an install override"
fi
grep -q 'cannot be "install" when observability.profile=disabled' \
  "$work_dir/disabled-override.log" ||
  fail "disabled install override did not return the expected error"

if HELMFILE_ENV=local helmfile \
  --file "$stack_dir/helmfile.d" \
  --environment default \
  --state-values-set observability.profile=control \
  --state-values-set metricsBackend.authentication.mode=invalid \
  list >"$work_dir/invalid-auth-mode.log" 2>&1; then
  fail "invalid metrics backend auth mode was accepted"
fi
grep -q 'metricsBackend.authentication.mode must be none, token, or mtls' \
  "$work_dir/invalid-auth-mode.log" ||
  fail "invalid metrics backend auth mode did not return the expected error"

echo "profile-defaults: all checks passed"
