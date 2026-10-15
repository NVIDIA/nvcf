#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Automated vLLM Checkpoint/Restore Test
# Tests the complete flow: deploy → wait → checkpoint → restore → verify inference
#
# Uses kubectl exec + curl inside the pod for API access (reliable, no port-forward).
# Prints a structured timing summary at the end.
#
# Exit codes: 0 = PASS, 1 = FAIL (with which step failed)

set -euo pipefail

SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"

# Colors
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m'

log_info() { echo -e "${GREEN}[INFO]${NC} $*"; }
log_error() { echo -e "${RED}[ERROR]${NC} $*"; }
log_warn() { echo -e "${YELLOW}[WARN]${NC} $*"; }

# Configuration
NAMESPACE="nvsnap-system"
POD_NAME="vllm-small"
CONTAINER_NAME="vllm"
RESTORE_POD_NAME="vllm-small-restored"
RESTORE_CONTAINER_NAME="restore"
VLLM_PORT=8000

# Timeouts (seconds)
POD_READY_TIMEOUT=600       # 10min: image pull + model download + torch compile
MODELS_POLL_TIMEOUT=600     # 10min: extra safety for /v1/models
MODELS_POLL_INTERVAL=30
INFERENCE_POLL_TIMEOUT=300  # 5min: inference warmup
INFERENCE_POLL_INTERVAL=30
RESTORE_READY_TIMEOUT=600   # 10min: CRIU restore + GPU restore
POST_MODELS_TIMEOUT=120     # 2min: post-restore /v1/models
POST_MODELS_INTERVAL=10
POST_INFER_TIMEOUT=120      # 2min: post-restore /v1/completions
POST_INFER_INTERVAL=10

# ─── Timing infrastructure ──────────────────────────────────────────────────
declare -a STEP_NAMES=()
declare -a STEP_DURATIONS=()
declare -a STEP_RESULTS=()
TEST_START=$(date +%s)

step_start() {
    CURRENT_STEP_START=$(date +%s)
}

step_done() {
    local name="$1" result="$2"
    local elapsed=$(( $(date +%s) - CURRENT_STEP_START ))
    STEP_NAMES+=("$name")
    STEP_DURATIONS+=("$elapsed")
    STEP_RESULTS+=("$result")
}

fmt_duration() {
    local secs="$1"
    printf "%dm %02ds" $((secs / 60)) $((secs % 60))
}

print_summary() {
    local total_elapsed=$(( $(date +%s) - TEST_START ))
    local overall="PASS"

    echo ""
    echo -e "${BOLD}${CYAN}Step                         Duration    Result${NC}"
    echo -e "${CYAN}──────────────────────────────────────────────────${NC}"
    for i in "${!STEP_NAMES[@]}"; do
        local name="${STEP_NAMES[$i]}"
        local dur=$(fmt_duration "${STEP_DURATIONS[$i]}")
        local res="${STEP_RESULTS[$i]}"
        local color="$GREEN"
        if [ "$res" = "FAIL" ]; then
            color="$RED"
            overall="FAIL"
        elif [ "$res" = "SKIP" ]; then
            color="$YELLOW"
        fi
        printf "%-28s %-11s ${color}%s${NC}\n" "$name" "$dur" "$res"
    done
    echo -e "${CYAN}──────────────────────────────────────────────────${NC}"
    local total_dur=$(fmt_duration "$total_elapsed")
    local total_color="$GREEN"
    if [ "$overall" = "FAIL" ]; then total_color="$RED"; fi
    printf "%-28s %-11s ${total_color}%s${NC}\n" "Total" "$total_dur" "$overall"
    echo ""

    if [ "$overall" = "FAIL" ]; then
        return 1
    fi
    return 0
}

# ─── Helper: call API via kubectl exec ────────────────────────────────────────
# Runs curl inside the pod — no port-forward needed, always reliable.
# Usage: pod_curl <pod> <container> <method> <path> [data] [timeout]
pod_curl() {
    local pod="$1" container="$2" method="$3" path="$4" data="${5:-}" timeout="${6:-10}"

    # Clear LD_PRELOAD so the intercept library doesn't load into curl
    if [ -n "$data" ]; then
        kubectl exec -n "$NAMESPACE" "$pod" -c "$container" -- \
            env LD_PRELOAD= curl -s -m "$timeout" -X "$method" "http://localhost:${VLLM_PORT}${path}" \
            -H "Content-Type: application/json" -d "$data" 2>/dev/null
    else
        kubectl exec -n "$NAMESPACE" "$pod" -c "$container" -- \
            env LD_PRELOAD= curl -s -m "$timeout" -X "$method" "http://localhost:${VLLM_PORT}${path}" 2>/dev/null
    fi
}

# ─── Helper: poll until API responds ──────────────────────────────────────────
# Usage: poll_api <pod> <container> <method> <path> <data> <grep_pattern> <timeout_secs> <interval_secs> <description>
# Returns: 0 on success (sets POLL_RESULT), 1 on timeout
POLL_RESULT=""
poll_api() {
    local pod="$1" container="$2" method="$3" path="$4" data="$5"
    local pattern="$6" timeout_secs="$7" interval="$8" desc="$9"

    local deadline=$(( $(date +%s) + timeout_secs ))
    local attempt=0
    while [ "$(date +%s)" -lt "$deadline" ]; do
        attempt=$((attempt + 1))
        POLL_RESULT=$(pod_curl "$pod" "$container" "$method" "$path" "$data" 30 || true)
        if echo "$POLL_RESULT" | grep -q "$pattern"; then
            log_info "  $desc OK (attempt $attempt)"
            return 0
        fi
        local remaining=$(( deadline - $(date +%s) ))
        if [ "$remaining" -le 0 ]; then break; fi
        log_info "  attempt $attempt: $desc not ready, retrying in ${interval}s (${remaining}s left)..."
        sleep "$interval"
    done
    log_error "  $desc not responding after ${timeout_secs}s"
    return 1
}

# ─── Fail handler ────────────────────────────────────────────────────────────
fail() {
    local step="$1"
    step_done "$step" "FAIL"
    log_error "FAILED at: $step"
    print_summary || true
    exit 1
}

log_info "=========================================="
log_info "vLLM Checkpoint/Restore Test"
log_info "=========================================="
echo ""

# ─── Step 1: Clean up ────────────────────────────────────────────────────────
log_info "Step 1: Cleaning up existing pods..."
kubectl delete pod $POD_NAME $RESTORE_POD_NAME -n $NAMESPACE --ignore-not-found
sleep 3

# ─── Step 2: Deploy vLLM ─────────────────────────────────────────────────────
log_info "Step 2: Deploying vLLM pod..."
kubectl apply -f "$PROJECT_ROOT/deploy/k8s/vllm-small.yaml"

# ─── Step 3: Wait for pod ready (readiness probe checks /v1/models) ──────────
step_start
log_info "Step 3: Waiting for pod ready (up to ${POD_READY_TIMEOUT}s)..."
if kubectl wait --for=condition=ready pod/$POD_NAME -n $NAMESPACE --timeout=${POD_READY_TIMEOUT}s; then
    step_done "Pod ready" "OK"
else
    kubectl logs $POD_NAME -n $NAMESPACE -c $CONTAINER_NAME --tail=20 || true
    fail "Pod ready"
fi

# ─── Step 4: Verify /v1/models ───────────────────────────────────────────────
step_start
log_info "Step 4: Verifying /v1/models responds..."
if poll_api "$POD_NAME" "$CONTAINER_NAME" GET /v1/models "" "TinyLlama" \
    "$MODELS_POLL_TIMEOUT" "$MODELS_POLL_INTERVAL" "/v1/models"; then
    step_done "Models API ready" "OK"
else
    kubectl logs $POD_NAME -n $NAMESPACE -c $CONTAINER_NAME --tail=30 || true
    fail "Models API ready"
fi

# ─── Step 5: Verify inference ────────────────────────────────────────────────
step_start
log_info "Step 5: Verifying inference works before checkpoint..."
if poll_api "$POD_NAME" "$CONTAINER_NAME" POST /v1/completions \
    '{"model":"TinyLlama/TinyLlama-1.1B-Chat-v1.0","prompt":"Hello","max_tokens":5}' \
    '"choices"' "$INFERENCE_POLL_TIMEOUT" "$INFERENCE_POLL_INTERVAL" "/v1/completions"; then
    echo "$POLL_RESULT" | python3 -m json.tool 2>/dev/null || echo "$POLL_RESULT"
    step_done "Pre-checkpoint infer" "OK"
else
    kubectl logs $POD_NAME -n $NAMESPACE -c $CONTAINER_NAME --tail=30 || true
    fail "Pre-checkpoint infer"
fi

# ─── Step 6: Get node ────────────────────────────────────────────────────────
POD_NODE=$(kubectl get pod $POD_NAME -n $NAMESPACE -o jsonpath='{.spec.nodeName}')
log_info "Pod on node: $POD_NODE"

# ─── Step 7: Create checkpoint ───────────────────────────────────────────────
step_start
log_info "Step 7: Creating checkpoint..."
CHECKPOINT_OUTPUT=$(${SCRIPT_DIR}/checkpoint.sh create $POD_NAME $CONTAINER_NAME $NAMESPACE 2>&1) || true
CHECKPOINT_ID=$(echo "$CHECKPOINT_OUTPUT" | grep "Checkpoint ID:" | awk '{print $NF}')

if [ -z "$CHECKPOINT_ID" ]; then
    log_error "Failed to create checkpoint"
    echo "$CHECKPOINT_OUTPUT"
    fail "Checkpoint"
fi
log_info "Checkpoint: $CHECKPOINT_ID"
step_done "Checkpoint" "OK"

# ─── Step 8: Delete original pod ─────────────────────────────────────────────
log_info "Step 8: Deleting original pod..."
kubectl delete pod $POD_NAME -n $NAMESPACE --wait=false
sleep 5

# ─── Step 9: Restore from checkpoint ─────────────────────────────────────────
step_start
log_info "Step 9: Restoring on node $POD_NODE..."
RESTORE_MANIFEST=$(mktemp)
trap "rm -f $RESTORE_MANIFEST" EXIT

sed -e "s|value: \"vllm-small__nvsnap-system__[0-9-]*\"|value: \"$CHECKPOINT_ID\"|" \
    -e "s|nodeName: .*|nodeName: $POD_NODE|" \
    "$PROJECT_ROOT/deploy/k8s/vllm-small-restore.yaml" > "$RESTORE_MANIFEST"

kubectl apply -f "$RESTORE_MANIFEST"

log_info "Waiting for restore pod ready (up to ${RESTORE_READY_TIMEOUT}s)..."
log_info "  (readiness probe polls /v1/models — succeeds only when vLLM is serving)"
if kubectl wait --for=condition=ready pod/$RESTORE_POD_NAME -n $NAMESPACE --timeout=${RESTORE_READY_TIMEOUT}s; then
    step_done "Restore pod ready" "OK"
else
    log_warn "Restore pod not ready, checking status..."
    kubectl get pod $RESTORE_POD_NAME -n $NAMESPACE -o wide || true
    kubectl logs $RESTORE_POD_NAME -n $NAMESPACE -c $RESTORE_CONTAINER_NAME --tail=30 || true
    fail "Restore pod ready"
fi

# ─── Step 10: Post-restore /v1/models ────────────────────────────────────────
step_start
log_info "Step 10: Verifying /v1/models after restore..."
if poll_api "$RESTORE_POD_NAME" "$RESTORE_CONTAINER_NAME" GET /v1/models "" "TinyLlama" \
    "$POST_MODELS_TIMEOUT" "$POST_MODELS_INTERVAL" "post-restore /v1/models"; then
    step_done "Post-restore models" "OK"
else
    kubectl logs $RESTORE_POD_NAME -n $NAMESPACE -c $RESTORE_CONTAINER_NAME --tail=30 || true
    fail "Post-restore models"
fi

# ─── Step 11: Post-restore /v1/completions ───────────────────────────────────
step_start
log_info "Step 11: Verifying /v1/completions after restore..."
if poll_api "$RESTORE_POD_NAME" "$RESTORE_CONTAINER_NAME" POST /v1/completions \
    '{"model":"TinyLlama/TinyLlama-1.1B-Chat-v1.0","prompt":"The meaning of life is","max_tokens":10}' \
    '"choices"' "$POST_INFER_TIMEOUT" "$POST_INFER_INTERVAL" "post-restore /v1/completions"; then
    echo "$POLL_RESULT" | python3 -m json.tool 2>/dev/null || echo "$POLL_RESULT"
    step_done "Post-restore infer" "OK"
else
    log_warn "Post-restore /v1/completions not working"
    step_done "Post-restore infer" "FAIL"
fi

# ─── Diagnostics ─────────────────────────────────────────────────────────────
echo ""
log_info "Key restore events:"
kubectl logs $RESTORE_POD_NAME -n $NAMESPACE -c $RESTORE_CONTAINER_NAME 2>&1 | \
    grep -E "wakeRestoredThreads|uv_loop_fork|libzmq.*CRIU|ETERM|reinit completed|RESTORE_COMPLETE" | head -10 || true

# ─── Summary ─────────────────────────────────────────────────────────────────
echo ""
log_info "=========================================="
if print_summary; then
    log_info "TEST PASSED"
else
    log_error "TEST FAILED"
fi
log_info "=========================================="
echo ""
log_info "Checkpoint: ${CHECKPOINT_ID:-<none>}"
log_info "Restored pod: $RESTORE_POD_NAME"
log_info "Cleanup: kubectl delete pod $RESTORE_POD_NAME -n $NAMESPACE"
