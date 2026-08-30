#!/usr/bin/env bash
# Verify that the self-managed secrets template gives Cassandra migrations and
# OpenBao migrations the same non-empty application-role password.
set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
stacks_dir="$(cd "$stack_dir/.." && pwd)"
helm_dir="$(cd "$stack_dir/../../helm" && pwd)"
work_dir="$(mktemp -d)"
test_stacks_dir="$work_dir/stacks"
test_stack_dir="$test_stacks_dir/self-managed"
environment_name="cassandra-openbao-credential-wiring-test"
secrets_file="$test_stack_dir/secrets/$environment_name-secrets.yaml"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "cassandra-openbao-credential-wiring: $*" >&2
  exit 1
}

# Exercise the real stack and its shipped secrets template from an isolated
# copy. The sibling stacks are needed because helmfile.d references them.
mkdir -p "$test_stacks_dir"
cp -R "$stacks_dir"/. "$test_stacks_dir"
cp "$test_stack_dir/secrets/secrets.yaml.template" "$secrets_file"

for stack_environments in "$test_stacks_dir"/*/environments; do
  test -d "$stack_environments" || continue
  test -e "$stack_environments/$environment_name.yaml" ||
    printf '{}\n' >"$stack_environments/$environment_name.yaml"
done

helmfile_common=(
  --file "$test_stack_dir/helmfile.d"
  --environment default
  --state-values-set ingress.gatewayApi.controllerNamespace=envoy-gateway-system
  --state-values-set ingress.gatewayApi.gateways.shared.name=shared-gw
  --state-values-set ingress.gatewayApi.gateways.shared.namespace=envoy-gateway-system
  --state-values-set ingress.gatewayApi.gateways.grpc.name=grpc-gw
  --state-values-set ingress.gatewayApi.gateways.grpc.namespace=envoy-gateway-system
)

render_chart_values() {
  local release="$1"
  local output_file="$2"
  local render_log="$work_dir/$release-write-values.log"

  if ! HELMFILE_ENV="$environment_name" \
    HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" \
    helmfile "${helmfile_common[@]}" \
      --selector "name=$release" \
      write-values \
      --output-file-template "$output_file" 2>"$render_log"; then
    cat "$render_log" >&2
    fail "$release: helmfile could not render the stack"
  fi

  test -s "$output_file" || fail "$release: helmfile wrote no values"
}

cassandra_values="$work_dir/cassandra-values.yaml"
openbao_values="$work_dir/openbao-values.yaml"
render_chart_values cassandra "$cassandra_values"
render_chart_values openbao-server "$openbao_values"

# Verify that the Helmfile selects the published OpenBao wrapper pin. The
# manifest assertion below intentionally uses the local source chart because
# the unit suite does not have OCI registry credentials.
openbao_release="$(HELMFILE_ENV="$environment_name" \
  HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" \
  helmfile "${helmfile_common[@]}" \
    --selector name=openbao-server \
    list --skip-charts --output json)"
openbao_chart_name="$(jq -r '.[0].chart // ""' <<<"$openbao_release")"
openbao_chart_version="$(jq -r '.[0].version // ""' <<<"$openbao_release")"
test "$openbao_chart_name" = 'nvcf/helm-nvcf-openbao-server' ||
  fail "expected default OpenBao chart, got ${openbao_chart_name:-missing}"
test "$openbao_chart_version" = '0.32.1' ||
  fail "expected default OpenBao version 0.32.1, got ${openbao_chart_version:-missing}"

cassandra_password="$(yq -r '.cassandra.serviceRolePassword // ""' "$cassandra_values")"
test -n "$cassandra_password" ||
  fail "Cassandra migrations received no application-role password"

cassandra_manifest="$work_dir/cassandra-manifest.yaml"
helm template cassandra "$helm_dir/cassandra/helm" \
  --namespace cassandra-system \
  --values "$cassandra_values" >"$cassandra_manifest" ||
  fail "Cassandra chart did not render"
cassandra_job_password="$(yq -r '
  select(.kind == "Job" and .metadata.name == "cassandra-migrations") |
  .spec.template.spec.containers[].env[] |
  select(.name == "SERVICE_ROLE_PASSWORD") |
  .value
' "$cassandra_manifest")"

test -n "$cassandra_job_password" ||
  fail "Cassandra migration Job received no application-role password"
test "$cassandra_job_password" = "$cassandra_password" ||
  fail "Cassandra migration Job did not receive the rendered stack password"

# The wrapper chart's migration Job does not depend on upstream OpenBao
# templates. Supply an empty dependency chart in the isolated copy so this
# focused render stays offline while exercising the real wrapper template.
openbao_chart="$work_dir/openbao-chart"
cp -R "$helm_dir/openbao/helm" "$openbao_chart"
mkdir -p "$openbao_chart/charts/openbao"
openbao_dependency_version="$(yq -r '
  .dependencies[] |
  select(.name == "openbao") |
  .version
' "$openbao_chart/Chart.yaml")"
test -n "$openbao_dependency_version" ||
  fail "OpenBao wrapper chart has no OpenBao dependency version"
printf '%s\n' \
  'apiVersion: v2' \
  'name: openbao' \
  "version: $openbao_dependency_version" >"$openbao_chart/charts/openbao/Chart.yaml"

openbao_manifest="$work_dir/openbao-manifest.yaml"
helm template openbao-server "$openbao_chart" \
  --namespace vault-system \
  --values "$openbao_values" \
  --show-only templates/hook-post-02-migrations.yaml >"$openbao_manifest" ||
  fail "OpenBao migration Job did not render"
openbao_job_password_count="$(yq -r '
  [.spec.template.spec.containers[] |
    select(.name == "bao-migrations") |
    .env[] |
    select(.name == "DEFAULT_CASSANDRA_PASSWORD")] |
  length
' "$openbao_manifest")"
test "$openbao_job_password_count" = "1" ||
  fail "OpenBao migration Job must receive exactly one application-role password"
openbao_job_password="$(yq -r '
  .spec.template.spec.containers[] |
  select(.name == "bao-migrations") |
  .env[] |
  select(.name == "DEFAULT_CASSANDRA_PASSWORD") |
  .value
' "$openbao_manifest")"

test -n "$openbao_job_password" ||
  fail "OpenBao migration Job received no application-role password"
test "$cassandra_job_password" = "$openbao_job_password" ||
  fail "Cassandra and OpenBao migrations received different application-role passwords"

echo "cassandra-openbao-credential-wiring: all checks passed"
