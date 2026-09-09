#!/usr/bin/env bash
set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "profile-defaults: $*" >&2
  exit 1
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

# `show-dag` eagerly prepares the placeholder OCI charts. Debug rendering of
# `list --skip-charts` keeps this test offline while preserving `needs` edges.
render_release_state() {
  local output_name="$1"
  shift
  local log_file="$work_dir/$output_name.log"
  local state_file="$work_dir/$output_name.yaml"

  if ! HELMFILE_ENV=local HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" \
    helmfile \
      --file "$stack_dir/helmfile.d" \
      --log-level debug \
      --environment default \
      "$@" \
      list --skip-charts --output json >"$log_file" 2>&1; then
    cat "$log_file" >&2
    fail "could not render the $output_name Helmfile state"
  fi

  awk '
    /^rendering result of ".*":$/ { print "---"; capture = 1; next }
    capture && /^[[:space:]]*[0-9]+: / {
      sub(/^[[:space:]]*[0-9]+: ?/, "")
      print
      next
    }
    capture { capture = 0 }
  ' "$log_file" >"$state_file"

  test -s "$state_file" || fail "could not recover the $output_name Helmfile state"
}

release_needs() {
  local state_file="$1"
  local release_name="$2"

  NVCF_RELEASE_NAME="$release_name" yq ea -r '
    select(.releases != null) |
    .releases[] |
    select(.name == strenv(NVCF_RELEASE_NAME)) |
    .needs[]?
  ' "$state_file"
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

pod_monitor_template_count="$(
  find "$stack_dir/charts/nvcf-default-monitors/templates" \
    -maxdepth 1 -type f -name '*podmonitor*.yaml' |
    wc -l |
    tr -d ' '
)"
test "$pod_monitor_template_count" = "1" ||
  fail "default monitors chart must use one generic PodMonitor template"

enabled_releases="$(
  cat <<'EOF'
default-monitors
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

render_release_state crds-installed \
  --state-values-set observability.profile=control
test "$(release_needs "$work_dir/crds-installed.yaml" victoria-metrics)" = \
  "monitoring/prometheus-operator-crds" ||
  fail "victoria-metrics must wait for stack-installed Prometheus Operator CRDs"

render_release_state crds-existing \
  --state-values-set observability.profile=control \
  --state-values-set observability.components.prometheusOperatorCrds.mode=existing
test -z "$(release_needs "$work_dir/crds-existing.yaml" victoria-metrics)" ||
  fail "victoria-metrics must not depend on a CRD release the stack does not install"

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
helm template otel-collector "$stack_dir/charts/nvcf-otel-collector" \
  >"$work_dir/chart-collector-defaults.yaml"

control_manifests="$(find "$work_dir/control" -type f -name '*.yaml' -print)"
compute_manifests="$(find "$work_dir/compute" -type f -name '*.yaml' -print)"
all_manifests="$(find "$work_dir/all" -type f -name '*.yaml' -print)"
control_swapped_manifests="$(find "$work_dir/control-swapped" -type f -name '*.yaml' -print)"
compute_swapped_manifests="$(find "$work_dir/compute-swapped" -type f -name '*.yaml' -print)"
compute_worker_disabled_manifests="$(find "$work_dir/compute-worker-disabled" -type f -name '*.yaml' -print)"
chart_control_manifests="$work_dir/chart-control-defaults.yaml"
chart_compute_manifests="$work_dir/chart-compute-defaults.yaml"
worker_monitor_manifest="$work_dir/chart-worker-podmonitor.yaml"
collector_manifests="$work_dir/chart-collector-defaults.yaml"

grep -q '^      - secrets$' "$collector_manifests" ||
  fail "Target Allocator RBAC must allow referenced Secret discovery"

sed -n '/name: nvcf-default-monitors-worker/,$p' \
  "$chart_compute_manifests" >"$worker_monitor_manifest"
grep -q '^    any: true$' "$worker_monitor_manifest" ||
  fail "worker PodMonitor must use a supported all-namespaces selector"
grep -q '^    matchExpressions:$' "$worker_monitor_manifest" ||
  fail "worker PodMonitor must use a pod label expression"
grep -q '^    - key: icms-request-id$' "$worker_monitor_manifest" ||
  fail "worker PodMonitor must select NVCA-managed workload pods"
grep -q '^      operator: Exists$' "$worker_monitor_manifest" ||
  fail "worker PodMonitor label expression must use Exists"

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
