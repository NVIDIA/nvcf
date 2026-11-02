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
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "openbao-published-chart: $*" >&2
  exit 1
}

published_chart_version="$(awk '
  $1 == "-" && $2 == "name:" { release = $3 }
  release == "openbao-server" && $1 == "version:" { print $2; exit }
' "$stack_dir/helmfile.d/01-dependencies.yaml.gotmpl")"
test -n "$published_chart_version" ||
  fail "could not derive the published OpenBao version from the stack Helmfile"

# A downloaded release package permits the same assertions without registry access.
if test -n "${NVCF_PUBLISHED_CHART_DIR:-}"; then
  published_chart="$NVCF_PUBLISHED_CHART_DIR/helm-nvcf-openbao-server-$published_chart_version.tgz"
else
  : "${NVCF_PUBLISHED_CHART_REGISTRY:?NVCF_PUBLISHED_CHART_REGISTRY is required}"
  : "${NVCF_PUBLISHED_CHART_REPOSITORY:?NVCF_PUBLISHED_CHART_REPOSITORY is required}"
  helm pull "oci://${NVCF_PUBLISHED_CHART_REGISTRY}/${NVCF_PUBLISHED_CHART_REPOSITORY}/helm-nvcf-openbao-server" \
    --version "$published_chart_version" --destination "$work_dir"
  published_chart="$work_dir/helm-nvcf-openbao-server-$published_chart_version.tgz"
fi
test -s "$published_chart" || fail "missing published chart package: $published_chart"
helm show chart "$published_chart" >"$work_dir/chart.yaml"
test "$(yq -r '.name' "$work_dir/chart.yaml")" = helm-nvcf-openbao-server ||
  fail "package name does not match the OpenBao chart"
test "$(yq -r '.version' "$work_dir/chart.yaml")" = "$published_chart_version" ||
  fail "package version does not match the stack pin"
helm show values "$published_chart" >"$work_dir/chart-values.yaml"

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
    --file "$test_stack_dir/helmfile.d/01-dependencies.yaml.gotmpl" \
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
expected_migrations_tag="$(yq -r '.openbao.migrations.image.tag // ""' "$openbao_values")"
if test -z "$expected_migrations_tag"; then
  expected_migrations_tag="$(yq -r '.openbao.migrations.image.tag' "$work_dir/chart-values.yaml")"
fi
expected_migrations_image="$(yq -r '.openbao.migrations.image | .registry + "/" + .repository' "$openbao_values"):$expected_migrations_tag"

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
if test -z "$expected_password" || test "$expected_password" = 'null'; then
  fail "rendered stack values contain no DEFAULT_CASSANDRA_PASSWORD"
fi

published_manifest="$work_dir/openbao-published-manifest.yaml"
helm template openbao-server "$published_chart" \
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

migrations_image="$(yq -r '
  .spec.template.spec.containers[] |
  select(.name == "bao-migrations") |
  .image
' "$published_manifest")"
test "$migrations_image" = "$expected_migrations_image" ||
  fail "published OpenBao chart rendered migrations image $migrations_image, expected $expected_migrations_image"

echo "openbao-published-chart: all checks passed"
