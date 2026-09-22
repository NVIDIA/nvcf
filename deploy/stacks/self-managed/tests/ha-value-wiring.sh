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
if awk '/^api:/{p=1;next} /^[a-zA-Z]/{p=0} p' "$work_dir/api-off.yaml" | grep -q "strategy:"; then
  fail "api: rollout strategy leaked while highAvailability.mode=none"
fi
# Group-B must also stay untouched when HA is off.
for comp in nvctApi notary sis apikeys reval; do
  sub="$(awk -v k="^$comp:" '$0~k{p=1;next} p&&/^[a-zA-Z]/{p=0} p' "$work_dir/api-off.yaml")"
  if echo "$sub" | grep -qE "replicaCount:[[:space:]]*[2-9]"; then
    fail "$comp: HA replicaCount leaked while highAvailability.mode=none"
  fi
  if echo "$sub" | grep -q "podAntiAffinity:"; then
    fail "$comp: HA affinity leaked while highAvailability.mode=none"
  fi
done

render_chart_values ratelimiter "$work_dir/ratelimiter-off.yaml" "$core" --state-values-set rateLimiter.enabled=true ||
  fail "render ratelimiter (ha none)"
if awk '/^rateLimiter:/{p=1;next} /^[a-zA-Z]/{p=0} p' "$work_dir/ratelimiter-off.yaml" | grep -q "podAntiAffinity:"; then
  fail "ratelimiter: HA affinity leaked while highAvailability.mode=none"
fi

# Tier-2: anti-affinity must not leak into the quorum charts when off.
render_chart_values cassandra "$work_dir/cassandra-off.yaml" "$deps" || fail "render cassandra (ha none)"
if grep -q "podAntiAffinity:" "$work_dir/cassandra-off.yaml"; then
  fail "cassandra: Tier-2 anti-affinity leaked while highAvailability.mode=none"
fi

# JetStream RF must not leak into the stream creators when off.
if awk '/^api:/{p=1;next} /^[a-zA-Z]/{p=0} p' "$work_dir/api-off.yaml" | grep -q "NVCF_NATS_REPLICAS:"; then
  fail "api: JetStream RF env leaked while highAvailability.mode=none"
fi
render_chart_values invocation-service "$work_dir/invocation-off.yaml" "$core" || fail "render invocation (ha none)"
if grep -q "NATS_PROPERTIES__REPLICAS:" "$work_dir/invocation-off.yaml"; then
  fail "invocation: JetStream RF env leaked while highAvailability.mode=none"
fi

echo "== highAvailability preferred: stateless / quorum sizing =="
write_env <<'EOF'
highAvailability:
  mode: preferred
EOF

render_chart_values api "$work_dir/api-on.yaml" "$core" || fail "render api (preferred)"
grep -E "replicaCount:[[:space:]]*2" "$work_dir/api-on.yaml" >/dev/null ||
  fail "api: expected replicaCount 2 when highAvailability.mode=preferred"
grep -q "preferredDuringSchedulingIgnoredDuringExecution:" "$work_dir/api-on.yaml" ||
  fail "api: expected preferred anti-affinity when highAvailability.mode=preferred"
grep -q "topologySpreadConstraints:" "$work_dir/api-on.yaml" ||
  fail "api: expected topologySpreadConstraints when highAvailability.mode=preferred"
grep -q "topology.kubernetes.io/zone" "$work_dir/api-on.yaml" ||
  fail "api: expected zone topologyKey when highAvailability.mode=preferred"
grep -q "whenUnsatisfiable: ScheduleAnyway" "$work_dir/api-on.yaml" ||
  fail "api: expected ScheduleAnyway topology spread when highAvailability.mode=preferred"
# Rollout strategy (Gap 3): a PDB governs drains, not rollouts; maxUnavailable 0
# / maxSurge 1 keeps the current Ready count during a Deployment rolling update.
awk '/^api:/{p=1;next} /^[a-zA-Z]/{p=0} p' "$work_dir/api-on.yaml" | grep -q "strategy:" ||
  fail "api: expected rollout strategy when highAvailability.mode=preferred"
grep -q "maxUnavailable: 0" "$work_dir/api-on.yaml" ||
  fail "api: expected strategy maxUnavailable 0 when highAvailability.mode=preferred"
grep -q "maxSurge: 1" "$work_dir/api-on.yaml" ||
  fail "api: expected strategy maxSurge 1 when highAvailability.mode=preferred"
awk '/^api:/{p=1;next} /^[a-zA-Z]/{p=0} p' "$work_dir/api-on.yaml" | grep -q "podDisruptionBudget:" ||
  fail "api: expected podDisruptionBudget when highAvailability.mode=preferred"
grep -q 'NVCF_NATS_REPLICAS: "3"' "$work_dir/api-on.yaml" ||
  fail "api: expected JetStream RF NVCF_NATS_REPLICAS=3 when highAvailability.mode=preferred"

render_chart_values invocation-service "$work_dir/invocation-on.yaml" "$core" || fail "render invocation (preferred)"
grep -q 'NATS_PROPERTIES__REPLICAS: "3"' "$work_dir/invocation-on.yaml" ||
  fail "invocation: expected JetStream RF NATS_PROPERTIES__REPLICAS=3 when highAvailability.mode=preferred"

render_chart_values cassandra "$work_dir/cassandra-on.yaml" "$deps" || fail "render cassandra (preferred)"
grep -E "replicaCount:[[:space:]]*3" "$work_dir/cassandra-on.yaml" >/dev/null ||
  fail "cassandra: expected replicaCount 3 when highAvailability.mode=preferred"
grep -A2 "podDisruptionBudget:" "$work_dir/cassandra-on.yaml" | grep -q "enabled: true" ||
  fail "cassandra: expected HA PDB enabled"
grep -q "podAntiAffinity:" "$work_dir/cassandra-on.yaml" ||
  fail "cassandra: expected Tier-2 anti-affinity when highAvailability.mode=preferred"
grep -q "preferredDuringSchedulingIgnoredDuringExecution:" "$work_dir/cassandra-on.yaml" ||
  fail "cassandra: expected preferred Tier-2 anti-affinity when highAvailability.mode=preferred"
# Zone topology spread is part of the same mode convention as hostname
# anti-affinity (soft under preferred) — it is no longer a separate opt-in
# toggle. NOTE: the write-values file holds every release's values, so scope
# to the cassandra: block.
awk '/^cassandra:/{p=1;next} /^[a-zA-Z]/{p=0} p' "$work_dir/cassandra-on.yaml" | grep -q "topology.kubernetes.io/zone" ||
  fail "cassandra: expected Tier-2 zone spread when highAvailability.mode=preferred"
awk '/^cassandra:/{p=1;next} /^[a-zA-Z]/{p=0} p' "$work_dir/cassandra-on.yaml" | grep -q "whenUnsatisfiable: ScheduleAnyway" ||
  fail "cassandra: expected soft (ScheduleAnyway) Tier-2 spread when highAvailability.mode=preferred"

render_chart_values openbao-server "$work_dir/openbao-on.yaml" "$deps" || fail "render openbao (preferred)"
grep -A5 "^[[:space:]]*ha:" "$work_dir/openbao-on.yaml" | grep -E "replicas:[[:space:]]*3" >/dev/null ||
  fail "openbao: expected server.ha.replicas 3 when highAvailability.mode=preferred"
grep -q "podAntiAffinity:" "$work_dir/openbao-on.yaml" ||
  fail "openbao: expected Tier-2 anti-affinity when highAvailability.mode=preferred"
grep -A3 "disruptionBudget:" "$work_dir/openbao-on.yaml" | grep -q "maxUnavailable: 1" ||
  fail "openbao: expected server.ha.disruptionBudget maxUnavailable=1 when highAvailability.mode=preferred"
awk '/^openbao:/{p=1;next} /^[a-zA-Z]/{p=0} p' "$work_dir/openbao-on.yaml" | grep -q "topology.kubernetes.io/zone" ||
  fail "openbao: expected Tier-2 zone spread when highAvailability.mode=preferred"

render_chart_values nats "$work_dir/nats-on.yaml" "$deps" || fail "render nats (preferred)"
grep -A5 "cluster:" "$work_dir/nats-on.yaml" | grep -E "replicas:[[:space:]]*3" >/dev/null ||
  fail "nats: expected config.cluster.replicas 3 when highAvailability.mode=preferred"
grep -q "podAntiAffinity:" "$work_dir/nats-on.yaml" ||
  fail "nats: expected Tier-2 anti-affinity when highAvailability.mode=preferred"
awk '/^nats:/{p=1;next} /^[a-zA-Z]/{p=0} p' "$work_dir/nats-on.yaml" | grep -q "topology.kubernetes.io/zone" ||
  fail "nats: expected Tier-2 zone spread when highAvailability.mode=preferred"

# rateLimiter + nats-auth-callout scale to 2 replicas.
render_chart_values ratelimiter "$work_dir/ratelimiter-on.yaml" "$core" --state-values-set rateLimiter.enabled=true ||
  fail "render ratelimiter (preferred)"
awk '/^rateLimiter:/{p=1;next} /^[a-zA-Z]/{p=0} p' "$work_dir/ratelimiter-on.yaml" | grep -E "replicaCount:[[:space:]]*2" >/dev/null ||
  fail "ratelimiter: expected replicaCount 2 when highAvailability.mode=preferred"
awk '/^rateLimiter:/{p=1;next} /^[a-zA-Z]/{p=0} p' "$work_dir/ratelimiter-on.yaml" | grep -q "preferredDuringSchedulingIgnoredDuringExecution:" ||
  fail "ratelimiter: expected preferred anti-affinity when highAvailability.mode=preferred"
awk '/^rateLimiter:/{p=1;next} /^[a-zA-Z]/{p=0} p' "$work_dir/ratelimiter-on.yaml" | grep -q "topology.kubernetes.io/zone" ||
  fail "ratelimiter: expected zone topology spread when highAvailability.mode=preferred"

render_chart_values nats-auth-callout-service "$work_dir/natsauth-on.yaml" "$core" ||
  fail "render nats-auth-callout (preferred)"
awk '/^natsAuthCalloutService:/{p=1;next} /^[a-zA-Z]/{p=0} p' "$work_dir/natsauth-on.yaml" | grep -E "replicaCount:[[:space:]]*2" >/dev/null ||
  fail "nats-auth-callout: expected replicaCount 2 when highAvailability.mode=preferred"
awk '/^natsAuthCalloutService:/{p=1;next} /^[a-zA-Z]/{p=0} p' "$work_dir/natsauth-on.yaml" | grep -q "podDisruptionBudget:" ||
  fail "nats-auth-callout: expected podDisruptionBudget when highAvailability.mode=preferred"
awk '/^natsAuthCalloutService:/{p=1;next} /^[a-zA-Z]/{p=0} p' "$work_dir/natsauth-on.yaml" | grep -q "topologySpreadConstraints:" ||
  fail "nats-auth-callout: expected zone topology spread when highAvailability.mode=preferred"

# llm-api-gateway: anti-affinity when the LLM addon is on.
render_chart_values llm-api-gateway "$work_dir/llmgw-on.yaml" "$core" --state-values-set addons.llm.enabled=true ||
  fail "render llm-api-gateway (preferred)"
awk '/^llmApiGateway:/{p=1;next} /^[a-zA-Z]/{p=0} p' "$work_dir/llmgw-on.yaml" | grep -q "preferredDuringSchedulingIgnoredDuringExecution:" ||
  fail "llm-api-gateway: expected preferred anti-affinity when highAvailability.mode=preferred"

# Group-B services (nvct-api, notary, sis, api-keys, reval) join the replica-safe
# set under HA. Every component subtree is emitted, so assert on api-on.yaml.
for comp in nvctApi notary sis apikeys reval; do
  sub="$(awk -v k="^$comp:" '$0~k{p=1;next} p&&/^[a-zA-Z]/{p=0} p' "$work_dir/api-on.yaml")"
  echo "$sub" | grep -E "replicaCount:[[:space:]]*2" >/dev/null ||
    fail "$comp: expected replicaCount 2 when highAvailability.mode=preferred"
  echo "$sub" | grep -q "podDisruptionBudget:" ||
    fail "$comp: expected podDisruptionBudget when highAvailability.mode=preferred"
  echo "$sub" | grep -q "topologySpreadConstraints:" ||
    fail "$comp: expected zone topology spread when highAvailability.mode=preferred"
  echo "$sub" | grep -q "preferredDuringSchedulingIgnoredDuringExecution:" ||
    fail "$comp: expected preferred anti-affinity when highAvailability.mode=preferred"
  echo "$sub" | grep -q "strategy:" ||
    fail "$comp: expected rollout strategy when highAvailability.mode=preferred"
done

# invocation-service + grpc-proxy stay single-replica until Envoy and get no PDB
# (minAvailable 1 on a singleton blocks drains). Placement may still render but
# is a no-op at one replica.
render_chart_values invocation-service "$work_dir/invocation-on.yaml" "$core" ||
  fail "render invocation-service (preferred)"
if awk '/^invocation:/{p=1;next} /^[a-zA-Z]/{p=0} p' "$work_dir/invocation-on.yaml" | grep -qE "replicaCount:[[:space:]]*[2-9]"; then
  fail "invocation-service: must stay single-replica under HA (deferred until Envoy)"
fi
# The chart's own PDB knob may render (enabled: false); the HA PDB (enabled:
# true / minAvailable on a singleton) must NOT.
if awk '/^invocation:/{p=1;next} /^[a-zA-Z]/{p=0} p' "$work_dir/invocation-on.yaml" | grep -A3 "podDisruptionBudget:" | grep -q "enabled: true"; then
  fail "invocation-service: HA PDB must not be enabled while single-replica (deferred until Envoy)"
fi

render_chart_values grpc-proxy "$work_dir/grpcproxy-on.yaml" "$core" ||
  fail "render grpc-proxy (preferred)"
if awk '/^grpcproxy:/{p=1;next} /^[a-zA-Z]/{p=0} p' "$work_dir/grpcproxy-on.yaml" | grep -qE "replicaCount:[[:space:]]*[2-9]"; then
  fail "grpc-proxy: must stay single-replica under HA (deferred until Envoy)"
fi
if awk '/^grpcproxy:/{p=1;next} /^[a-zA-Z]/{p=0} p' "$work_dir/grpcproxy-on.yaml" | grep -A3 "podDisruptionBudget:" | grep -q "enabled: true"; then
  fail "grpc-proxy: HA PDB must not be enabled while single-replica (deferred until Envoy)"
fi

echo "== global.affinity / global.topologySpreadConstraints fallback (class -> all -> convention) =="

# class-specific global.affinity override wins over the generated convention.
write_env <<'EOF'
highAvailability:
  mode: preferred
global:
  affinity:
    cassandra:
      podAntiAffinity:
        requiredDuringSchedulingIgnoredDuringExecution:
          - labelSelector:
              matchLabels:
                custom: override
            topologyKey: kubernetes.io/hostname
EOF
render_chart_values cassandra "$work_dir/cassandra-global-class.yaml" "$deps" || fail "render cassandra (global.affinity.cassandra override)"
awk '/^cassandra:/{p=1;next} /^[a-zA-Z]/{p=0} p' "$work_dir/cassandra-global-class.yaml" | grep -q "custom: override" ||
  fail "cassandra: expected global.affinity.cassandra override to win over the generated convention"
# The generated anti-affinity's own selector ("operator: In" against
# app.kubernetes.io/instance) must not also render — only the override
# content. app.kubernetes.io/instance alone is not distinctive enough to
# assert on: the (unrelated, mode-derived) topologySpreadConstraints block
# legitimately uses it too.
if awk '/^cassandra:/{p=1;next} /^[a-zA-Z]/{p=0} p' "$work_dir/cassandra-global-class.yaml" | grep -A2 "podAntiAffinity:" | grep -q "operator: In"; then
  fail "cassandra: generated anti-affinity must not also render alongside a class override"
fi

# global.affinity.all applies when no class-specific override exists.
write_env <<'EOF'
highAvailability:
  mode: preferred
global:
  affinity:
    all:
      podAntiAffinity:
        requiredDuringSchedulingIgnoredDuringExecution:
          - labelSelector:
              matchLabels:
                custom: shared
            topologyKey: kubernetes.io/hostname
EOF
render_chart_values cassandra "$work_dir/cassandra-global-all.yaml" "$deps" || fail "render cassandra (global.affinity.all)"
awk '/^cassandra:/{p=1;next} /^[a-zA-Z]/{p=0} p' "$work_dir/cassandra-global-all.yaml" | grep -q "custom: shared" ||
  fail "cassandra: expected global.affinity.all to apply when no class-specific override exists"

# An explicit {} for a class suppresses the generated anti-affinity entirely —
# presence is the signal, not truthiness.
write_env <<'EOF'
highAvailability:
  mode: preferred
global:
  affinity:
    cassandra: {}
  topologySpreadConstraints:
    cassandra: []
EOF
render_chart_values cassandra "$work_dir/cassandra-global-suppressed.yaml" "$deps" || fail "render cassandra (global.affinity.cassandra: {})"
if awk '/^cassandra:/{p=1;next} /^[a-zA-Z]/{p=0} p' "$work_dir/cassandra-global-suppressed.yaml" | grep -q "podAntiAffinity:"; then
  fail "cassandra: expected explicit global.affinity.cassandra: {} to suppress generated anti-affinity"
fi
if awk '/^cassandra:/{p=1;next} /^[a-zA-Z]/{p=0} p' "$work_dir/cassandra-global-suppressed.yaml" | grep -q "topology.kubernetes.io/zone"; then
  fail "cassandra: expected explicit global.topologySpreadConstraints.cassandra: [] to suppress generated zone spread"
fi

echo "== highAvailability enforced: required anti-affinity, hard zone spread =="
write_env <<'EOF'
highAvailability:
  mode: enforced
EOF

render_chart_values api "$work_dir/api-enforced.yaml" "$core" || fail "render api (enforced)"
grep -q "requiredDuringSchedulingIgnoredDuringExecution:" "$work_dir/api-enforced.yaml" ||
  fail "api: expected required anti-affinity when highAvailability.mode=enforced"
grep -q "whenUnsatisfiable: DoNotSchedule" "$work_dir/api-enforced.yaml" ||
  fail "api: expected DoNotSchedule topology spread when highAvailability.mode=enforced"

render_chart_values cassandra "$work_dir/cassandra-enforced.yaml" "$deps" || fail "render cassandra (enforced)"
grep -q "requiredDuringSchedulingIgnoredDuringExecution:" "$work_dir/cassandra-enforced.yaml" ||
  fail "cassandra: expected required Tier-2 anti-affinity when highAvailability.mode=enforced"
awk '/^cassandra:/{p=1;next} /^[a-zA-Z]/{p=0} p' "$work_dir/cassandra-enforced.yaml" | grep -q "whenUnsatisfiable: DoNotSchedule" ||
  fail "cassandra: expected hard (DoNotSchedule) Tier-2 spread when highAvailability.mode=enforced"

render_chart_values ratelimiter "$work_dir/ratelimiter-enforced.yaml" "$core" --state-values-set rateLimiter.enabled=true ||
  fail "render ratelimiter (enforced)"
awk '/^rateLimiter:/{p=1;next} /^[a-zA-Z]/{p=0} p' "$work_dir/ratelimiter-enforced.yaml" | grep -q "requiredDuringSchedulingIgnoredDuringExecution:" ||
  fail "ratelimiter: expected required anti-affinity when highAvailability.mode=enforced"

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
