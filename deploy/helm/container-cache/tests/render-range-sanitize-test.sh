#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Rendered-output regression tests for malformed-Range sanitization. Run from
# the chart subtree:
#   bash tests/render-range-sanitize-test.sh
#
# Background: the NGC CLI sends "Range: bytes=0--1" for a zero-length file.
# nginx answers 416; the origin ignores the bad header and answers 200. We
# match the origin. See files/lua/safe-range.lua.
set -euo pipefail
CHART_DIR="$(cd "$(dirname "$0")/.." && pwd)/deploy"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }
count() { grep -Ec "$1" "$2" || true; }

helm template t "$CHART_DIR" > "$TMP/off.yaml" 2>/dev/null
helm template t "$CHART_DIR" --set consistentHashRouting.enabled=true --set replicaCount=3 > "$TMP/on.yaml" 2>/dev/null

echo "1. the sanitizer ships in the ConfigMap and is projected into the lua volume"
for f in "$TMP/off.yaml" "$TMP/on.yaml"; do
  grep -q 'safe-range.lua:' "$f" || fail "safe-range.lua missing from the ConfigMap"
  grep -q 'key: safe-range.lua' "$f" || fail "safe-range.lua not projected into the lua volume"
done

echo "2. it is unconditional (present whether or not hash routing is enabled)"
[ "$(count 'safe-range.lua:' "$TMP/off.yaml")" -ge 1 ] || fail "sanitizer must not be gated on consistentHashRouting"

echo "3. every range-consuming directive uses \$safe_range, never \$http_range"
for f in "$TMP/off.yaml" "$TMP/on.yaml"; do
  [ "$(count 'proxy_set_header Range \$http_range' "$f")" = 0 ] \
    || fail "a proxy_set_header Range still forwards the raw \$http_range"
  [ "$(count 'proxy_cache_key .*\$http_range' "$f")" = 0 ] \
    || fail "a proxy_cache_key still keys on the raw \$http_range"
  [ "$(count 'set \$cc_hash_key .*\$http_range' "$f")" = 0 ] \
    || fail "a routing key still hashes the raw \$http_range"
done

echo "4. no nginx directive still consumes the raw \$http_range"
# Only nginx directives are checked: a rendered directive is a line ending in
# ";". That deliberately excludes the log formats (we log what the client
# actually sent), comments, and lua reading ngx.var.http_range -- which covers
# both the sanitizer itself and the S3 slice block's $request_range. S3 is out
# of scope here: it has its own `slice` interaction and no reported failures.
python3 - "$TMP/on.yaml" <<'PY2' || fail "an nginx directive still consumes the raw \$http_range"
import sys
bad=[f'  line {n}: {l.strip()[:90]}'
     for n,l in enumerate(open(sys.argv[1]),1)
     if '$http_range' in l and l.rstrip().endswith(';')]
if bad:
    print('\n'.join(bad)); sys.exit(1)
PY2

echo "5. within each block, the sanitizer is set before \$safe_range is read"
# nginx caches $http_range once evaluated and $safe_range is empty until set,
# so a consumer placed above the sanitizer would silently drop the range.
python3 - "$TMP/on.yaml" <<'PY2' || fail "a \$safe_range consumer is ordered above its sanitizer"
import re,sys
text=open(sys.argv[1]).read()
bad=[]
for m in re.finditer(r'set_by_lua_file\s+\$safe_range', text):
    # Walk back to the nearest enclosing "location ... {" and check the span
    # between it and the sanitizer for any read of $safe_range.
    head = text[:m.start()]
    loc  = head.rfind('location')
    if loc == -1:
        bad.append('sanitizer outside any location block'); continue
    span = head[loc:]
    for consumer in ('proxy_cache_key', 'proxy_set_header Range', 'set $cc_hash_key'):
        if consumer in span and '$safe_range' in span[span.find(consumer):]:
            bad.append(f'{consumer!r} reads $safe_range before it is set')
if bad:
    print('\n'.join('  '+b for b in dict.fromkeys(bad))); sys.exit(1)
PY2

echo "6. routing key still equals the cache key (invariant preserved, new variable)"
grep -q 'set $cc_hash_key "$request_method|$uri|$arg_versionId|$safe_range"' "$TMP/on.yaml" \
  || fail "routing key must equal the proxy_cache_key"
grep -q 'proxy_cache_key $request_method|$uri|$arg_versionId|$safe_range' "$TMP/on.yaml" \
  || fail "cache key must use the sanitized range"

echo "PASS: all malformed-Range sanitization render assertions hold"
