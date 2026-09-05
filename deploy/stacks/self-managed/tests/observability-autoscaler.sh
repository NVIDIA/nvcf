#!/usr/bin/env bash
set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
repo_dir="$(cd "$stack_dir/../../.." && pwd)"
work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "observability-autoscaler: $*" >&2
  exit 1
}

helmfile_args=(
  --file "$stack_dir/helmfile.d"
  --environment default
  --allow-no-matching-release
)

state_values=(
  --state-values-set ingress.gatewayApi.gateways.shared.name=shared
  --state-values-set ingress.gatewayApi.gateways.shared.namespace=envoy
  --state-values-set ingress.gatewayApi.gateways.grpc.name=grpc
  --state-values-set ingress.gatewayApi.gateways.grpc.namespace=envoy
)

release_count() {
  local profile="$1"
  local release="$2"

  HELMFILE_ENV=base helmfile \
    "${helmfile_args[@]}" \
    "${state_values[@]}" \
    --state-values-set "observability.profile=$profile" \
    list 2>/dev/null |
    awk -v release="$release" 'NR > 1 && $1 == release {count++} END {print count + 0}'
}

for profile in control compute all; do
  test "$(release_count "$profile" victoria-metrics)" = "1" ||
    fail "$profile profile did not install exactly one shared metrics backend"
done

test "$(release_count disabled victoria-metrics)" = "0" ||
  fail "disabled profile installed the shared metrics backend"
test "$(release_count control function-autoscaler)" = "1" ||
  fail "control profile did not install exactly one function autoscaler"
test "$(release_count all function-autoscaler)" = "1" ||
  fail "all profile did not install exactly one function autoscaler"
test "$(release_count compute function-autoscaler)" = "0" ||
  fail "compute profile installed the control-plane function autoscaler"
test "$(release_count disabled function-autoscaler)" = "0" ||
  fail "disabled profile installed the function autoscaler"

test_stack_dir="$work_dir/self-managed"
cp -R "$stack_dir" "$test_stack_dir"
cp "$test_stack_dir/secrets/secrets.yaml.template" \
  "$test_stack_dir/secrets/base-secrets.yaml"

HELMFILE_ENV=base HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" helmfile \
  --file "$test_stack_dir/helmfile.d/03-observability.yaml.gotmpl" \
  --environment default \
  "${state_values[@]}" \
  --state-values-set observability.profile=control \
  --state-values-set-string stateMetrics.resources.requests.cpu=100m \
  --state-values-set-string stateMetrics.resources.requests.memory=128Mi \
  --state-values-set-string stateMetrics.resources.limits.cpu=500m \
  --state-values-set-string stateMetrics.resources.limits.memory=512Mi \
  --selector name=state-metrics \
  write-values \
  --output-file-template "$work_dir/state-metrics-values.yaml" >/dev/null

for assertion in \
  '.stateMetrics.resources.requests.cpu == "100m"' \
  '.stateMetrics.resources.requests.memory == "128Mi"' \
  '.stateMetrics.resources.limits.cpu == "500m"' \
  '.stateMetrics.resources.limits.memory == "512Mi"'; do
  yq -e "$assertion" "$work_dir/state-metrics-values.yaml" >/dev/null ||
    fail "state-metrics resource override was not passed to the chart: $assertion"
done

write_autoscaler_values() {
  local output_file="$1"
  shift

  HELMFILE_ENV=base HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" helmfile \
    --file "$stack_dir/helmfile.d/03-observability.yaml.gotmpl" \
    --environment default \
    "${state_values[@]}" \
    --state-values-set observability.profile=control \
    "$@" \
    --selector name=function-autoscaler \
    write-values \
    --output-file-template "$output_file" >/dev/null
}

write_autoscaler_values "$work_dir/autoscaler-values.yaml"

autoscaler_values="$work_dir/autoscaler-values.yaml"
autoscaler_tag="$(yq -r '.functionautoscaler.image.tag // ""' "$autoscaler_values")"
expected_autoscaler_tag="$(yq -r '.functionAutoscaler.image.tag // ""' "$stack_dir/environments/base.yaml")"
[[ -n "$expected_autoscaler_tag" ]] ||
  fail "base environment does not pin the function autoscaler image"
test "$autoscaler_tag" = "$expected_autoscaler_tag" ||
  fail "rendered autoscaler image tag is $autoscaler_tag, expected $expected_autoscaler_tag"
for expected in \
  'CASSANDRA__CONTACT_POINTS: cassandra.cassandra-system.svc.cluster.local' \
  'CASSANDRA__IS_DEVELOPMENT: "false"' \
  'NVCF_API__NVCF_API_GRPC_ADDRESS: http://api.nvcf.svc.cluster.local:9090' \
  'NVCF_API__DISABLE_AUTH: "false"' \
  'NVCF_API__DRY_RUN: "false"' \
  'TIMESERIES_DB__TIMESERIES_DB_URL: http://vmsingle.monitoring.svc.cluster.local:8428' \
  'TIMESERIES_DB__AUTH_MODE: none' \
  'TIMESERIES_DB__IGNORE_ENV: "true"'; do
  grep -q "$expected" "$autoscaler_values" ||
    fail "control profile did not render autoscaler value: $expected"
done

helm template function-autoscaler "$repo_dir/deploy/helm/function-autoscaler" \
  --namespace nvcf \
  --values "$autoscaler_values" \
  >"$work_dir/autoscaler-manifests.yaml"

autoscaler_manifests="$work_dir/autoscaler-manifests.yaml"
grep -q "image: nvcr.io/YOUR_ORG/YOUR_TEAM/nvcf-function-autoscaler:${expected_autoscaler_tag}" "$autoscaler_manifests" ||
  fail "self-managed stack did not pin the autoscaler image to $expected_autoscaler_tag"
test "$(grep -c '^kind: ConfigMap$' "$autoscaler_manifests")" = "2" ||
  fail "autoscaler chart did not render only its env and Vault template ConfigMaps"
grep -q 'name: function-autoscaler-env' "$autoscaler_manifests" ||
  fail "autoscaler chart did not render its chart-owned env ConfigMap"
if grep -q 'nvcf-observability-autoscaler\|nvcf-observability-profile' "$autoscaler_manifests"; then
  fail "autoscaler chart rendered a redundant observability contract ConfigMap"
fi
grep -q 'NVCF_API__DRY_RUN: "false"' "$autoscaler_manifests" ||
  fail "autoscaler chart did not render the self-managed runtime configuration"
grep -q 'TIMESERIES_DB__TIMESERIES_DB_URL: "http://vmsingle.monitoring.svc.cluster.local:8428"' \
  "$autoscaler_manifests" ||
  fail "autoscaler chart did not consolidate the bundled PromQL endpoint into its env ConfigMap"
grep -q '"helm.sh/hook": test' "$autoscaler_manifests" ||
  fail "autoscaler chart lost its runtime helm test hook"

write_autoscaler_values "$work_dir/external-autoscaler-values.yaml" \
  --state-values-set metricsBackend.mode=existing \
  --state-values-set metricsBackend.type=external \
  --state-values-set-string metricsBackend.promqlEndpoint=https://metrics.example.com \
  --state-values-set metricsBackend.authentication.mode=token \
  --state-values-set-string metricsBackend.authentication.authnEndpoint=https://auth.example.com \
  --state-values-set functionAutoscaler.timeseriesDb.ignoreEnv=false

external_autoscaler_values="$work_dir/external-autoscaler-values.yaml"
for expected in \
  'TIMESERIES_DB__TIMESERIES_DB_URL: https://metrics.example.com' \
  'TIMESERIES_DB__AUTH_MODE: token' \
  'TIMESERIES_DB__AUTHN_URL: https://auth.example.com' \
  'TIMESERIES_DB__IGNORE_ENV: "false"'; do
  grep -q "$expected" "$external_autoscaler_values" ||
    fail "external backend did not render autoscaler value: $expected"
done

write_autoscaler_values "$work_dir/mtls-autoscaler-values.yaml" \
  --state-values-set metricsBackend.mode=existing \
  --state-values-set metricsBackend.type=external \
  --state-values-set-string metricsBackend.promqlEndpoint=https://metrics.example.com \
  --state-values-set metricsBackend.authentication.mode=mtls \
  --state-values-set-string metricsBackend.authentication.clientCertificatePath=/tls/client.crt \
  --state-values-set-string metricsBackend.authentication.clientPrivateKeyPath=/tls/client.key

mtls_autoscaler_values="$work_dir/mtls-autoscaler-values.yaml"
for expected in \
  'TIMESERIES_DB__AUTH_MODE: mtls' \
  'TIMESERIES_DB__CLIENT_CERTIFICATE_PATH: /tls/client.crt' \
  'TIMESERIES_DB__CLIENT_PRIVATE_KEY_PATH: /tls/client.key'; do
  grep -q "$expected" "$mtls_autoscaler_values" ||
    fail "mTLS backend did not render autoscaler value: $expected"
done

if HELMFILE_ENV=base helmfile \
  "${helmfile_args[@]}" \
  "${state_values[@]}" \
  --state-values-set observability.profile=invalid \
  list >"$work_dir/invalid-profile.log" 2>&1; then
  fail "invalid profile was accepted by the self-managed stack"
fi
grep -q 'observability.profile must be disabled, control, compute, or all' \
  "$work_dir/invalid-profile.log" ||
  fail "invalid self-managed profile did not return the expected error"

echo "observability-autoscaler: all checks passed"
