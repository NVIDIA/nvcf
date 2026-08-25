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

cassandra_password="$(yq -r '.cassandra.serviceRolePassword // ""' "$cassandra_values")"
openbao_password_count="$(yq -r '
  [.openbao.migrations.env[] |
    select(.name == "DEFAULT_CASSANDRA_PASSWORD")] |
  length
' "$openbao_values")"
test "$openbao_password_count" = "1" ||
  fail "OpenBao migrations must receive exactly one application-role password"
openbao_password="$(yq -r '
  .openbao.migrations.env[] |
  select(.name == "DEFAULT_CASSANDRA_PASSWORD") |
  .value
' "$openbao_values")"

test -n "$cassandra_password" ||
  fail "Cassandra migrations received no application-role password"
test -n "$openbao_password" ||
  fail "OpenBao migrations received no application-role password"

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
test "$cassandra_job_password" = "$openbao_password" ||
  fail "Cassandra and OpenBao migrations received different application-role passwords"

echo "cassandra-openbao-credential-wiring: all checks passed"
