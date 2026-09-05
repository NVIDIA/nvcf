#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Rendered-output regression tests for cache eviction sizing. An emptyDir
# shares the node filesystem, so min_free alone never fires there; the chart
# must add a per-zone max_size in that mode and only in that mode. Run from
# the chart subtree:
#   bash tests/render-emptydir-max-size-test.sh
set -euo pipefail
CHART_DIR="$(cd "$(dirname "$0")/.." && pwd)/deploy"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }
count() { grep -Ec "$1" "$2" || true; }
paths() { grep -E '^\s*proxy_cache_path ' "$1"; }

helm template t "$CHART_DIR" > "$TMP/default.yaml" 2>/dev/null
helm template t "$CHART_DIR" --set persistentVolumeClaim.storageClassName=nvcf-cc-sc > "$TMP/pvc.yaml" 2>/dev/null
helm template t "$CHART_DIR" --set persistentVolumeClaim.sizeProxyGB=5000 --set persistentVolumeClaim.sizeGB=1000 --set persistentVolumeClaim.freeProxyPct=15 > "$TMP/big.yaml" 2>/dev/null
helm template t "$CHART_DIR" --set persistentVolumeClaim.freeProxyPct=100 > "$TMP/edge.yaml" 2>/dev/null

echo "1. every render declares exactly three cache paths"
for f in default pvc big edge; do
  [ "$(count '^\s*proxy_cache_path ' "$TMP/$f.yaml")" = 3 ] || fail "$f: expected 3 proxy_cache_path directives"
done

echo "2. emptydir (chart default 200/100/15): max_size is 85 percent of each zone's own size"
paths "$TMP/default.yaml" | grep -q '/container_cache levels=1:2 max_size=85g min_free=30g' || fail "container zone: expected max_size=85g min_free=30g"
paths "$TMP/default.yaml" | grep -q '/proxy_cache/s3 levels=1:2 max_size=170g min_free=30g' || fail "s3 zone: expected max_size=170g min_free=30g"
paths "$TMP/default.yaml" | grep -q '/proxy_cache/ngc levels=1:2 max_size=170g min_free=30g' || fail "ngc zone: expected max_size=170g min_free=30g"

echo "3. emptydir: max_size stays below the emptyDir sizeLimit kubelet enforces"
grep -q 'sizeLimit: "200Gi"' "$TMP/default.yaml" || fail "proxy-cache emptyDir sizeLimit must be 200Gi"
grep -q 'sizeLimit: "100Gi"' "$TMP/default.yaml" || fail "cache emptyDir sizeLimit must be 100Gi"

echo "4. PersistentVolume: no max_size renders and min_free is unchanged"
[ "$(count 'max_size=' "$TMP/pvc.yaml")" = 0 ] || fail "max_size leaked into the PVC render"
[ "$(count 'min_free=30g' "$TMP/pvc.yaml")" = 3 ] || fail "PVC render must keep min_free=30g on all three zones"
grep -q 'storageClassName: "nvcf-cc-sc"' "$TMP/pvc.yaml" || fail "PVC render must carry the storage class"

echo "5. emptydir sized for a large node (5000/1000/15): caps scale with the configured sizes"
paths "$TMP/big.yaml" | grep -q '/proxy_cache/ngc levels=1:2 max_size=4250g min_free=750g' || fail "ngc zone: expected max_size=4250g min_free=750g"
paths "$TMP/big.yaml" | grep -q '/container_cache levels=1:2 max_size=850g min_free=750g' || fail "container zone: expected max_size=850g"
grep -q 'sizeLimit: "5000Gi"' "$TMP/big.yaml" || fail "proxy-cache emptyDir sizeLimit must follow sizeProxyGB"

echo "6. freeProxyPct=100 clamps max_size to 1g instead of rendering 0g"
[ "$(count 'max_size=1g' "$TMP/edge.yaml")" = 3 ] || fail "expected max_size=1g on all zones at freeProxyPct=100"
[ "$(count 'max_size=0g' "$TMP/edge.yaml")" = 0 ] || fail "max_size=0g must never render"

echo "PASS: emptydir max_size render tests"
