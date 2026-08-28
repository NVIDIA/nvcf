#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
stacks_dir="$(cd "$stack_dir/.." && pwd)"
work_dir="$(mktemp -d)"
test_stacks_dir="$work_dir/stacks"
test_stack_dir="$test_stacks_dir/self-managed"
environment_name="openbao-published-chart-test"
secrets_file="$test_stack_dir/secrets/$environment_name-secrets.yaml"
: "${NVCF_PUBLISHED_CHART_REGISTRY:?NVCF_PUBLISHED_CHART_REGISTRY is required}"
: "${NVCF_PUBLISHED_CHART_REPOSITORY:?NVCF_PUBLISHED_CHART_REPOSITORY is required}"
published_chart_registry="$NVCF_PUBLISHED_CHART_REGISTRY"
published_chart_repository="$NVCF_PUBLISHED_CHART_REPOSITORY"
published_chart_version=0.32.1
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "openbao-published-chart: $*" >&2
  exit 1
}

# Match the existing Cassandra/OpenBao credential-wiring test's rendered stack
# values, but keep that local-chart test offline and independent of registry
# availability.
mkdir -p "$test_stacks_dir"
cp -R "$stacks_dir"/. "$test_stacks_dir"
cp "$test_stack_dir/secrets/secrets.yaml.template" "$secrets_file"

for stack_environments in "$test_stacks_dir"/*/environments; do
  test -d "$stack_environments" || continue
  test -e "$stack_environments/$environment_name.yaml" ||
    printf '{}\n' >"$stack_environments/$environment_name.yaml"
done

openbao_values="$work_dir/openbao-values.yaml"
openbao_release="$work_dir/openbao-release.json"
HELMFILE_ENV="$environment_name" \
  HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" \
  helmfile \
    --file "$test_stack_dir/helmfile.d" \
    --environment default \
    --state-values-set ingress.gatewayApi.controllerNamespace=envoy-gateway-system \
    --state-values-set ingress.gatewayApi.gateways.shared.name=shared-gw \
    --state-values-set ingress.gatewayApi.gateways.shared.namespace=envoy-gateway-system \
    --state-values-set ingress.gatewayApi.gateways.grpc.name=grpc-gw \
    --state-values-set ingress.gatewayApi.gateways.grpc.namespace=envoy-gateway-system \
    --selector name=openbao-server \
    write-values \
    --output-file-template "$openbao_values" >/dev/null

test -s "$openbao_values" || fail "helmfile wrote no OpenBao values"

HELMFILE_ENV="$environment_name" \
  HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" \
  helmfile \
    --file "$test_stack_dir/helmfile.d/01-dependencies.yaml.gotmpl" \
    --environment default \
    --selector name=openbao-server \
    list --skip-charts --output json >"$openbao_release"

test "$(yq -r '.[0].chart' "$openbao_release")" = \
  'nvcf/helm-nvcf-openbao-server' ||
  fail "dependency Helmfile selected the wrong OpenBao chart"
test "$(yq -r '.[0].version' "$openbao_release")" = \
  "$published_chart_version" ||
  fail "dependency Helmfile did not select OpenBao $published_chart_version"

expected_password="$(yq -r '
  .openbao.migrations.env[] |
  select(.name == "DEFAULT_CASSANDRA_PASSWORD") |
  .value
' "$openbao_values")"
test -n "$expected_password" && test "$expected_password" != 'null' ||
  fail "rendered stack values contain no DEFAULT_CASSANDRA_PASSWORD"

published_manifest="$work_dir/openbao-published-manifest.yaml"
published_chart="oci://${published_chart_registry}/${published_chart_repository}/helm-nvcf-openbao-server"
helm template openbao-server "$published_chart" \
  --version "$published_chart_version" \
  --namespace vault-system \
  --values "$openbao_values" \
  --show-only templates/hook-post-02-migrations.yaml >"$published_manifest" ||
  fail "published OpenBao $published_chart_version chart did not render"

password_count="$(yq -r '
  [.spec.template.spec.containers[] |
    select(.name == "bao-migrations") |
    .env[] |
    select(.name == "DEFAULT_CASSANDRA_PASSWORD")] |
  length
' "$published_manifest")"
test "$password_count" = '1' ||
  fail "published OpenBao chart must render exactly one DEFAULT_CASSANDRA_PASSWORD"

password="$(yq -r '
  .spec.template.spec.containers[] |
  select(.name == "bao-migrations") |
  .env[] |
  select(.name == "DEFAULT_CASSANDRA_PASSWORD") |
  .value
' "$published_manifest")"
test "$password" = "$expected_password" ||
  fail "published OpenBao chart did not preserve DEFAULT_CASSANDRA_PASSWORD"

echo "openbao-published-chart: all checks passed"
