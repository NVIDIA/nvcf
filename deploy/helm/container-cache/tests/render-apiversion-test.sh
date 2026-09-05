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
  python3 - "$TMP/$label.yaml" "$label" <<'PY'
import re, sys, yaml
path, label = sys.argv[1], sys.argv[2]
text = open(path).read()
# Raw documents, so a failure can point at the comment line the field was
# trimmed onto. A comment that merely mentions "kind:" in a valid document is
# not an error.
raw_docs = [d for d in re.split(r"^---[ \t]*$", text, flags=re.M)]
bad = []
n = 0
for raw in raw_docs:
    body = [l for l in raw.splitlines() if l.strip() and not l.lstrip().startswith("#")]
    if not body:
        continue  # separator gap, or a template that rendered only its "# Source:" header under these values
    d = yaml.safe_load(raw)
    if not isinstance(d, dict):
        bad.append(("non-mapping document", body[0][:80]))
        continue
    n += 1
    for field in ("apiVersion", "kind"):
        if not d.get(field):
            glued = next((l.strip() for l in raw.splitlines() if l.lstrip().startswith("#") and f"{field}:" in l), None)
            bad.append((d.get("kind"), (d.get("metadata") or {}).get("name"), f"missing {field}", f"glued onto comment: {glued}" if glued else ""))
if n == 0:
    bad.append(("no documents rendered", ""))
if bad:
    print(f"FAIL: {label}: {bad}", file=sys.stderr)
    sys.exit(1)
print(f"{label}: {n} documents, all carry apiVersion and kind")
PY
}

check default
check consistent-hash --set consistentHashRouting.enabled=true --set replicaCount=3
check pvc --set persistentVolumeClaim.storageClassName=nvcf-cc-sc
check pdb --set podDisruptionBudget.enabled=true --set podDisruptionBudget.minAvailable=1
echo "PASS: apiVersion render tests"
