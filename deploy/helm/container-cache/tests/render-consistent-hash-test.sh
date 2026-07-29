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
done

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

echo "6. enabled: routing keys on the full cache key; marker rejection + one-hop guard present"
grep -q 'set $cc_hash_key "$request_method|$uri|$arg_versionId|$http_range"' "$TMP/on.yaml" || fail "routing key must equal the proxy_cache_key"
grep -q 'X-NVCF-CC-Relayed' "$TMP/on.yaml" || fail "one-hop relay marker missing"

echo "PASS: all consistent-hash routing render assertions hold"
