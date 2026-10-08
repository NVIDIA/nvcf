#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Rendered-output regression tests for the nginx-proxy probe and
# slow-storage resilience configuration. Run from the chart subtree:
#   make test
set -euo pipefail
CHART_DIR="$(cd "$(dirname "$0")/.." && pwd)/deploy"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }

helm template t "$CHART_DIR" > "$TMP/default.yaml" 2>/dev/null
helm template t "$CHART_DIR" --set probes.liveness.failureThreshold=99 \
  --set probes.readiness.timeoutSeconds=9 > "$TMP/probes-override.yaml" 2>/dev/null
helm template t "$CHART_DIR" --set cache.aioThreads=128 > "$TMP/threads-override.yaml" 2>/dev/null
helm template t "$CHART_DIR" --set cache.maxConnsPerServer=0 > "$TMP/cap-disabled.yaml" 2>/dev/null
helm template t "$CHART_DIR" --set cache.maxConnsPerServer=123 > "$TMP/cap-override.yaml" 2>/dev/null

echo "1. default render: liveness is tcpSocket on the http port, not httpGet"
grep -A12 "livenessProbe:" "$TMP/default.yaml" > "$TMP/liveness.txt"
grep -A1 "tcpSocket:" "$TMP/liveness.txt" | grep -Eq "^\s+port: http$" || fail "liveness must be tcpSocket on port http"
if grep -q "httpGet:" "$TMP/liveness.txt"; then fail "liveness still renders httpGet"; fi

echo "2. default render: readiness stays httpGet /healthz on the http port"
grep -A12 "readinessProbe:" "$TMP/default.yaml" > "$TMP/readiness.txt"
grep -q "httpGet:" "$TMP/readiness.txt" || fail "readiness must be httpGet"
grep -Eq "^\s+path: /healthz$" "$TMP/readiness.txt" || fail "readiness /healthz missing"
grep -Eq "^\s+port: http$" "$TMP/readiness.txt" || fail "readiness must probe port http"
if grep -q "tcpSocket:" "$TMP/readiness.txt"; then fail "readiness must not be tcpSocket"; fi

echo "3. probes overrides render"
grep -c "failureThreshold: 99" "$TMP/probes-override.yaml" >/dev/null || fail "liveness override not rendered"
grep -c "timeoutSeconds: 9" "$TMP/probes-override.yaml" >/dev/null || fail "readiness override not rendered"

echo "4. slow-storage resilience directives render with defaults"
grep -c "aio_write on;" "$TMP/default.yaml" >/dev/null || fail "aio_write missing"
grep -Eq "thread_pool default threads=64( |;)" "$TMP/default.yaml" || fail "thread_pool missing"
grep -Ec "limit_conn perserver 512;" "$TMP/default.yaml" >/dev/null || fail "limit_conn missing"
grep -c 'limit_conn_zone $server_name zone=perserver' "$TMP/default.yaml" >/dev/null || fail "limit_conn_zone missing"

echo "5. knob overrides: aioThreads resizes pool; maxConnsPerServer renders its value and 0 disables the cap"
grep -Eq "thread_pool default threads=128( |;)" "$TMP/threads-override.yaml" || fail "aioThreads override not rendered"
grep -Eq "limit_conn perserver 123;" "$TMP/cap-override.yaml" || fail "maxConnsPerServer=123 not rendered"
if grep -Eq "limit_conn perserver" "$TMP/cap-disabled.yaml"; then fail "cap=0 must disable limit_conn"; fi

echo "PASS: all rendered-output assertions hold"
