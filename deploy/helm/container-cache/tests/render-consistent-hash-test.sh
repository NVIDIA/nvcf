#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Rendered-output regression tests for opt-in consistent-hash routing
# (consistentHashRouting.enabled). Run from the chart subtree:
#   bash tests/render-consistent-hash-test.sh
set -euo pipefail
CHART_DIR="$(cd "$(dirname "$0")/.." && pwd)/deploy"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }
count() { grep -Ec "$1" "$2" || true; }

helm template t "$CHART_DIR" > "$TMP/off.yaml" 2>/dev/null
helm template t "$CHART_DIR" --set consistentHashRouting.enabled=false > "$TMP/off2.yaml" 2>/dev/null
helm template t "$CHART_DIR" --set consistentHashRouting.enabled=true --set replicaCount=3 > "$TMP/on.yaml" 2>/dev/null

echo "1. disabled (default and explicit): no routing directives render"
for f in "$TMP/off.yaml" "$TMP/off2.yaml"; do
  [ "$(count 'rewrite_by_lua_file /etc/nginx/conf.d/lua/cc-route' "$f")" = 0 ] || fail "rewrite_by_lua_file leaked when disabled"
  [ "$(count 'location @cc_relay' "$f")" = 0 ] || fail "@cc_relay leaked when disabled"
  [ "$(count 'upstream cc_owner_' "$f")" = 0 ] || fail "cc_owner upstream leaked when disabled"
  [ "$(count 'statefulset.kubernetes.io/pod-name' "$f")" = 0 ] || fail "peer Service leaked when disabled"
  [ "$(count 'cc-route.lua' "$f")" = 0 ] || fail "cc-route.lua leaked into the ConfigMap/volume when disabled"
done

echo "1b. default render == explicit enabled=false render (disabled is a pure no-op)"
diff "$TMP/off.yaml" "$TMP/off2.yaml" >/dev/null || fail "default and enabled=false renders differ"

echo "2. enabled: @cc_relay is defined exactly once (deduped into proxy-common)"
n="$(count 'location @cc_relay' "$TMP/on.yaml")"
[ "$n" = 1 ] || fail "expected 1 @cc_relay, got $n (duplication regressed)"

echo "3. enabled: both routed blocks (ngc + *.hf.co) call the shared lua file"
n="$(count 'rewrite_by_lua_file /etc/nginx/conf.d/lua/cc-route.lua' "$TMP/on.yaml")"
[ "$n" = 2 ] || fail "expected 2 rewrite_by_lua_file (ngc + hf), got $n"
[ "$(count 'rewrite_by_lua_block' "$TMP/on.yaml")" = 0 ] || fail "inline rewrite_by_lua_block must not render (should be the shared file)"

echo "4. enabled: shared routing lua ships in the configmap"
grep -q 'cc-route.lua:' "$TMP/on.yaml" || fail "cc-route.lua not in configmap"

echo "5. enabled: N peer Services + N owner upstreams on the listener port (14128)"
[ "$(count 'statefulset.kubernetes.io/pod-name:' "$TMP/on.yaml")" = 3 ] || fail "expected 3 per-ordinal peer Services"
[ "$(count 'upstream cc_owner_' "$TMP/on.yaml")" = 3 ] || fail "expected 3 cc_owner upstreams"
[ "$(count 'peer-[0-9].*svc.cluster.local:14128 max_fails' "$TMP/on.yaml")" = 3 ] || fail "peer upstreams must target the ssl listener port 14128"
[ "$(count 'targetPort: 14128' "$TMP/on.yaml")" = 3 ] || fail "each peer Service must expose targetPort 14128 (drift breaks every relay)"
[ "$(count 'port: 14128' "$TMP/on.yaml")" = 3 ] || fail "each peer Service must expose port 14128"

echo "6. enabled: routing key == proxy_cache_key; marker emitted AND inbound marker rejected"
grep -q 'set $cc_hash_key "$request_method|$uri|$arg_versionId|$http_range"' "$TMP/on.yaml" || fail "routing key must equal the proxy_cache_key"
grep -q 'proxy_set_header X-NVCF-CC-Relayed "1"' "$TMP/on.yaml" || fail "relay hop must emit the one-hop marker"
grep -q 'ngx.req.get_headers()\["X-NVCF-CC-Relayed"\]' "$TMP/on.yaml" || fail "cc-route.lua must reject an inbound relay marker (serve locally, prevents relay loops)"

echo "7. enabled: the peer hop reuses connections"
# Both halves are required. A keepalive pool with no `Connection ""` is dead
# weight, because nginx then sends its default `Connection: close` and every
# relayed request re-handshakes TLS. The header must be repeated inside
# @cc_relay specifically: declaring any proxy_set_header in a location cancels
# inheritance of the server-level set.
[ "$(count 'keepalive [0-9]+;' "$TMP/on.yaml")" = 3 ] || fail "each cc_owner upstream needs a keepalive pool"
awk '/location @cc_relay/,/^ *}$/' "$TMP/on.yaml" | grep -q 'proxy_set_header Connection ""' \
  || fail '@cc_relay must repeat Connection "" or the keepalive pool is never used'

echo "8. enabled: hot objects replicate locally after relayCacheMinUses"
awk '/location @cc_relay/,/^ *}$/' "$TMP/on.yaml" | grep -q 'proxy_cache_min_uses 3' \
  || fail "relay must cache locally after the configured use threshold"
awk '/location @cc_relay/,/^ *}$/' "$TMP/on.yaml" | grep -q 'proxy_cache_key \$cc_hash_key' \
  || fail "relay cache key must match the owner's identity, not the default key"

echo "8b. relayCacheMinUses=0 restores strict single-copy relaying"
helm template t "$CHART_DIR" --set consistentHashRouting.enabled=true --set replicaCount=3 \
  --set consistentHashRouting.relayCacheMinUses=0 > "$TMP/on-nocache.yaml" 2>/dev/null
awk '/location @cc_relay/,/^ *}$/' "$TMP/on-nocache.yaml" | grep -q 'proxy_cache off' \
  || fail "relayCacheMinUses=0 must leave the relay a pure stream"
if awk '/location @cc_relay/,/^ *}$/' "$TMP/on-nocache.yaml" | grep -q 'proxy_cache_min_uses'; then
  fail "relayCacheMinUses=0 must not emit proxy_cache_min_uses"
fi

echo "9. relay cost is observable, and duration buckets outlast a whole transfer"
# Without the route label a relayed request is indistinguishable from a local
# hit, which is what made the relay's latency cost unmeasurable.
grep -q '"cache_status", "http_status", "route"' "$TMP/on.yaml" \
  || fail "request/throughput metrics must carry the route label"
grep -q 'local route = "local"' "$TMP/on.yaml" || fail "route must default to local"
grep -q 'route = "relayed"' "$TMP/on.yaml" || fail "relayed requests must be labelled"
grep -q 'route = "peer"' "$TMP/on.yaml" || fail "requests served for a peer must be labelled"
# A relay that serves from its own replica never contacts the owner, so it must
# not be counted as a peer hop. Without this the label overstates relay traffic
# and hides the replication that removed the hop.
grep -q 'if cache_status == "HIT" then' "$TMP/on.yaml" \
  || fail "a relay-served cache hit must be classified local, not relayed"
# The objects here are whole model files, so a 10s ceiling put a large share of
# traffic in +Inf and histogram_quantile then reports the bucket edge, not a
# latency.
grep -q 'proxy_cache_request_duration_seconds' "$TMP/on.yaml" || fail "duration histogram missing"
awk '/proxy_cache_request_duration_seconds/{print; exit}' "$TMP/on.yaml" | grep -q '600' \
  || fail "duration buckets must extend past a full object transfer"

echo "9b. route label renders even with routing disabled (no undeclared-variable read)"
grep -q 'local route = "local"' "$TMP/off.yaml" || fail "route label must still render when routing is off"
[ "$(count 'ngx.var.cc_owner' "$TMP/off.yaml")" = 0 ] \
  || fail "must not read the routing variable when it is undeclared (OpenResty raises)"

echo "PASS: all consistent-hash routing render assertions hold"
