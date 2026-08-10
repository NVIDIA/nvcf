#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# validate-test-env.sh - Ensure test environment has correct versions
#
# Run this BEFORE checkpoint/restore testing to avoid testing stale binaries.
# This script validates that all components have the expected version.

set -e

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

EXPECTED_VERSION="${1:-}"
NAMESPACE="${NAMESPACE:-nvsnap-system}"
VLLM_POD="${VLLM_POD:-vllm-small}"

if [ -z "$EXPECTED_VERSION" ]; then
    echo "Usage: $0 <expected-version>"
    echo "Example: $0 v0.7.1"
    exit 1
fi

echo "=========================================="
echo "Validating test environment for $EXPECTED_VERSION"
echo "=========================================="

ERRORS=0

# 1. Check local library was built recently
echo -e "\n${YELLOW}[1/6] Checking local library build...${NC}"
LOCAL_LIB="lib/nvsnap_intercept/src/io_uring_intercept.c"
if [ -f "$LOCAL_LIB" ]; then
    MODIFIED=$(stat -c %Y "$LOCAL_LIB" 2>/dev/null || stat -f %m "$LOCAL_LIB")
    NOW=$(date +%s)
    AGE_HOURS=$(( (NOW - MODIFIED) / 3600 ))
    if [ $AGE_HOURS -gt 2 ]; then
        echo -e "${YELLOW}  WARNING: $LOCAL_LIB last modified $AGE_HOURS hours ago${NC}"
    else
        echo -e "${GREEN}  OK: Source modified recently ($AGE_HOURS hours ago)${NC}"
    fi
fi

# 2. Check Docker image exists locally
echo -e "\n${YELLOW}[2/6] Checking Docker image...${NC}"
IMAGE="nvcr.io/0651155215864979/ncp-dev/nvsnap-agent:$EXPECTED_VERSION"
if docker image inspect "$IMAGE" >/dev/null 2>&1; then
    CREATED=$(docker image inspect "$IMAGE" --format '{{.Created}}' | cut -d'T' -f1,2 | tr 'T' ' ')
    echo -e "${GREEN}  OK: Image exists locally (created: $CREATED)${NC}"
else
    echo -e "${RED}  ERROR: Image $IMAGE not found locally${NC}"
    echo "  Run: ./scripts/build-agent-app.sh"
    ERRORS=$((ERRORS + 1))
fi

# 3. Check agent DaemonSet is using correct image
echo -e "\n${YELLOW}[3/6] Checking agent DaemonSet...${NC}"
DS_IMAGE=$(kubectl get daemonset nvsnap-agent -n $NAMESPACE -o jsonpath='{.spec.template.spec.containers[0].image}' 2>/dev/null || echo "NOT_FOUND")
if [ "$DS_IMAGE" = "$IMAGE" ]; then
    echo -e "${GREEN}  OK: DaemonSet uses $EXPECTED_VERSION${NC}"
else
    echo -e "${RED}  ERROR: DaemonSet uses $DS_IMAGE (expected $IMAGE)${NC}"
    echo "  Run: kubectl apply -f deploy/k8s/agent-daemonset.yaml"
    ERRORS=$((ERRORS + 1))
fi

# 4. Check agent pods are running with correct image
echo -e "\n${YELLOW}[4/6] Checking agent pods...${NC}"
AGENT_PODS=$(kubectl get pods -n $NAMESPACE -l app=nvsnap-agent -o jsonpath='{.items[*].metadata.name}')
for POD in $AGENT_PODS; do
    POD_IMAGE=$(kubectl get pod $POD -n $NAMESPACE -o jsonpath='{.spec.containers[0].image}')
    POD_AGE=$(kubectl get pod $POD -n $NAMESPACE -o jsonpath='{.metadata.creationTimestamp}')
    if [ "$POD_IMAGE" = "$IMAGE" ]; then
        echo -e "${GREEN}  OK: $POD uses $EXPECTED_VERSION (started: $POD_AGE)${NC}"
    else
        echo -e "${RED}  ERROR: $POD uses $POD_IMAGE${NC}"
        ERRORS=$((ERRORS + 1))
    fi
done

# 5. Check vLLM pod (if exists) has correct library
echo -e "\n${YELLOW}[5/6] Checking vLLM pod library...${NC}"
if kubectl get pod $VLLM_POD -n $NAMESPACE >/dev/null 2>&1; then
    INIT_IMAGE=$(kubectl get pod $VLLM_POD -n $NAMESPACE -o jsonpath='{.spec.initContainers[0].image}')
    if [ "$INIT_IMAGE" = "$IMAGE" ]; then
        echo -e "${GREEN}  OK: $VLLM_POD init container uses $EXPECTED_VERSION${NC}"
    else
        echo -e "${RED}  ERROR: $VLLM_POD init container uses $INIT_IMAGE${NC}"
        echo "  Delete and recreate: kubectl delete pod $VLLM_POD -n $NAMESPACE"
        ERRORS=$((ERRORS + 1))
    fi
    
    # Verify library is actually loaded
    LIB_CHECK=$(kubectl exec $VLLM_POD -n $NAMESPACE -- cat /proc/1/maps 2>/dev/null | grep nvsnap | head -1 || echo "")
    if [ -n "$LIB_CHECK" ]; then
        echo -e "${GREEN}  OK: Library loaded in vLLM process${NC}"
    else
        echo -e "${YELLOW}  WARNING: Could not verify library is loaded${NC}"
    fi
else
    echo -e "${YELLOW}  SKIP: $VLLM_POD not found${NC}"
fi

# 6. Check manifests match expected version
echo -e "\n${YELLOW}[6/6] Checking manifest versions...${NC}"
for MANIFEST in deploy/k8s/vllm-small.yaml deploy/k8s/vllm-small-restore.yaml deploy/k8s/agent-daemonset.yaml; do
    if [ -f "$MANIFEST" ]; then
        if grep -q "$IMAGE" "$MANIFEST"; then
            echo -e "${GREEN}  OK: $MANIFEST uses $EXPECTED_VERSION${NC}"
        else
            ACTUAL=$(grep "nvsnap-agent:" "$MANIFEST" | head -1 | sed 's/.*nvsnap-agent:/nvsnap-agent:/')
            echo -e "${RED}  ERROR: $MANIFEST uses $ACTUAL${NC}"
            ERRORS=$((ERRORS + 1))
        fi
    fi
done

# Summary
echo -e "\n=========================================="
if [ $ERRORS -eq 0 ]; then
    echo -e "${GREEN}✓ All checks passed! Ready for testing.${NC}"
else
    echo -e "${RED}✗ $ERRORS errors found. Fix before testing!${NC}"
    exit 1
fi
echo "=========================================="
