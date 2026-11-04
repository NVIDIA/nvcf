#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Systematic Vanilla CRIU Checkpoint/Restore Test
# NO LD_PRELOAD, NO interventions
# Purpose: Establish baseline CRIU functionality
#
# Establishes a baseline CRIU checkpoint/restore (no LD_PRELOAD interventions)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"

export KUBECONFIG="${KUBECONFIG:?KUBECONFIG must point at your cluster kubeconfig — export it before running this script}"

# Colors
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

info() { echo -e "${GREEN}[INFO]${NC} $*"; }
warn() { echo -e "${YELLOW}[WARN]${NC} $*"; }
error() { echo -e "${RED}[ERROR]${NC} $*"; }
step() { echo -e "${BLUE}[STEP]${NC} $*"; }

NAMESPACE="nvsnap-system"
POD_NAME="vllm-vanilla-test"
RESTORE_POD_NAME="vllm-vanilla-restored"

echo ""
info "=============================================="
info "Systematic Vanilla CRIU Test"
info "NO LD_PRELOAD | NO interventions"
info "=============================================="
echo ""

# Test 1: Checkpoint
step "TEST 1: Vanilla Checkpoint Creation"
echo ""

info "1.1: Cleaning up old pods..."
kubectl delete pod $POD_NAME $RESTORE_POD_NAME -n $NAMESPACE --ignore-not-found
sleep 3

info "1.2: Deploying vanilla vLLM..."
kubectl apply -f "$PROJECT_ROOT/deploy/k8s/vllm-vanilla-checkpoint-test.yaml"

info "1.3: Waiting for vLLM to be ready (up to 180s)..."
if ! kubectl wait --for=condition=ready pod/$POD_NAME -n $NAMESPACE --timeout=180s; then
    error "Pod failed to start"
    kubectl logs -n $NAMESPACE $POD_NAME --tail=30
    exit 1
fi

info "1.4: Waiting for model to load (polling API, up to 300s)..."
MODEL_READY=false
for i in $(seq 1 60); do
    if kubectl exec -n $NAMESPACE $POD_NAME -- curl -s --max-time 3 http://localhost:8000/v1/models 2>/dev/null | grep -q "TinyLlama"; then
        info "✓ API responding - model loaded after ~$((i*5))s"
        MODEL_READY=true
        break
    fi
    sleep 5
done

if [ "$MODEL_READY" = "false" ]; then
    warn "Model not responding after 300s - continuing anyway"
    warn "Temp files may be missing from checkpoint if model is still loading"
fi

info "1.6: Creating checkpoint..."
CKPT_OUTPUT=$("$SCRIPT_DIR/checkpoint.sh" create $POD_NAME vllm $NAMESPACE 2>&1)
CKPT_ID=$(echo "$CKPT_OUTPUT" | grep "Checkpoint ID:" | awk '{print $NF}')

if [ -z "$CKPT_ID" ]; then
    error "Checkpoint creation failed"
    echo "$CKPT_OUTPUT"
    exit 1
fi

info "✓ Checkpoint created: $CKPT_ID"

# Verify checkpoint data
NODE=$(kubectl get pod $POD_NAME -n $NAMESPACE -o jsonpath='{.spec.nodeName}')
AGENT_POD=$(kubectl get pod -n $NAMESPACE -l app=nvsnap-agent --field-selector spec.nodeName=$NODE -o name | head -1)

info "1.7: Verifying checkpoint data integrity..."
PRESCAN_DATA=$(kubectl exec -n $NAMESPACE $AGENT_POD -- \
    grep "Pre-scan.*sq_entries" /var/lib/nvsnap/checkpoints/$CKPT_ID/dump.log 2>/dev/null || echo "")

if [ -z "$PRESCAN_DATA" ]; then
    warn "Could not find io_uring prescan data in dump.log"
else
    echo "$PRESCAN_DATA"

    if echo "$PRESCAN_DATA" | grep -qE "sq_entries=[0-9]{7,}"; then
        error "❌ CORRUPT DATA DETECTED - sq_entries > 1,000,000"
        error "The CRIU fix didn't work!"
        exit 1
    else
        info "✓ No corrupt data - all sq_entries values are reasonable"
    fi
fi

echo ""
step "TEST 1 COMPLETE: Checkpoint created with clean data"
echo ""

# Test 2: Restore
step "TEST 2: Vanilla Restore (same image instance)"
echo ""

info "2.1: Keeping original pod alive to preserve cached image..."
info "Pod: $POD_NAME (will delete after restore pod starts)"
sleep 2

info "2.2: Deploying restore pod..."
sed -e "s/CHECKPOINT_ID_PLACEHOLDER/$CKPT_ID/" \
    -e "s/NODE_NAME_PLACEHOLDER/$NODE/" \
    "$PROJECT_ROOT/deploy/k8s/vllm-vanilla-restore-test.yaml" | kubectl apply -f -

info "2.3: Deleting original pod NOW..."
kubectl delete pod $POD_NAME -n $NAMESPACE --wait=false

info "2.4: Waiting for restore pod (up to 60s)..."
sleep 30

POD_STATUS=$(kubectl get pod $RESTORE_POD_NAME -n $NAMESPACE -o jsonpath='{.status.phase}' 2>/dev/null || echo "NotFound")

echo ""
info "================================================"
info "TEST 2 RESULTS"
info "================================================"
echo ""

info "Pod status: $POD_STATUS"

if [ "$POD_STATUS" = "Running" ]; then
    info "✓ Restore pod is Running (no crash during init)"

    # Check for restored processes
    VLLM_PROCS=$(kubectl exec -n $NAMESPACE $RESTORE_POD_NAME -- pgrep -af "vllm" 2>/dev/null || echo "")

    if [ -n "$VLLM_PROCS" ]; then
        info "✓ vLLM processes found:"
        echo "$VLLM_PROCS" | head -5

        # Test API
        sleep 10
        if kubectl exec -n $NAMESPACE $RESTORE_POD_NAME -- curl -s http://localhost:8000/v1/models 2>/dev/null | grep -q "TinyLlama"; then
            echo ""
            info "=============================================="
            info "✅✅✅ SUCCESS - VANILLA CRIU WORKS! ✅✅✅"
            info "=============================================="
            info "vLLM restored and API responding"
            info "No interventions needed!"
            exit 0
        else
            warn "Processes restored but API not responding"
            warn "May need more time or there's an application issue"
        fi
    else
        warn "No vLLM processes found - restore may have failed"
    fi

elif [ "$POD_STATUS" = "Failed" ] || [ "$POD_STATUS" = "Error" ]; then
    EXIT_CODE=$(kubectl get pod $RESTORE_POD_NAME -n $NAMESPACE -o jsonpath='{.status.containerStatuses[0].state.terminated.exitCode}' 2>/dev/null || echo "unknown")
    error "Restore pod failed with exit code: $EXIT_CODE"

    if [ "$EXIT_CODE" = "139" ]; then
        error "SIGSEGV detected - process crashed"
    fi

    info "Checking logs for errors..."
    kubectl logs -n $NAMESPACE $RESTORE_POD_NAME 2>&1 | \
        grep -iE "error|fail|build-id|segfault" | head -20
fi

echo ""
info "Checkpoint: $CKPT_ID"
info "Node: $NODE"
info "Restore pod: $RESTORE_POD_NAME"
echo ""
info "For detailed logs:"
info "  kubectl logs -n $NAMESPACE $RESTORE_POD_NAME | less"
