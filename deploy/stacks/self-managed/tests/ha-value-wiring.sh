#!/usr/bin/env bash
# Test that highAvailability values thread from environment files through
# global.yaml.gotmpl into chart values for Tier-1 / Tier-2 releases.
set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work_dir="$(mktemp -d)"
test_stack_dir="$work_dir/self-managed"
environment_name="ha-value-wiring-test"
environment_file="$test_stack_dir/environments/$environment_name.yaml"
secrets_file="$test_stack_dir/secrets/$environment_name-secrets.yaml"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "ha-value-wiring: $*" >&2
  exit 1
}

mkdir -p "$test_stack_dir"
cp -R "$stack_dir"/. "$test_stack_dir"
printf '{}\n' >"$secrets_file"

render_chart_values() {
  local release="$1"
  local output_file="$2"
  local helmfile_file="$3"
  shift 3

  # global.yaml.gotmpl evaluates adminIssuerProxy gateway refs for every release.
  HELMFILE_ENV="$environment_name" \
    HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" \
    helmfile \
      --file "$helmfile_file" \
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

write_env() {
  cat >"$environment_file"
}

deps="$test_stack_dir/helmfile.d/01-dependencies.yaml.gotmpl"
core="$test_stack_dir/helmfile.d/02-core.yaml.gotmpl"

echo "== highAvailability disabled: chart defaults / base values unchanged =="
write_env <<'EOF'
highAvailability:
  enabled: false
EOF

render_chart_values api "$work_dir/api-off.yaml" "$core" || fail "render api (ha off)"
# HA must not inject replicaCount into the api values when disabled.
if awk '/^api:/{p=1;next} /^[a-zA-Z]/{p=0} p' "$work_dir/api-off.yaml" | grep -q "replicaCount:"; then
  fail "api: HA replicaCount leaked while highAvailability.enabled=false"
fi
if awk '/^api:/{p=1;next} /^[a-zA-Z]/{p=0} p' "$work_dir/api-off.yaml" | grep -q "podAntiAffinity:"; then
  fail "api: HA affinity leaked while highAvailability.enabled=false"
fi

echo "== highAvailability production: Tier-1 / Tier-2 sizing =="
write_env <<'EOF'
highAvailability:
  enabled: true
  profile: production
EOF

render_chart_values api "$work_dir/api-on.yaml" "$core" || fail "render api (ha on)"
grep -E "replicaCount:[[:space:]]*2" "$work_dir/api-on.yaml" >/dev/null ||
  fail "api: expected replicaCount 2 for production HA"
grep -q "podAntiAffinity:" "$work_dir/api-on.yaml" ||
  fail "api: expected podAntiAffinity for production HA"

render_chart_values cassandra "$work_dir/cassandra-on.yaml" "$deps" || fail "render cassandra (ha on)"
grep -E "replicaCount:[[:space:]]*3" "$work_dir/cassandra-on.yaml" >/dev/null ||
  fail "cassandra: expected replicaCount 3 for production HA"
grep -A2 "podDisruptionBudget:" "$work_dir/cassandra-on.yaml" | grep -q "enabled: true" ||
  fail "cassandra: expected HA PDB enabled"

render_chart_values openbao-server "$work_dir/openbao-on.yaml" "$deps" || fail "render openbao (ha on)"
grep -A5 "^[[:space:]]*ha:" "$work_dir/openbao-on.yaml" | grep -E "replicas:[[:space:]]*3" >/dev/null ||
  fail "openbao: expected server.ha.replicas 3 for production HA"

render_chart_values nats "$work_dir/nats-on.yaml" "$deps" || fail "render nats (ha on)"
grep -A5 "cluster:" "$work_dir/nats-on.yaml" | grep -E "replicas:[[:space:]]*3" >/dev/null ||
  fail "nats: expected config.cluster.replicas 3 for production HA"

echo "== highAvailability single-node: force replicas=1 =="
write_env <<'EOF'
highAvailability:
  enabled: true
  profile: single-node
EOF

render_chart_values api "$work_dir/api-single.yaml" "$core" || fail "render api (single-node)"
grep -E "replicaCount:[[:space:]]*1" "$work_dir/api-single.yaml" >/dev/null ||
  fail "api: expected replicaCount 1 for single-node HA profile"
if grep -q "podAntiAffinity:" "$work_dir/api-single.yaml"; then
  fail "api: single-node must not set podAntiAffinity"
fi

render_chart_values cassandra "$work_dir/cassandra-single.yaml" "$deps" || fail "render cassandra (single-node)"
grep -E "replicaCount:[[:space:]]*1" "$work_dir/cassandra-single.yaml" >/dev/null ||
  fail "cassandra: expected replicaCount 1 for single-node HA profile"

echo "== invalid profile fails render =="
write_env <<'EOF'
highAvailability:
  enabled: true
  profile: nope
EOF
if render_chart_values api "$work_dir/api-bad.yaml" "$core" 2>"$work_dir/api-bad.err"; then
  fail "api: invalid highAvailability.profile should fail"
fi
grep -q "highAvailability.profile" "$work_dir/api-bad.err" ||
  fail "api: expected profile validation error message"

echo "ha-value-wiring: ok"
