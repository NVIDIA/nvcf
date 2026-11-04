#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Build, push, deploy, and test — with verification at each step.
# Usage: ./scripts/build-and-test.sh [workload]
# Example: ./scripts/build-and-test.sh vllm-small

set -euo pipefail

SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
source "$SCRIPT_DIR/versions.sh"

WORKLOAD="${1:-vllm-small}"
EXPECTED_IMAGE="${NVSNAP_REGISTRY}/nvsnap-agent:${NVSNAP_APP_VERSION}"

RED='\033[0;31m'
GREEN='\033[0;32m'
NC='\033[0m'

fail() { echo -e "${RED}[FAIL]${NC} $*"; exit 1; }
ok() { echo -e "${GREEN}[OK]${NC} $*"; }

# Step 1: Sync manifests
echo "=== Step 1: Sync manifests ==="
"$SCRIPT_DIR/sync-versions.sh"
ok "Manifests synced to $NVSNAP_APP_VERSION"

# Step 2: Build base (only if needed — check if it exists)
echo "=== Step 2: Check/build base image ==="
BASE_IMAGE="${NVSNAP_REGISTRY}/nvsnap-agent-base:${NVSNAP_BASE_VERSION}"
if docker manifest inspect "$BASE_IMAGE" >/dev/null 2>&1; then
    ok "Base image $NVSNAP_BASE_VERSION exists in registry"
else
    echo "Base image not found — building..."
    NO_CACHE=1 "$SCRIPT_DIR/build-agent.sh" base || fail "Base build failed"
    "$SCRIPT_DIR/build-agent.sh" push-base || fail "Base push failed"
    docker manifest inspect "$BASE_IMAGE" >/dev/null 2>&1 || fail "Base image still not in registry after push"
    ok "Base image built and pushed"
fi

# Step 3: Build app
echo "=== Step 3: Build app image ==="
"$SCRIPT_DIR/build-agent.sh" app || fail "App build failed"
"$SCRIPT_DIR/build-agent.sh" push-app || fail "App push failed"
docker manifest inspect "$EXPECTED_IMAGE" >/dev/null 2>&1 || fail "App image not in registry after push"
ok "App image $NVSNAP_APP_VERSION built and pushed"

# Step 4: Deploy
echo "=== Step 4: Deploy agents ==="
"$SCRIPT_DIR/build-agent.sh" deploy || fail "Deploy failed"
DEPLOYED=$(kubectl get ds nvsnap-agent -n nvsnap-system -o jsonpath='{.spec.template.spec.containers[0].image}' 2>/dev/null)
[ "$DEPLOYED" = "$EXPECTED_IMAGE" ] || fail "Deployed ($DEPLOYED) != expected ($EXPECTED_IMAGE)"
ok "Agents deployed: $DEPLOYED"

# Step 5: Clean old checkpoints
echo "=== Step 5: Clean checkpoints ==="
kubectl delete pod "$WORKLOAD" "${WORKLOAD}-restored" -n nvsnap-system --force --grace-period=0 2>/dev/null || true
for p in $(kubectl get pods -n nvsnap-system -l app=nvsnap-agent -o jsonpath='{.items[*].metadata.name}'); do
    kubectl exec -n nvsnap-system "$p" -- sh -c 'rm -rf /var/lib/nvsnap/checkpoints/*' 2>/dev/null || true
done
ok "Checkpoints cleaned"

# Step 6: Test
echo "=== Step 6: Run e2e test ==="
"$SCRIPT_DIR/test-e2e.sh" "$WORKLOAD"
