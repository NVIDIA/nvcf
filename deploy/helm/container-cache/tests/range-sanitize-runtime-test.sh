#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Behavioural test for files/lua/safe-range.lua. Runs the real sanitizer inside
# OpenResty against a mock origin that tolerates a malformed Range the way the
# NGC CDN does, and asserts we match the origin instead of answering 416.
#
#   bash tests/range-sanitize-runtime-test.sh
#
# Requires docker and python3. Skips cleanly when docker is unavailable.
set -euo pipefail
CHART_DIR="$(cd "$(dirname "$0")/.." && pwd)/deploy"
LUA_SRC="$CHART_DIR/files/lua/safe-range.lua"
IMAGE="${OPENRESTY_IMAGE:-openresty/openresty:alpine}"
PORT_PROXY=18190
PORT_ORIGIN=18191

command -v docker >/dev/null 2>&1 || { echo "SKIP: docker not available"; exit 0; }
docker info >/dev/null 2>&1 || { echo "SKIP: docker daemon not reachable"; exit 0; }
[ -f "$LUA_SRC" ] || { echo "FAIL: $LUA_SRC missing" >&2; exit 1; }

TMP="$(mktemp -d)"
CONT="ccrange-$$"
cleanup() {
  docker rm -f "$CONT" >/dev/null 2>&1 || true
  [ -n "${ORIGIN_PID:-}" ] && kill "$ORIGIN_PID" 2>/dev/null || true
  rm -rf "$TMP"
}
trap cleanup EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }

mkdir -p "$TMP/lua"
cp "$LUA_SRC" "$TMP/lua/safe-range.lua"

cat > "$TMP/origin.py" <<PY
# Mock NGC/CloudFront origin: ignores a malformed Range, always answers 200.
from http.server import BaseHTTPRequestHandler, HTTPServer
BODIES = {'/empty': b'', '/nonempty': b'A'*1000}
class H(BaseHTTPRequestHandler):
    protocol_version = 'HTTP/1.1'
    def do_GET(self):
        b = BODIES.get(self.path.split('?')[0])
        if b is None:
            self.send_response(404); self.send_header('Content-Length','0'); self.end_headers(); return
        self.send_response(200)
        self.send_header('Content-Length', str(len(b)))
        self.send_header('Accept-Ranges','bytes')
        self.send_header('X-Origin-Saw-Range', self.headers.get('Range') or 'none')
        self.end_headers(); self.wfile.write(b)
    def log_message(self,*a): pass
HTTPServer(('127.0.0.1',$PORT_ORIGIN), H).serve_forever()
PY

cat > "$TMP/nginx.conf" <<NGINX
worker_processes 1;
events {}
error_log /dev/stderr warn;
http {
  proxy_cache_path /cache levels=1:2 keys_zone=z:10m use_temp_path=off;
  access_log off;
  server {
    listen $PORT_PROXY;
    location / {
      proxy_cache z;
      proxy_cache_valid 200 206 1h;
      add_header X-Cache \$upstream_cache_status always;
      add_header X-Key "\$request_method|\$uri|\$safe_range" always;
      set_by_lua_file \$safe_range /etc/lua/safe-range.lua;
      set \$cc_hash_key "\$request_method|\$uri|\$safe_range";
      proxy_set_header Range \$safe_range;
      proxy_cache_key \$request_method|\$uri|\$safe_range;
      proxy_pass http://127.0.0.1:$PORT_ORIGIN;
    }
  }
}
NGINX

python3 "$TMP/origin.py" & ORIGIN_PID=$!
docker run --rm -d --name "$CONT" --network host \
  -v "$TMP/nginx.conf:/usr/local/openresty/nginx/conf/nginx.conf:ro" \
  -v "$TMP/lua:/etc/lua:ro" "$IMAGE" >/dev/null

for _ in $(seq 1 25); do
  curl -sf -o /dev/null "http://127.0.0.1:$PORT_PROXY/nonempty" 2>/dev/null && break
  sleep 0.4
done
curl -sf -o /dev/null "http://127.0.0.1:$PORT_PROXY/nonempty" \
  || { docker logs "$CONT" 2>&1 | tail -20; fail "proxy did not come up"; }

probe() { # path, range -> "code size"
  if [ "$2" = "-" ]; then
    curl -sS -o /dev/null -w '%{http_code} %{size_download}' "http://127.0.0.1:$PORT_PROXY$1"
  else
    curl -sS -o /dev/null -w '%{http_code} %{size_download}' -H "Range: $2" "http://127.0.0.1:$PORT_PROXY$1"
  fi
}
expect() { # path, range, want, why
  got="$(probe "$1" "$2")"
  [ "$got" = "$3" ] || fail "$1 with Range '$2': expected [$3], got [$got] -- $4"
  printf '   ok  %-12s %-14s -> %s\n' "$1" "$2" "$got"
}

echo "1. a malformed range is neutralised, matching the origin's 200"
expect /empty    "bytes=0--1" "200 0"    "the reported NGC CLI failure: zero-length file"
expect /nonempty "bytes=0--1" "200 1000" "the fault is Range parsing, not zero length"

echo "2. valid ranges are untouched"
expect /nonempty "bytes=0-0"    "206 1"    "single byte range must still work"
expect /nonempty "bytes=0-99"   "206 100"  "bounded range must still work"
expect /nonempty "bytes=-100"   "206 100"  "suffix range must still work"
expect /nonempty "bytes=900-"   "206 100"  "open-ended range must still work"

echo "2b. what the origin actually receives (the sanitizer's real contract)"
saw() { # path, range -> the Range header the origin observed
  curl -sS -o /dev/null -D- -H "Range: $2" "http://127.0.0.1:$PORT_PROXY$1?cb=$RANDOM" \
    | tr -d '\r' | awk -F': ' 'tolower($1)=="x-origin-saw-range"{print $2; exit}'
}
forwards() { # path, range, expected-at-origin, why
  got="$(saw "$1" "$2")"
  [ "$got" = "$3" ] || fail "$1 with Range '$2': origin saw '$got', expected '$3' -- $4"
  printf '   ok  origin saw %-18s for %s\n' "'$got'" "'$2'"
}
forwards /nonempty "bytes=0-9,20-29" "bytes=0-9,20-29" "multi-range must reach the origin unchanged"
forwards /nonempty "bytes=0-99"      "bytes=0-99"      "valid range must reach the origin unchanged"
forwards /nonempty "bytes=0--1"      "none"            "malformed range must be stripped before the origin"
forwards /empty    "bytes=0--1"      "none"            "malformed range stripped on the zero-length file too"

echo "3. a parseable but unsatisfiable range keeps nginx's conformant 416"
expect /nonempty "bytes=100-50" "416 203"  "RFC 7233 3.1 says SHOULD 416; do not paper over it"
expect /empty    "bytes=0-0"    "200 0"    "zero-length with a valid range is not an error"

echo "4. no range is unaffected"
expect /empty    "-" "200 0"
expect /nonempty "-" "200 1000"

echo "5. a sanitized request shares the cache entry with an unranged one"
docker exec "$CONT" sh -c 'rm -rf /cache/*' >/dev/null 2>&1 || true
docker restart "$CONT" >/dev/null
for _ in $(seq 1 25); do curl -sf -o /dev/null "http://127.0.0.1:$PORT_PROXY/nonempty" 2>/dev/null && break; sleep 0.4; done
curl -sS -o /dev/null "http://127.0.0.1:$PORT_PROXY/nonempty"
hdrs="$(curl -sS -o /dev/null -D- -H 'Range: bytes=0--1' "http://127.0.0.1:$PORT_PROXY/nonempty")"
echo "$hdrs" | grep -qi 'X-Cache: HIT' \
  || fail "malformed-range request did not reuse the unranged cache entry (duplicate entry)"
echo "$hdrs" | grep -qi 'X-Key: GET|/nonempty|$' \
  || echo "$hdrs" | grep -qiE 'X-Key: GET\|/nonempty\|[[:space:]]*$' \
  || fail "cache key still carries the malformed range"
echo "   ok  shares the cache key with the unranged request"

echo "PASS: safe-range.lua behaves correctly against a tolerant origin"
