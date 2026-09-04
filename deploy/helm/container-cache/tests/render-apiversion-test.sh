#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Every rendered document must carry apiVersion and kind. A trailing "-}}" on
# a template action placed right after the license header swallows the
# newline and glues "apiVersion: v1" onto the last comment line; helm lint
# accepts the result and ArgoCD then fails the sync with "groupVersion
# shouldn't be empty". Run from the chart subtree:
#   bash tests/render-apiversion-test.sh
set -euo pipefail
CHART_DIR="$(cd "$(dirname "$0")/.." && pwd)/deploy"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }

check() { # $1 label, remaining args: helm --set flags
  local label="$1"; shift
  helm template t "$CHART_DIR" "$@" > "$TMP/$label.yaml" 2>/dev/null || fail "$label: helm template failed"
  # A comment line that ends in "apiVersion:" is the exact symptom.
  if grep -nE '^#.*apiVersion:' "$TMP/$label.yaml"; then fail "$label: apiVersion glued onto a comment line"; fi
  python3 - "$TMP/$label.yaml" "$label" <<'PY'
import sys, yaml
path, label = sys.argv[1], sys.argv[2]
bad = []
n = 0
for d in yaml.safe_load_all(open(path)):
    if not d:
        continue
    n += 1
    if not d.get("apiVersion") or not d.get("kind"):
        bad.append((d.get("kind"), (d.get("metadata") or {}).get("name"), d.get("apiVersion")))
if bad:
    print(f"FAIL: {label}: documents without apiVersion/kind: {bad}", file=sys.stderr)
    sys.exit(1)
print(f"{label}: {n} documents, all carry apiVersion and kind")
PY
}

check default
check consistent-hash --set consistentHashRouting.enabled=true --set replicaCount=3
check pvc --set persistentVolumeClaim.storageClassName=nvcf-cc-sc
check pdb --set podDisruptionBudget.enabled=true --set podDisruptionBudget.minAvailable=1
echo "PASS: apiVersion render tests"
