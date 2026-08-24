#!/usr/bin/env bash
# Test that podDisruptionBudget values thread correctly from environment files
# through global.yaml.gotmpl into each chart's rendered output, and that the
# chart-level fail validation fires when both or neither availability field is set.
set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
stacks_dir="$(cd "$stack_dir/.." && pwd)"
helm_dir="$(cd "$stack_dir/../../helm" && pwd)"
work_dir="$(mktemp -d)"
test_stacks_dir="$work_dir/stacks"
test_stack_dir="$test_stacks_dir/self-managed"
environment_name="pdb-value-wiring-test"
environment_file="$test_stack_dir/environments/$environment_name.yaml"
secrets_file="$test_stack_dir/secrets/$environment_name-secrets.yaml"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "pdb-value-wiring: $*" >&2
  exit 1
}

# Copy the whole stacks directory, not just self-managed. The self-managed
# helmfile.d pulls in a sibling stack through a relative path, so a copy of
# self-managed alone is not a working stack.
mkdir -p "$test_stacks_dir"
cp -R "$stacks_dir"/. "$test_stacks_dir"
mkdir -p "$(dirname "$secrets_file")"
printf '{}\n' >"$secrets_file"

# Every stack the copy reaches resolves ../environments/$HELMFILE_ENV.yaml
# against its own directory, so each one needs a file under this test's
# environment name.
for stack_environments in "$test_stacks_dir"/*/environments; do
  test -d "$stack_environments" || continue
  test -e "$stack_environments/$environment_name.yaml" ||
    printf '{}\n' >"$stack_environments/$environment_name.yaml"
done

# Run against the whole helmfile.d directory, the way the stack is actually
# applied. Selecting a release out of one hand-picked state file only works
# until that release moves, and the failure then looks like the release does
# not exist rather than like the state file is wrong.
#
# global.yaml.gotmpl marks the shared gateway values as required, so every
# invocation has to supply them, including the ones that never render a route.
helmfile_common=(
  --file "$test_stack_dir/helmfile.d"
  --environment default
  --state-values-set ingress.gatewayApi.controllerNamespace=envoy-gateway-system
  --state-values-set ingress.gatewayApi.gateways.shared.name=shared-gw
  --state-values-set ingress.gatewayApi.gateways.shared.namespace=envoy-gateway-system
  --state-values-set ingress.gatewayApi.gateways.grpc.name=grpc-gw
  --state-values-set ingress.gatewayApi.gateways.grpc.namespace=envoy-gateway-system
)

run_helmfile() {
  HELMFILE_ENV="$environment_name" \
    HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" \
    helmfile "${helmfile_common[@]}" "$@"
}

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

# Guards the defect this harness previously hid: a selector that names a
# release the selected state does not declare. One match is the only correct
# answer. Zero means the release was renamed, removed, or looked for in the
# wrong place. More than one means two states declare it, and a later apply
# silently reinstalls whatever the earlier one decided.
assert_single_release() {
  local release="$1"

  # helmfile exits non-zero both when a selector matches nothing and when the
  # stack fails to render. Only the first is this check's business. Reporting a
  # render failure as a missing release is the same misdirection this test was
  # fixed for, so let it surface as itself.
  local list_log="$work_dir/$release-list.log"
  local listed status=0
  listed="$(run_helmfile --selector "name=$release" list --skip-charts --output json 2>"$list_log")" ||
    status=$?
  if test "$status" -ne 0 && ! grep -Fq "no releases found" "$list_log"; then
    cat "$list_log" >&2
    fail "$release: helmfile could not render the stack"
  fi

  local matches
  matches="$(printf '%s' "${listed:-[]}" |
    jq -r --arg name "$release" '[.[] | select(.name == $name)] | length' 2>/dev/null || true)"
  test "${matches:-0}" = "1" ||
    fail "expected exactly one release named $release in the stack, found ${matches:-0}"
}

render_chart_values() {
  local release="$1"
  local output_file="$2"
  shift 2

  local render_log="$work_dir/$release-write-values.log"
  if ! run_helmfile \
    --selector "name=$release" \
    "$@" \
    write-values \
    --output-file-template "$output_file" 2>"$render_log"; then
    cat "$render_log" >&2
    fail "$release: helmfile could not render the stack"
  fi

  test -s "$output_file" ||
    fail "$release: helmfile wrote no values to $output_file"
}

# Renders a release against its in-repo chart so the chart's own validation is
# what fails. Without --chart the run fails on a chart pull instead, which
# looks the same to a test that only checks for a non-zero exit.
expect_chart_error() {
  local release="$1"
  local chart_dir="$2"
  local log_file="$3"
  local expected_error="$4"
  local forbidden_error="${5:-}"

  if run_helmfile \
    --selector "name=$release" \
    --chart "$chart_dir" \
    --skip-deps \
    template >"$log_file" 2>&1; then
    fail "$release: expected the chart to reject this configuration"
  fi

  grep -Fq "$expected_error" "$log_file" ||
    fail "$release: error did not contain the expected message: $expected_error"

  # The both-fields and neither-field diagnostics share a prefix, so each case
  # has to rule out the other one. Without this, a chart that collapsed the two
  # branches into one message would still pass both cases.
  if test -n "$forbidden_error"; then
    if grep -Fq "$forbidden_error" "$log_file"; then
      fail "$release: matched the wrong diagnostic: $forbidden_error"
    fi
  fi
}

write_env() {
  cat >"$environment_file"
}

# ---------------------------------------------------------------------------
# 0. Stack topology
# ---------------------------------------------------------------------------
write_env <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
EOF

for release in cassandra nats openbao-server ratelimiter ess-api; do
  assert_single_release "$release"
done

# ---------------------------------------------------------------------------
# 1. Cassandra PDB - omitted (default off)
# ---------------------------------------------------------------------------
render_chart_values cassandra "$work_dir/cassandra-off-values.yaml" >/dev/null
if yq -e '.cassandra.podDisruptionBudget.enabled == true' \
  "$work_dir/cassandra-off-values.yaml" >/dev/null 2>&1; then
  fail "cassandra: PDB should be disabled by default but rendered enabled: true"
fi

# ---------------------------------------------------------------------------
# 2. Cassandra PDB - minAvailable passthrough
# ---------------------------------------------------------------------------
write_env <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
cassandra:
  podDisruptionBudget:
    enabled: true
    minAvailable: 2
EOF

render_chart_values cassandra "$work_dir/cassandra-min-values.yaml" >/dev/null
yq -e '.cassandra.podDisruptionBudget.enabled == true' \
  "$work_dir/cassandra-min-values.yaml" >/dev/null ||
  fail "cassandra: minAvailable PDB did not set enabled: true"
yq -e '.cassandra.podDisruptionBudget.minAvailable == 2' \
  "$work_dir/cassandra-min-values.yaml" >/dev/null ||
  fail "cassandra: minAvailable: 2 did not reach the chart values"

# ---------------------------------------------------------------------------
# 3. Cassandra PDB - maxUnavailable passthrough
# ---------------------------------------------------------------------------
write_env <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
cassandra:
  podDisruptionBudget:
    enabled: true
    maxUnavailable: 1
EOF

render_chart_values cassandra "$work_dir/cassandra-max-values.yaml" >/dev/null
yq -e '.cassandra.podDisruptionBudget.maxUnavailable == 1' \
  "$work_dir/cassandra-max-values.yaml" >/dev/null ||
  fail "cassandra: maxUnavailable: 1 did not reach the chart values"

# ---------------------------------------------------------------------------
# 4. Cassandra PDB - chart fail: both fields set
# ---------------------------------------------------------------------------
write_env <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
cassandra:
  podDisruptionBudget:
    enabled: true
    minAvailable: 2
    maxUnavailable: 1
EOF

expect_chart_error cassandra "$helm_dir/cassandra/helm" \
  "$work_dir/cassandra-both.log" \
  "podDisruptionBudget: set exactly one of minAvailable or maxUnavailable, not both"

# ---------------------------------------------------------------------------
# 4b. Cassandra PDB - chart fail: neither field set
#
# base.yaml ships minAvailable: 2, so an environment that only sets enabled
# still inherits an availability field. Clear it with the chart's own unset
# sentinel to reach the neither-field state the chart is supposed to reject.
# ---------------------------------------------------------------------------
write_env <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
cassandra:
  podDisruptionBudget:
    enabled: true
    minAvailable: ""
EOF

expect_chart_error cassandra "$helm_dir/cassandra/helm" \
  "$work_dir/cassandra-neither.log" \
  "podDisruptionBudget: set exactly one of minAvailable or maxUnavailable" \
  "not both"


# ---------------------------------------------------------------------------
# 5. NATS PDB - passthrough (upstream chart; enabled by default)
# ---------------------------------------------------------------------------
write_env <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
nats:
  podDisruptionBudget:
    enabled: false
EOF

render_chart_values nats "$work_dir/nats-disabled-values.yaml" >/dev/null
yq -e '.nats.podDisruptionBudget.enabled == false' \
  "$work_dir/nats-disabled-values.yaml" >/dev/null ||
  fail "nats: podDisruptionBudget.enabled: false did not reach chart values"

# ---------------------------------------------------------------------------
# 6. OpenBao HA disruptionBudget passthrough
# ---------------------------------------------------------------------------
write_env <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
openbao:
  server:
    ha:
      disruptionBudget:
        enabled: true
        maxUnavailable: 1
EOF

render_chart_values openbao-server "$work_dir/openbao-ha-values.yaml" >/dev/null
yq -e '.openbao.server.ha.disruptionBudget.maxUnavailable == 1' \
  "$work_dir/openbao-ha-values.yaml" >/dev/null ||
  fail "openbao: server.ha.disruptionBudget.maxUnavailable: 1 did not reach chart values"

# ---------------------------------------------------------------------------
# 7. OpenBao injector PDB minAvailable passthrough
# ---------------------------------------------------------------------------
write_env <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
openbao:
  injector:
    podDisruptionBudget:
      minAvailable: 2
EOF

render_chart_values openbao-server "$work_dir/openbao-injector-values.yaml" >/dev/null
yq -e '.openbao.injector.podDisruptionBudget.minAvailable == 2' \
  "$work_dir/openbao-injector-values.yaml" >/dev/null ||
  fail "openbao: injector.podDisruptionBudget.minAvailable: 2 did not reach chart values"

# ---------------------------------------------------------------------------
# 8. rateLimiter PDB passthrough
# ---------------------------------------------------------------------------
write_env <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
rateLimiter:
  podDisruptionBudget:
    enabled: true
    maxUnavailable: 1
EOF

render_chart_values ratelimiter "$work_dir/ratelimiter-values.yaml" >/dev/null
yq -e '.rateLimiter.podDisruptionBudget.enabled == true' \
  "$work_dir/ratelimiter-values.yaml" >/dev/null ||
  fail "rateLimiter: podDisruptionBudget.enabled: true did not reach chart values"

# ---------------------------------------------------------------------------
# 9. ess PDB passthrough
# ---------------------------------------------------------------------------
write_env <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
ess:
  podDisruptionBudget:
    enabled: true
    maxUnavailable: 1
EOF

render_chart_values ess-api "$work_dir/ess-values.yaml" >/dev/null
yq -e '.ess.podDisruptionBudget.enabled == true' \
  "$work_dir/ess-values.yaml" >/dev/null ||
  fail "ess: podDisruptionBudget.enabled: true did not reach chart values"

echo "pdb-value-wiring: all checks passed"
