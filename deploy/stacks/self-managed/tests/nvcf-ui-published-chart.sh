#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail

: "${NVCF_PUBLISHED_CHART_REGISTRY:?NVCF_PUBLISHED_CHART_REGISTRY is required}"
: "${NVCF_PUBLISHED_CHART_REPOSITORY:?NVCF_PUBLISHED_CHART_REPOSITORY is required}"

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work_dir="$(mktemp -d)"
test_stack_dir="$work_dir/self-managed"
environment_name="nvcf-ui-published-chart-test"
environment_file="$test_stack_dir/environments/$environment_name.yaml"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "nvcf-ui-published-chart: $*" >&2
  exit 1
}

mkdir -p "$test_stack_dir"
cp -R "$stack_dir"/. "$test_stack_dir"
printf '{}\n' >"$test_stack_dir/secrets/$environment_name-secrets.yaml"
cat >"$environment_file" <<'YAML'
addons:
  nvcfUi:
    enabled: true
YAML

run_helmfile() {
  HELMFILE_ENV="$environment_name" \
    HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" \
    helmfile \
      --file "$test_stack_dir/helmfile.d/02-core.yaml.gotmpl" \
      --environment default \
      --state-values-set ingress.gatewayApi.controllerNamespace=envoy-gateway-system \
      --state-values-set ingress.gatewayApi.gateways.shared.name=shared-gw \
      --state-values-set ingress.gatewayApi.gateways.shared.namespace=envoy-gateway-system \
      --state-values-set ingress.gatewayApi.gateways.grpc.name=grpc-gw \
      --state-values-set ingress.gatewayApi.gateways.grpc.namespace=envoy-gateway-system \
      --selector name=nvcf-ui "$@"
}

run_helmfile list --skip-charts --output json >"$work_dir/release.json"
test "$(yq -r '.[0].chart' "$work_dir/release.json")" = nvcf/helm-nvcf-ui ||
  fail "stack selected the wrong UI chart"
chart_version="$(yq -r '.[0].version // ""' "$work_dir/release.json")"
test -n "$chart_version" || fail "stack selected no UI chart version"
namespace="$(yq -r '.[0].namespace' "$work_dir/release.json")"

# Pull once so both cases exercise the same published chart bytes.
helm pull "oci://${NVCF_PUBLISHED_CHART_REGISTRY}/${NVCF_PUBLISHED_CHART_REPOSITORY}/helm-nvcf-ui" \
  --version "$chart_version" --destination "$work_dir"
chart="$work_dir/helm-nvcf-ui-$chart_version.tgz"
migration_container='select(.kind == "Job") | .spec.template.spec.containers[] | select(.name == "openbao-migrations")'

for client in default 1.36; do
  if test "$client" != default; then
    yq -i '.addons.nvcfUi.openbaoMigrations.kubectlMinor = "1.36"' "$environment_file"
  fi
  values_file="$work_dir/$client-values.yaml"
  manifest_file="$work_dir/$client-manifest.yaml"
  run_helmfile write-values --output-file-template "$values_file" >/dev/null
  helm template nvcf-ui "$chart" --namespace "$namespace" \
    --values "$values_file" >"$manifest_file"

  expected_image="$(yq -r '.nvcfUi.openbaoMigrations.image | .registry + "/" + .repository + ":" + .tag' "$values_file")"
  test "$(yq -r "$migration_container | .image" "$manifest_file")" = "$expected_image" ||
    fail "$client: migration Job did not render the selected image"
  actual_client="$(yq -r "$migration_container | .env[] | select(.name == \"NVCF_KUBECTL_MINOR\") | .value" "$manifest_file")"
  if test "$client" = default; then
    test -z "$actual_client" || fail "default client must use the image default"
  else
    test "$actual_client" = "$client" || fail "selected client did not reach the migration Job"
  fi
done

echo "nvcf-ui-published-chart: all checks passed"
