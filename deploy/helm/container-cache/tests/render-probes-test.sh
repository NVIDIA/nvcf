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

echo "1. default render: liveness is tcpSocket, not httpGet"
grep -A12 "livenessProbe:" "$TMP/default.yaml" > "$TMP/liveness.txt"
grep -c "tcpSocket:" "$TMP/liveness.txt" >/dev/null || fail "liveness tcpSocket missing"
if grep -c "httpGet:" "$TMP/liveness.txt" >/dev/null; then fail "liveness still renders httpGet"; fi

echo "2. default render: readiness stays HTTP /healthz"
grep -A6 "readinessProbe:" "$TMP/default.yaml" | grep -c "path: /healthz" >/dev/null || fail "readiness /healthz missing"

echo "3. probes overrides render"
grep -c "failureThreshold: 99" "$TMP/probes-override.yaml" >/dev/null || fail "liveness override not rendered"
grep -c "timeoutSeconds: 9" "$TMP/probes-override.yaml" >/dev/null || fail "readiness override not rendered"

echo "4. slow-storage resilience directives render with defaults"
grep -c "aio_write on;" "$TMP/default.yaml" >/dev/null || fail "aio_write missing"
grep -Ec "thread_pool default threads=64" "$TMP/default.yaml" >/dev/null || fail "thread_pool missing"
grep -Ec "limit_conn perserver 512;" "$TMP/default.yaml" >/dev/null || fail "limit_conn missing"
grep -c 'limit_conn_zone $server_name zone=perserver' "$TMP/default.yaml" >/dev/null || fail "limit_conn_zone missing"

echo "5. knob overrides: aioThreads resizes pool; maxConnsPerServer=0 disables cap"
grep -Ec "thread_pool default threads=128" "$TMP/threads-override.yaml" >/dev/null || fail "aioThreads override not rendered"
if grep -Ec "limit_conn perserver" "$TMP/cap-disabled.yaml" >/dev/null; then fail "cap=0 must disable limit_conn"; fi

echo "PASS: all rendered-output assertions hold"
