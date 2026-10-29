#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

#
# Smoke tests for deploy/helm/nvsnap chart. Regression-net for issues like
# nvsnap#54 where the chart's rendered ClusterRole was missing rules for
# nvsnap.io/gpucheckpoints, causing /api/v1/checkpoints to 500 on the
# deployed server.
#
# Self-contained — needs only `helm` on PATH; no cluster connection.
# Exit 0 = all checks pass. Any failure exits non-zero.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
CHART="$REPO_ROOT/deploy/helm/nvsnap"
RELEASE=nvsnap-test
NS=nvsnap-system

if ! command -v helm >/dev/null; then
  echo "FAIL: helm not on PATH" >&2
  exit 1
fi

fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "PASS: $*"; }

# NOTE: helm lint currently fails on this chart due to missing
# dependency declarations in Chart.yaml (jaeger, cert-manager,
# gpu-operator are referenced as subcharts but not declared as deps).
# Pre-existing on main; tracked separately. Skip lint until that's
# fixed so this regression test isn't held hostage by an unrelated bug.

# 1. helm template renders without error
RENDER=$(helm template "$RELEASE" "$CHART" --namespace "$NS") \
  || fail "helm template failed"
pass "helm template renders"

# 2. ClusterRole 'nvsnap-server' has the nvsnap.io rule block (nvsnap#54)
# Split the rendered output on '---\n' (YAML doc separators), then
# pick the document that's both a ClusterRole AND named nvsnap-server.
CR_BLOCK=$(echo "$RENDER" | awk 'BEGIN{RS="\n---\n"} /kind: ClusterRole/ && /name: nvsnap-server/ {print; exit}')
[[ -n "$CR_BLOCK" ]] || fail "no ClusterRole named 'nvsnap-server' in rendered output"
echo "$CR_BLOCK" | grep -q 'gpucheckpoints' \
  || fail "nvsnap-server ClusterRole missing gpucheckpoints rule (nvsnap#54 regression)"
echo "$CR_BLOCK" | grep -q 'gpucheckpoints/status' \
  || fail "nvsnap-server ClusterRole missing gpucheckpoints/status rule"
echo "$CR_BLOCK" | grep -q 'gpurestores' \
  || fail "nvsnap-server ClusterRole missing gpurestores rule"
echo "$CR_BLOCK" | grep -q 'gpurestores/status' \
  || fail "nvsnap-server ClusterRole missing gpurestores/status rule"
pass "nvsnap-server ClusterRole has nvsnap.io rules"

# services:get — the observability proxy probes whether grafana /
# jaeger / prometheus Services exist before rendering UI tiles.
# Without this rule the probe Forbidden-fails silently and every
# tile shows "not installed" even when the subcharts are deployed.
# Regression test for the GCP-H100-a outage 2026-06-02.
echo "$CR_BLOCK" | awk '
  /resources:.*services/ { in_svc_block = 1 }
  in_svc_block && /verbs:/ { print; in_svc_block = 0 }
' | grep -q 'get' \
  || fail "nvsnap-server ClusterRole missing services:get rule (observability discovery)"
pass "nvsnap-server ClusterRole has services:get rule"

# storageclasses:get on the agent ClusterRole — the L2 backend
# probes the configured SC at startup. Without this rule the
# probe Forbidden-fails and L2 silently disables itself.
# Regression test for GCP-H100-a 2026-06-02 post-MR-39.
AGENT_CR_BLOCK=$(echo "$RENDER" | awk 'BEGIN{RS="\n---\n"} /kind: ClusterRole/ && /name: nvsnap-agent/ {print; exit}')
[[ -n "$AGENT_CR_BLOCK" ]] || fail "no ClusterRole named 'nvsnap-agent' in rendered output"
echo "$AGENT_CR_BLOCK" | grep -q 'storageclasses' \
  || fail "nvsnap-agent ClusterRole missing storageclasses:get rule (L2 startup probe)"
pass "nvsnap-agent ClusterRole has storageclasses:get rule"

# 3. CRD manifests live in chart/crds/ so Helm installs them on
# `helm install` (nvsnap#54: CRDs were absent from the chart entirely).
[[ -d "$CHART/crds" ]]                           || fail "chart/crds directory missing"
[[ -f "$CHART/crds/nvsnap.io_gpucheckpoints.yaml" ]] || fail "chart/crds/nvsnap.io_gpucheckpoints.yaml missing"

# CRD file must declare both gpucheckpoints + gpurestores
CRD_FILE="$CHART/crds/nvsnap.io_gpucheckpoints.yaml"
grep -q 'name: gpucheckpoints.nvsnap.io' "$CRD_FILE" \
  || fail "CRD file missing gpucheckpoints.nvsnap.io"
grep -q 'name: gpurestores.nvsnap.io' "$CRD_FILE" \
  || fail "CRD file missing gpurestores.nvsnap.io"
pass "chart ships CRDs for gpucheckpoints + gpurestores"

# 4. The chart's CRD file must be byte-identical to the canonical source
# in deploy/crds/ — otherwise drift can ship a stale CRD via Helm while
# kubectl-apply users get the fresh one.
SRC_CRD="$REPO_ROOT/deploy/crds/nvsnap.io_gpucheckpoints.yaml"
diff -q "$SRC_CRD" "$CRD_FILE" >/dev/null \
  || fail "chart/crds/nvsnap.io_gpucheckpoints.yaml drifted from deploy/crds/nvsnap.io_gpucheckpoints.yaml"
pass "chart CRD identical to deploy/crds/ source"

# 4b. GPUCheckpoint CRD must declare every status field nvsnap-server writes,
# else K8s silently drops unknown fields and the hash never reaches the
# CRD — NVCA Hook B reads the empty hash, refuses to mark Warm, and
# retries forever (nvsnap#80, GCP-H100-a 2026-06-02). Same for the
# InProgress phase setCheckpointStatus emits at the start.
GPUCHK_BLOCK=$(awk '/kind: CustomResourceDefinition/{f=0} /name: gpucheckpoints/{f=1} f' "$CRD_FILE")
for field in 'checkpointHash:' 'checkpointPath:' 'checkpointSize:' 'nodeName:'; do
  echo "$GPUCHK_BLOCK" | grep -q "$field" \
    || fail "GPUCheckpoint status schema missing field $field (nvsnap-server writes it; K8s will silently drop)"
done
for phase in 'Pending' 'InProgress' 'Completed' 'Failed' 'Restoring' 'Restored'; do
  echo "$GPUCHK_BLOCK" | grep -q "    - $phase\$" \
    || fail "GPUCheckpoint phase enum missing value '$phase' (nvsnap-server emits it; K8s will reject the update)"
done
pass "GPUCheckpoint status schema covers every field nvsnap-server writes"

# 5. Every rendered nvsnap image ref must have a non-empty tag. Catches
# regressions like the v0.0.5 blobstore ImagePullBackOff (helm fell
# back to .Chart.AppVersion when the tag was empty in values.yaml,
# but that tag was never built+pushed). The chart now fails the
# render explicitly on empty tag — verify both the happy path here
# (all tags non-empty + correctly substituted) and the failure path.
while IFS= read -r ref; do
  case "$ref" in
    *:) fail "rendered image ref has empty tag: $ref" ;;
    nvcr.io/0651155215864979/ncp-dev/nvsnap-*:*)
      tag="${ref##*:}"
      [[ -n "$tag" ]] || fail "rendered image ref has empty tag after colon: $ref"
      ;;
  esac
done < <(echo "$RENDER" | grep -E '^\s*image:\s+nvcr\.io/0651155215864979/ncp-dev/nvsnap-' | sed -E 's/^\s*image:\s+//')
pass "every nvsnap image ref has a non-empty tag"

# 6. Mutation: wiping any image tag must fail the render with a
# clear error, not silently fall back to AppVersion. This is the
# regression test for the v0.0.5 blobstore ImagePullBackOff.
for img in agent server blobstore; do
  if helm template "$RELEASE" "$CHART" --namespace "$NS" --set "$img.image.tag=" >/dev/null 2>&1; then
    fail "empty $img.image.tag should fail the chart render but didn't"
  fi
done
if helm template "$RELEASE" "$CHART" --namespace "$NS" --set nvsnap.builderTag="" >/dev/null 2>&1; then
  fail "empty nvsnap.builderTag should fail the chart render but didn't"
fi
pass "empty image tags fail the chart render with a clear error"

echo "OK — all helm chart checks passed"
