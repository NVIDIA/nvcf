#!/usr/bin/env bash
# Test that highAvailability values thread from environment files through
# global.yaml.gotmpl into chart values for stateless / quorum releases.
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

echo "== highAvailability mode none: chart defaults / base values unchanged =="
write_env <<'EOF'
highAvailability:
  mode: none
EOF

render_chart_values api "$work_dir/api-off.yaml" "$core" || fail "render api (ha none)"
# HA must not inject replicaCount into the api values when disabled.
if awk '/^api:/{p=1;next} /^[a-zA-Z]/{p=0} p' "$work_dir/api-off.yaml" | grep -q "replicaCount:"; then
  fail "api: HA replicaCount leaked while highAvailability.mode=none"
fi
if awk '/^api:/{p=1;next} /^[a-zA-Z]/{p=0} p' "$work_dir/api-off.yaml" | grep -q "podAntiAffinity:"; then
  fail "api: HA affinity leaked while highAvailability.mode=none"
fi
if awk '/^api:/{p=1;next} /^[a-zA-Z]/{p=0} p' "$work_dir/api-off.yaml" | grep -q "topologySpreadConstraints:"; then
  fail "api: topology spread leaked while highAvailability.mode=none"
fi
if awk '/^api:/{p=1;next} /^[a-zA-Z]/{p=0} p' "$work_dir/api-off.yaml" | grep -q "podDisruptionBudget:"; then
  fail "api: PDB leaked while highAvailability.mode=none"
fi

render_chart_values ratelimiter "$work_dir/ratelimiter-off.yaml" "$core" --state-values-set rateLimiter.enabled=true ||
  fail "render ratelimiter (ha none)"
if awk '/^rateLimiter:/{p=1;next} /^[a-zA-Z]/{p=0} p' "$work_dir/ratelimiter-off.yaml" | grep -q "podAntiAffinity:"; then
  fail "ratelimiter: HA affinity leaked while highAvailability.mode=none"
fi

echo "== highAvailability ha-preferred: stateless / quorum sizing =="
write_env <<'EOF'
highAvailability:
  mode: ha-preferred
EOF

render_chart_values api "$work_dir/api-on.yaml" "$core" || fail "render api (ha-preferred)"
grep -E "replicaCount:[[:space:]]*2" "$work_dir/api-on.yaml" >/dev/null ||
  fail "api: expected replicaCount 2 when highAvailability.mode=ha-preferred"
grep -q "preferredDuringSchedulingIgnoredDuringExecution:" "$work_dir/api-on.yaml" ||
  fail "api: expected preferred anti-affinity when highAvailability.mode=ha-preferred"
grep -q "topologySpreadConstraints:" "$work_dir/api-on.yaml" ||
  fail "api: expected topologySpreadConstraints when highAvailability.mode=ha-preferred"
grep -q "topology.kubernetes.io/zone" "$work_dir/api-on.yaml" ||
  fail "api: expected zone topologyKey when highAvailability.mode=ha-preferred"
grep -q "whenUnsatisfiable: ScheduleAnyway" "$work_dir/api-on.yaml" ||
  fail "api: expected ScheduleAnyway topology spread when highAvailability.mode=ha-preferred"
awk '/^api:/{p=1;next} /^[a-zA-Z]/{p=0} p' "$work_dir/api-on.yaml" | grep -q "podDisruptionBudget:" ||
  fail "api: expected podDisruptionBudget when highAvailability.mode=ha-preferred"

render_chart_values cassandra "$work_dir/cassandra-on.yaml" "$deps" || fail "render cassandra (ha-preferred)"
grep -E "replicaCount:[[:space:]]*3" "$work_dir/cassandra-on.yaml" >/dev/null ||
  fail "cassandra: expected replicaCount 3 when highAvailability.mode=ha-preferred"
grep -A2 "podDisruptionBudget:" "$work_dir/cassandra-on.yaml" | grep -q "enabled: true" ||
  fail "cassandra: expected HA PDB enabled"

render_chart_values openbao-server "$work_dir/openbao-on.yaml" "$deps" || fail "render openbao (ha-preferred)"
grep -A5 "^[[:space:]]*ha:" "$work_dir/openbao-on.yaml" | grep -E "replicas:[[:space:]]*3" >/dev/null ||
  fail "openbao: expected server.ha.replicas 3 when highAvailability.mode=ha-preferred"

render_chart_values nats "$work_dir/nats-on.yaml" "$deps" || fail "render nats (ha-preferred)"
grep -A5 "cluster:" "$work_dir/nats-on.yaml" | grep -E "replicas:[[:space:]]*3" >/dev/null ||
  fail "nats: expected config.cluster.replicas 3 when highAvailability.mode=ha-preferred"

# Hot-path helpers (#988): rateLimiter + nats-auth-callout to 2 replicas.
render_chart_values ratelimiter "$work_dir/ratelimiter-on.yaml" "$core" --state-values-set rateLimiter.enabled=true ||
  fail "render ratelimiter (ha-preferred)"
awk '/^rateLimiter:/{p=1;next} /^[a-zA-Z]/{p=0} p' "$work_dir/ratelimiter-on.yaml" | grep -E "replicaCount:[[:space:]]*2" >/dev/null ||
  fail "ratelimiter: expected replicaCount 2 when highAvailability.mode=ha-preferred"
awk '/^rateLimiter:/{p=1;next} /^[a-zA-Z]/{p=0} p' "$work_dir/ratelimiter-on.yaml" | grep -q "preferredDuringSchedulingIgnoredDuringExecution:" ||
  fail "ratelimiter: expected preferred anti-affinity when highAvailability.mode=ha-preferred"
awk '/^rateLimiter:/{p=1;next} /^[a-zA-Z]/{p=0} p' "$work_dir/ratelimiter-on.yaml" | grep -q "topology.kubernetes.io/zone" ||
  fail "ratelimiter: expected zone topology spread when highAvailability.mode=ha-preferred"

render_chart_values nats-auth-callout-service "$work_dir/natsauth-on.yaml" "$core" ||
  fail "render nats-auth-callout (ha-preferred)"
awk '/^natsAuthCalloutService:/{p=1;next} /^[a-zA-Z]/{p=0} p' "$work_dir/natsauth-on.yaml" | grep -E "replicaCount:[[:space:]]*2" >/dev/null ||
  fail "nats-auth-callout: expected replicaCount 2 when highAvailability.mode=ha-preferred"

# llm-api-gateway (#987): stateless anti-affinity when the LLM addon is on.
render_chart_values llm-api-gateway "$work_dir/llmgw-on.yaml" "$core" --state-values-set addons.llm.enabled=true ||
  fail "render llm-api-gateway (ha-preferred)"
awk '/^llmApiGateway:/{p=1;next} /^[a-zA-Z]/{p=0} p' "$work_dir/llmgw-on.yaml" | grep -q "preferredDuringSchedulingIgnoredDuringExecution:" ||
  fail "llm-api-gateway: expected preferred anti-affinity when highAvailability.mode=ha-preferred"

echo "== highAvailability ha-enforced: required anti-affinity =="
write_env <<'EOF'
highAvailability:
  mode: ha-enforced
EOF

render_chart_values api "$work_dir/api-enforced.yaml" "$core" || fail "render api (ha-enforced)"
grep -q "requiredDuringSchedulingIgnoredDuringExecution:" "$work_dir/api-enforced.yaml" ||
  fail "api: expected required anti-affinity when highAvailability.mode=ha-enforced"
grep -q "whenUnsatisfiable: DoNotSchedule" "$work_dir/api-enforced.yaml" ||
  fail "api: expected DoNotSchedule topology spread when highAvailability.mode=ha-enforced"

render_chart_values ratelimiter "$work_dir/ratelimiter-enforced.yaml" "$core" --state-values-set rateLimiter.enabled=true ||
  fail "render ratelimiter (ha-enforced)"
awk '/^rateLimiter:/{p=1;next} /^[a-zA-Z]/{p=0} p' "$work_dir/ratelimiter-enforced.yaml" | grep -q "requiredDuringSchedulingIgnoredDuringExecution:" ||
  fail "ratelimiter: expected required anti-affinity when highAvailability.mode=ha-enforced"

echo "== highAvailability invalid mode fails render =="
write_env <<'EOF'
highAvailability:
  mode: best-effort
EOF
if render_chart_values api "$work_dir/api-bad.yaml" "$core" 2>"$work_dir/api-bad.err"; then
  fail "api: invalid highAvailability.mode should fail helmfile render"
fi
grep -q "highAvailability.mode" "$work_dir/api-bad.err" ||
  fail "api: expected fail message to mention highAvailability.mode"

echo "ha-value-wiring: ok"
