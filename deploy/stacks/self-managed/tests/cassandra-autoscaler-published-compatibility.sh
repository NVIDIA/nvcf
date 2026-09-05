#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

: "${NVCF_PUBLISHED_CHART_REGISTRY:?NVCF_PUBLISHED_CHART_REGISTRY is required}"
: "${NVCF_PUBLISHED_CHART_REPOSITORY:?NVCF_PUBLISHED_CHART_REPOSITORY is required}"

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
stacks_dir="$(cd "$stack_dir/.." && pwd)"
work_dir="$(mktemp -d)"
test_stacks_dir="$work_dir/stacks"
test_stack_dir="$test_stacks_dir/self-managed"
environment_name="cassandra-autoscaler-published-compatibility-test"
secrets_file="$test_stack_dir/secrets/$environment_name-secrets.yaml"
cassandra_manifest="$work_dir/cassandra.yaml"
cassandra_release="$work_dir/cassandra-release.json"
autoscaler_manifest="$work_dir/function-autoscaler.yaml"
autoscaler_release="$work_dir/function-autoscaler-release.json"
# These literals are the compatibility contract under test. Review and advance
# them together only after the autoscaler no longer uses the removed tables.
cassandra_chart_version=0.20.3
autoscaler_chart_version=0.3.0
migrations_image_version=0.17.3
autoscaler_image_version=1.21.0
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "cassandra-autoscaler-published-compatibility: $*" >&2
  exit 1
}

mkdir -p "$test_stacks_dir"
cp -R "$stacks_dir"/. "$test_stacks_dir"
cp "$test_stack_dir/secrets/secrets.yaml.template" "$secrets_file"

for stack_environments in "$test_stacks_dir"/*/environments; do
  test -d "$stack_environments" || continue
  test -e "$stack_environments/$environment_name.yaml" ||
    printf '{}\n' >"$stack_environments/$environment_name.yaml"
done

source_args=(
  --state-values-set-string "global.helm.sources.registry=$NVCF_PUBLISHED_CHART_REGISTRY"
  --state-values-set-string "global.helm.sources.repository=$NVCF_PUBLISHED_CHART_REPOSITORY"
)
values_args=(
  --state-values-set-string "global.image.registry=$NVCF_PUBLISHED_CHART_REGISTRY"
  --state-values-set-string "global.image.repository=$NVCF_PUBLISHED_CHART_REPOSITORY"
  --state-values-set ingress.gatewayApi.controllerNamespace=gateway
  --state-values-set ingress.gatewayApi.gateways.shared.name=shared-gw
  --state-values-set ingress.gatewayApi.gateways.shared.namespace=gateway
  --state-values-set ingress.gatewayApi.gateways.grpc.name=grpc-gw
  --state-values-set ingress.gatewayApi.gateways.grpc.namespace=gateway
)

render_release() {
  local state_file="$1"
  local release_name="$2"
  local release_file="$3"
  local manifest_file="$4"
  shift 4

  (cd "$test_stack_dir" &&
    HELMFILE_ENV="$environment_name" \
      HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" \
      helmfile \
        --file "$state_file" \
        --environment default \
        "${source_args[@]}" \
        "${values_args[@]}" \
        "$@" \
        --selector "name=$release_name" \
        list --skip-charts --output json >"$release_file")

  (cd "$test_stack_dir" &&
    HELMFILE_ENV="$environment_name" \
      HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" \
      helmfile \
        --file "$state_file" \
        --environment default \
        "${source_args[@]}" \
        "${values_args[@]}" \
        "$@" \
        --selector "name=$release_name" \
        template >"$manifest_file")
}

render_release \
  helmfile.d/01-dependencies.yaml.gotmpl \
  cassandra \
  "$cassandra_release" \
  "$cassandra_manifest"

test "$(yq -r '.[0].chart' "$cassandra_release")" = \
  'nvcf/helm-nvcf-cassandra' ||
  fail "dependency Helmfile selected the wrong Cassandra chart"
test "$(yq -r '.[0].version' "$cassandra_release")" = \
  "$cassandra_chart_version" ||
  fail "dependency Helmfile did not select Cassandra $cassandra_chart_version"

migrations_image="$(yq ea -r '
  select(.kind == "Job") |
  .spec.template.spec.containers[] |
  select(.name == "nvcf-cassandra-migrations") |
  .image
' "$cassandra_manifest")"
expected_migrations_image="${NVCF_PUBLISHED_CHART_REGISTRY}/${NVCF_PUBLISHED_CHART_REPOSITORY}/nvcf-cassandra-migrations:${migrations_image_version}"
test "$migrations_image" = "$expected_migrations_image" ||
  fail "published Cassandra chart rendered migrations image $migrations_image, expected $expected_migrations_image"

render_release \
  helmfile.d/03-observability.yaml.gotmpl \
  function-autoscaler \
  "$autoscaler_release" \
  "$autoscaler_manifest" \
  --state-values-set observability.profile=control \
  --state-values-set stateMetrics.enabled=true

test "$(yq -r '.[0].chart' "$autoscaler_release")" = \
  'nvcf/helm-nvcf-function-autoscaler' ||
  fail "observability Helmfile selected the wrong function autoscaler chart"
test "$(yq -r '.[0].version' "$autoscaler_release")" = \
  "$autoscaler_chart_version" ||
  fail "observability Helmfile did not select function autoscaler $autoscaler_chart_version"

autoscaler_image="$(yq ea -r '
  select(.kind == "Deployment" and .metadata.name == "function-autoscaler") |
  .spec.template.spec.containers[] |
  select(.name == "function-autoscaler") |
  .image
' "$autoscaler_manifest")"
expected_autoscaler_image="${NVCF_PUBLISHED_CHART_REGISTRY}/${NVCF_PUBLISHED_CHART_REPOSITORY}/nvcf-function-autoscaler:${autoscaler_image_version}"
test "$autoscaler_image" = "$expected_autoscaler_image" ||
  fail "published autoscaler chart rendered image $autoscaler_image, expected $expected_autoscaler_image"

echo "cassandra-autoscaler-published-compatibility: all checks passed"
