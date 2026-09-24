#!/usr/bin/env bash
# Rendered-manifest test for the HA chart hooks.
#
# ha-value-wiring.sh proves global.yaml.gotmpl EMITS the right values. This test
# proves the in-scope charts actually CONSUME them: it runs `helm template` with
# HA-shaped values and asserts the rendered manifest contains the fields. It
# guards against the "silent no-op" failure mode where the stack emits a value
# (e.g. topologySpreadConstraints) that a chart never renders because the hook
# is missing.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)"
helm="$repo_root/deploy/helm"

fail() {
  echo "ha-chart-render: $*" >&2
  exit 1
}

# render <dir> <prefix> [extra --set args...]
# Applies HA-shaped values under <prefix> (root keys when prefix is empty).
render() {
  local dir="$1" prefix="$2"; shift 2
  local dot=""
  [ -n "$prefix" ] && dot="$prefix."
  helm template ha-chart-render "$dir" \
    --set "${dot}topologySpreadConstraints[0].maxSkew=1" \
    --set "${dot}topologySpreadConstraints[0].topologyKey=topology.kubernetes.io/zone" \
    --set "${dot}topologySpreadConstraints[0].whenUnsatisfiable=ScheduleAnyway" \
    --set "${dot}strategy.type=RollingUpdate" \
    --set "${dot}strategy.rollingUpdate.maxUnavailable=0" \
    --set "${dot}strategy.rollingUpdate.maxSurge=1" \
    --set "${dot}podDisruptionBudget.enabled=true" \
    --set "${dot}podDisruptionBudget.minAvailable=1" \
    --set "${dot}affinity.podAntiAffinity.requiredDuringSchedulingIgnoredDuringExecution[0].topologyKey=kubernetes.io/hostname" \
    "$@" 2>&1
}

# check <name> <dir> <prefix> <checks> [extra --set args...]
# <checks> is a space-separated subset of: pdb spread strategy affinity
check() {
  local name="$1" dir="$2" prefix="$3" checks="$4"; shift 4
  local out
  if ! out="$(render "$dir" "$prefix" "$@")"; then
    printf '%s\n' "$out" >&2
    fail "$name: helm template failed"
  fi
  local c
  for c in $checks; do
    case "$c" in
      pdb)      printf '%s\n' "$out" | grep -q "kind: PodDisruptionBudget" \
                  || fail "$name: PodDisruptionBudget not rendered (chart missing the PDB template)";;
      spread)   printf '%s\n' "$out" | grep -q "topologySpreadConstraints:" \
                  || fail "$name: topologySpreadConstraints not rendered (chart missing the hook)";;
      strategy) printf '%s\n' "$out" | grep -q "strategy:" \
                  || fail "$name: rollout strategy not rendered (chart missing the hook)";;
      affinity) printf '%s\n' "$out" | grep -q "podAntiAffinity:" \
                  || fail "$name: podAntiAffinity not rendered (chart missing the affinity hook)";;
    esac
  done
  echo "ha-chart-render: $name ok ($checks)"
}

img="--set image.registry=r --set image.repository=repo"

# --- Tier-1 replica-safe serving charts (full hook set) ---
check nvcf-api "$helm/cloud-functions/nvcf-api" api "pdb spread strategy affinity" \
  --set api.image.registry=r --set api.image.repository=repo --set api.accountBootstrap.enabled=false
check admin-token-issuer-proxy "$helm/admin-token-issuer-proxy/chart" adminIssuerProxy "pdb spread strategy affinity" \
  --set adminIssuerProxy.image.registry=r --set adminIssuerProxy.image.repository=repo
check ratelimiter "$helm/ratelimiter/nvcf-ratelimiter" rateLimiter "pdb spread strategy affinity" \
  --set rateLimiter.image.registry=r --set rateLimiter.image.repository=repo
check llm-api-gateway "$helm/llm-api-gateway/llm-api-gateway" llmApiGateway "pdb spread strategy affinity" \
  --set llmApiGateway.image.registry=r --set llmApiGateway.image.repository=repo
check nats-auth-callout "$helm/nats-auth-callout" "" "pdb spread strategy affinity"

# --- Group-B control-plane charts (full hook set) ---
check nvct-api "$helm/cloud-tasks/nvct-api" nvctApi "pdb spread strategy affinity" \
  --set nvctApi.image.registry=r --set nvctApi.image.repository=repo
check notary "$helm/notary/nvcf-notary-service" notary "pdb spread strategy affinity" \
  --set notary.image.registry=r --set notary.image.repository=repo
check sis "$helm/icms/icms-api" sis "pdb spread strategy affinity" \
  --set sis.image.registry=r --set sis.image.repository=repo --set sis.vault.audience=aud
check api-keys "$helm/api-keys-colocated/api-keys" apikeys "pdb spread strategy affinity" \
  --set apikeys.image.registry=r --set apikeys.image.repository=repo
check reval "$helm/helm-reval" reval "pdb spread strategy affinity"
check ess "$helm/encrypted-secret-store/ess-api" ess "pdb spread strategy affinity" \
  --set ess.image.registry=r --set ess.image.repository=repo

# --- Envoy-deferred: single-replica, but the zone-spread hook must still exist
#     so the value is not a silent no-op once Envoy allows scaling. grpc-proxy
#     namespaces its scheduling under .deployment. ---
check invocation-service "$helm/http-invocation/nvcf-invocation-service" invocation "spread affinity" \
  --set invocation.image.registry=r --set invocation.image.repository=repo
check grpc-proxy "$helm/grpc-proxy/grpc-proxy" grpcproxy.deployment "spread affinity" \
  --set grpcproxy.image.registry=r --set grpcproxy.image.repository=repo

echo "ha-chart-render: ok"
