#!/usr/bin/env bash
# Test that podDisruptionBudget values thread correctly from environment files
# through global.yaml.gotmpl into each chart's rendered output, and that the
# chart-level fail validation fires when both or neither availability field is set.
set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work_dir="$(mktemp -d)"
test_stack_dir="$work_dir/self-managed"
environment_name="pdb-value-wiring-test"
environment_file="$test_stack_dir/environments/$environment_name.yaml"
secrets_file="$test_stack_dir/secrets/$environment_name-secrets.yaml"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "pdb-value-wiring: $*" >&2
  exit 1
}

mkdir -p "$test_stack_dir"
cp -R "$stack_dir"/. "$test_stack_dir"
printf '{}\n' >"$secrets_file"

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

render_chart_values() {
  local release="$1"
  local output_file="$2"
  shift 2

  HELMFILE_ENV="$environment_name" \
    HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" \
    helmfile \
      --file "$test_stack_dir/helmfile.d/01-dependencies.yaml.gotmpl" \
      --environment default \
      --selector "name=$release" \
      "$@" \
      write-values \
      --output-file-template "$output_file" 2>/dev/null ||
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
      --selector "name=$release" \
      "$@" \
      write-values \
      --output-file-template "$output_file"
}

pdb_enabled_in_values() {
  local values_file="$1"
  local key_prefix="$2"   # e.g. "cassandra" or "" for root-level
  grep -q "enabled: true" "$values_file"
}

pdb_key_present() {
  local values_file="$1"
  grep -q "podDisruptionBudget:" "$values_file"
}

pdb_min_available() {
  local values_file="$1"
  grep -q "minAvailable:" "$values_file"
}

pdb_max_unavailable() {
  local values_file="$1"
  grep -q "maxUnavailable:" "$values_file"
}

write_env() {
  cat >"$environment_file"
}

# ---------------------------------------------------------------------------
# 1. Cassandra PDB — omitted (default off)
# ---------------------------------------------------------------------------
write_env <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
EOF

render_chart_values cassandra "$work_dir/cassandra-off-values.yaml" >/dev/null
if grep -q "podDisruptionBudget:" "$work_dir/cassandra-off-values.yaml" &&
   grep -A2 "podDisruptionBudget:" "$work_dir/cassandra-off-values.yaml" | grep -q "enabled: true"; then
  fail "cassandra: PDB should be disabled by default but rendered enabled: true"
fi

# ---------------------------------------------------------------------------
# 2. Cassandra PDB — minAvailable passthrough
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
grep -q "enabled: true" "$work_dir/cassandra-min-values.yaml" ||
  fail "cassandra: minAvailable PDB did not set enabled: true"
grep -q "minAvailable: 2" "$work_dir/cassandra-min-values.yaml" ||
  fail "cassandra: minAvailable: 2 did not reach the chart values"

# ---------------------------------------------------------------------------
# 3. Cassandra PDB — maxUnavailable passthrough
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
grep -q "maxUnavailable: 1" "$work_dir/cassandra-max-values.yaml" ||
  fail "cassandra: maxUnavailable: 1 did not reach the chart values"

# ---------------------------------------------------------------------------
# 4. Cassandra PDB — chart fail: both fields set
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

# helm template should fail when both fields are set
if HELMFILE_ENV="$environment_name" \
   HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" \
   helmfile \
     --file "$test_stack_dir/helmfile.d/01-dependencies.yaml.gotmpl" \
     --environment default \
     --selector name=cassandra \
     template >"$work_dir/cassandra-both.log" 2>&1; then
  fail "cassandra: setting both minAvailable and maxUnavailable should have failed"
fi
grep -qi "exactly one" "$work_dir/cassandra-both.log" ||
  fail "cassandra: both-fields error did not contain expected message"

# ---------------------------------------------------------------------------
# 5. NATS PDB — passthrough (upstream chart; enabled by default)
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
# The nats wrapper values should reflect the override
grep -A2 "podDisruptionBudget:" "$work_dir/nats-disabled-values.yaml" | grep -q "enabled: false" ||
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
grep -q "maxUnavailable: 1" "$work_dir/openbao-ha-values.yaml" ||
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
grep -q "minAvailable: 2" "$work_dir/openbao-injector-values.yaml" ||
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
grep -q "enabled: true" "$work_dir/ratelimiter-values.yaml" ||
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
grep -q "enabled: true" "$work_dir/ess-values.yaml" ||
  fail "ess: podDisruptionBudget.enabled: true did not reach chart values"

echo "pdb-value-wiring: all checks passed"
