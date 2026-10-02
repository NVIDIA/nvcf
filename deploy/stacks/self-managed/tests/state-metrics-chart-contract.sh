#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
repo_dir="$(cd "$stack_dir/../../.." && pwd)"
work_dir="$(mktemp -d)"
test_stack_dir="$work_dir/self-managed"
environment_name="state-metrics-chart-contract-test"
environment_file="$test_stack_dir/environments/$environment_name.yaml"
secrets_file="$test_stack_dir/secrets/$environment_name-secrets.yaml"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "state-metrics-chart-contract: $*" >&2
  exit 1
}

chart_version="$(awk '
  $1 == "-" && $2 == "name:" { release = $3 }
  release == "state-metrics" && $1 == "version:" { print $2; exit }
' "$stack_dir/helmfile.d/03-observability.yaml.gotmpl")"
test -n "$chart_version" ||
  fail "could not derive the state-metrics chart version from the stack Helmfile"

if test -n "${NVCF_STATE_METRICS_CHART:-}"; then
  state_metrics_chart="$NVCF_STATE_METRICS_CHART"
else
  : "${NVCF_PUBLISHED_CHART_REGISTRY:?NVCF_PUBLISHED_CHART_REGISTRY is required}"
  : "${NVCF_PUBLISHED_CHART_REPOSITORY:?NVCF_PUBLISHED_CHART_REPOSITORY is required}"
  state_metrics_chart="oci://${NVCF_PUBLISHED_CHART_REGISTRY}/${NVCF_PUBLISHED_CHART_REPOSITORY}/helm-nvcf-state-metrics"
fi

cp -R "$stack_dir" "$test_stack_dir"
printf '{}\n' >"$environment_file"
cp "$test_stack_dir/secrets/secrets.yaml.template" "$secrets_file"

state_metrics_values="$work_dir/state-metrics-values.yaml"
HELMFILE_ENV="$environment_name" \
  HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" \
  helmfile \
    --file "$test_stack_dir/helmfile.d/03-observability.yaml.gotmpl" \
    --environment default \
    --state-values-set ingress.gatewayApi.gateways.shared.name=shared-gw \
    --state-values-set ingress.gatewayApi.gateways.shared.namespace=envoy-gateway-system \
    --state-values-set ingress.gatewayApi.gateways.grpc.name=grpc-gw \
    --state-values-set ingress.gatewayApi.gateways.grpc.namespace=envoy-gateway-system \
    --state-values-set observability.profile=control \
    --selector name=state-metrics \
    write-values \
    --output-file-template "$state_metrics_values" >/dev/null

test -s "$state_metrics_values" || fail "helmfile wrote no state-metrics values"

expected_audience="$(
  bash -c '
    source "$1"
    generate_jwt_auth_role nvcf-state-metrics nvcf services-all-kv-ro
  ' _ "$repo_dir/migrations/openbao/migrations/utils/functions.sh" |
    yq -p=json -r '.bound_audiences[0]'
)"
test -n "$expected_audience" && test "$expected_audience" != "null" ||
  fail "OpenBao migration helper generated no state-metrics audience"

state_metrics_manifest="$work_dir/state-metrics-manifest.yaml"
helm template state-metrics "$state_metrics_chart" \
  --version "$chart_version" \
  --namespace nvcf \
  --values "$state_metrics_values" >"$state_metrics_manifest" ||
  fail "state-metrics chart $chart_version did not render"

manifest_audiences="$(yq ea -r '
  select(.kind == "Deployment") |
  .spec.template.spec.volumes[] |
  select(.name == "token") |
  .projected.sources[] |
  .serviceAccountToken.audience
' "$state_metrics_manifest")"
test "$manifest_audiences" = "$expected_audience" ||
  fail "rendered chart audience $manifest_audiences does not match migration audience $expected_audience"

tmp_volume_count="$(yq ea -r '
  select(.kind == "Deployment") |
  [.spec.template.spec.volumes[] |
    select(.name == "tmp" and (.emptyDir | type) == "!!map")] |
  length
' "$state_metrics_manifest")"
test "$tmp_volume_count" = "1" ||
  fail "rendered chart did not preserve its tmp emptyDir volume"

echo "state-metrics-chart-contract: all checks passed"
