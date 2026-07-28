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

release_namespace() {
  local profile="$1"
  local release="$2"

  HELMFILE_ENV=base helmfile \
    "${helmfile_args[@]}" \
    "${state_values[@]}" \
    --state-values-set "observability.profile=$profile" \
    list 2>/dev/null |
    awk -v release="$release" 'NR > 1 && $1 == release {print $2}'
}

for profile in control compute all; do
  test "$(release_count "$profile" victoria-metrics)" = "1" ||
    fail "$profile profile did not install exactly one shared metrics backend"
done

test "$(release_count disabled victoria-metrics)" = "0" ||
  fail "disabled profile installed the shared metrics backend"
test "$(release_namespace control observability-contract)" = "nvcf" ||
  fail "control profile did not place the contract in the autoscaler namespace"
test "$(release_count control function-autoscaler)" = "1" ||
  fail "control profile did not install exactly one function autoscaler"
test "$(release_count all function-autoscaler)" = "1" ||
  fail "all profile did not install exactly one function autoscaler"
test "$(release_count compute function-autoscaler)" = "0" ||
  fail "compute profile installed the control-plane function autoscaler"
test "$(release_count disabled function-autoscaler)" = "0" ||
  fail "disabled profile installed the function autoscaler"

HELMFILE_ENV=base HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" helmfile \
  --file "$stack_dir/helmfile.d/03-observability.yaml.gotmpl" \
  --environment default \
  "${state_values[@]}" \
  --state-values-set observability.profile=control \
  --selector name=function-autoscaler \
  write-values \
  --output-file-template "$work_dir/autoscaler-values.yaml" >/dev/null

autoscaler_values="$work_dir/autoscaler-values.yaml"
for expected in \
  'CASSANDRA__CONTACT_POINTS: cassandra.cassandra-system.svc.cluster.local' \
  'CASSANDRA__IS_DEVELOPMENT: "false"' \
  'NVCF_API__NVCF_API_GRPC_ADDRESS: http://api.nvcf.svc.cluster.local:9090' \
  'NVCF_API__DISABLE_AUTH: "true"' \
  'NVCF_API__DRY_RUN: "false"' \
  'name: nvcf-observability-autoscaler'; do
  grep -q "$expected" "$autoscaler_values" ||
    fail "control profile did not render autoscaler value: $expected"
done

helm template function-autoscaler "$repo_dir/deploy/helm/function-autoscaler" \
  --namespace nvcf \
  --values "$autoscaler_values" \
  >"$work_dir/autoscaler-manifests.yaml"

autoscaler_manifests="$work_dir/autoscaler-manifests.yaml"
grep -q 'image: nvcr.io/YOUR_ORG/YOUR_TEAM/nvcf-function-autoscaler:1.18.3' "$autoscaler_manifests" ||
  fail "self-managed stack did not pin the autoscaler image"
test "$(grep -c 'name: nvcf-observability-autoscaler' "$autoscaler_manifests")" = "1" ||
  fail "autoscaler chart did not render exactly one external observability envFrom"
grep -q 'NVCF_API__DRY_RUN: "false"' "$autoscaler_manifests" ||
  fail "autoscaler chart did not render the self-managed runtime configuration"
grep -q '"helm.sh/hook": test' "$autoscaler_manifests" ||
  fail "autoscaler chart lost its runtime helm test hook"

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
