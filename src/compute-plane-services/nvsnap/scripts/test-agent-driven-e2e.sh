#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Agent-driven E2E checkpoint/restore test.
#
# Mirrors scripts/test-e2e.sh but exercises the AGENT-DRIVEN restore
# path (sleep-only placeholder pod + nvsnap-agent /v1/restore API),
# not the legacy restore-entrypoint path (privileged restore pod).
#
# Usage:
#   ./scripts/test-agent-driven-e2e.sh vllm-small
#   ./scripts/test-agent-driven-e2e.sh vllm-8b
#   ./scripts/test-agent-driven-e2e.sh sglang-small
#   ./scripts/test-agent-driven-e2e.sh sglang-8b
#   ./scripts/test-agent-driven-e2e.sh trtllm-small
#
# Exit codes: 0 = PASS, 1 = FAIL (with which step failed)

set -euo pipefail

WORKLOAD="${1:-}"
if [ -z "$WORKLOAD" ]; then
    echo "usage: $0 <workload>" >&2
    exit 2
fi

SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"
NS="nvsnap-system"

WORKLOAD_YAML="$PROJECT_ROOT/deploy/k8s/${WORKLOAD}.yaml"
PLACEHOLDER_YAML="$PROJECT_ROOT/deploy/k8s/${WORKLOAD}-agent-placeholder.yaml"

if [ ! -f "$WORKLOAD_YAML" ] || [ ! -f "$PLACEHOLDER_YAML" ]; then
    echo "missing manifest(s): $WORKLOAD_YAML or $PLACEHOLDER_YAML" >&2
    exit 1
fi

# Verify deployed agent matches expected version (catches forgot-to-deploy).
"$SCRIPT_DIR/sync-versions.sh" >/dev/null 2>&1 || true
source "$SCRIPT_DIR/versions.sh"
DEPLOYED=$(kubectl get ds nvsnap-agent -n "$NS" -o jsonpath='{.spec.template.spec.containers[0].image}' 2>/dev/null || true)
EXPECTED="${NVSNAP_REGISTRY}/nvsnap-agent:${NVSNAP_APP_VERSION}"
if [ "$DEPLOYED" != "$EXPECTED" ]; then
    echo "ERROR: agent image mismatch (deployed=$DEPLOYED expected=$EXPECTED)" >&2
    echo "       run: ./scripts/build-agent.sh app push-app && kubectl rollout restart ds/nvsnap-agent -n $NS" >&2
    exit 1
fi

GREEN='\033[0;32m'; RED='\033[0;31m'; YELLOW='\033[1;33m'; CYAN='\033[0;36m'; BOLD='\033[1m'; RESET='\033[0m'
log()  { echo -e "${GREEN}[INFO]${RESET} $*"; }
warn() { echo -e "${YELLOW}[WARN]${RESET} $*"; }
err()  { echo -e "${RED}[ERROR]${RESET} $*"; }

# Timing
declare -A TIMINGS
declare -A RESULTS
STEP_START=0
step_begin() { STEP_START=$(date +%s); }
step_end()   { local n="$1" r="$2"; TIMINGS[$n]=$(( $(date +%s) - STEP_START )); RESULTS[$n]="$r"; }

print_summary() {
    echo
    echo -e "${BOLD}${CYAN}[$WORKLOAD] Agent-Driven E2E Results${RESET}"
    echo -e "${BOLD}${CYAN}Step                         Duration    Result${RESET}"
    echo -e "${CYAN}──────────────────────────────────────────────────${RESET}"
    local total=0
    for k in "Pod ready" "Pre-checkpoint infer" "Checkpoint" "Placeholder ready" "Restore" "Post-restore infer"; do
        if [ -n "${TIMINGS[$k]:-}" ]; then
            local d=${TIMINGS[$k]}
            total=$(( total + d ))
            local mins=$(( d / 60 )) secs=$(( d % 60 ))
            local r=${RESULTS[$k]}
            local color=$GREEN; [ "$r" = "FAIL" ] && color=$RED
            printf "%-29s %dm %02ds    ${color}%s${RESET}\n" "$k" "$mins" "$secs" "$r"
        fi
    done
    echo -e "${CYAN}──────────────────────────────────────────────────${RESET}"
    local total_mins=$(( total / 60 )) total_secs=$(( total % 60 ))
    local final=PASS
    for r in "${RESULTS[@]}"; do [ "$r" = "FAIL" ] && final=FAIL; done
    local color=$GREEN; [ "$final" = "FAIL" ] && color=$RED
    printf "%-29s %dm %02ds    ${color}%s${RESET}\n" "Total" "$total_mins" "$total_secs" "$final"
    echo
    if [ "$final" = "PASS" ]; then
        log "TEST PASSED: $WORKLOAD"
    else
        err "TEST FAILED: $WORKLOAD"
    fi
}

PRE_INFER_PROMPT='Capital of France is'
TEST_TOKENS=15

POD_NAME="$WORKLOAD"
PLACEHOLDER_NAME="${WORKLOAD}-placeholder"

# Resolve model id from the workload yaml's --model / --model-path flag.
MODEL=$( { grep -oE -- '--model [^ ]+' "$WORKLOAD_YAML" || true; } | head -1 | awk '{print $2}')
if [ -z "$MODEL" ]; then
    MODEL=$( { grep -oE -- '--model-path [^ ]+' "$WORKLOAD_YAML" || true; } | head -1 | awk '{print $2}')
fi

# Resolve listen port from the workload yaml's --port flag (vllm/trtllm: 8000, sglang: 30000).
PORT=$( { grep -oE -- '--port [0-9]+' "$WORKLOAD_YAML" || true; } | head -1 | awk '{print $2}')
PORT="${PORT:-8000}"
log "Workload=$WORKLOAD  model=$MODEL"

# Resolve the workload's pinned node, find the agent on that node.
WORKLOAD_NODE=$(grep -E '^  nodeName:' "$WORKLOAD_YAML" | head -1 | awk '{print $2}')
AGENT_POD=$(kubectl -n "$NS" get pods -l app=nvsnap-agent --field-selector spec.nodeName="$WORKLOAD_NODE" -o jsonpath='{.items[0].metadata.name}')
if [ -z "$AGENT_POD" ]; then
    err "no nvsnap-agent pod on node $WORKLOAD_NODE"
    exit 1
fi
log "Agent pod=$AGENT_POD on node=$WORKLOAD_NODE"

# Cleanup any prior run.
kubectl -n "$NS" delete pod "$POD_NAME" "$PLACEHOLDER_NAME" --grace-period=5 --ignore-not-found >/dev/null 2>&1 || true

# Test-only: delete prior checkpoints for this pod identity. Production
# code does NOT auto-delete checkpoints (NVCF integration / retention
# controllers own that policy); this is a regression-test convenience
# to keep the agent's host volume bounded across iterative runs.
# Each TP=2 vllm-8b run produces ~116 GB; left untouched they compound.
PRIOR=$(kubectl -n "$NS" exec "$AGENT_POD" -- sh -c \
  "ls -1 /var/lib/nvsnap/checkpoints 2>/dev/null | grep -E '^${POD_NAME}__${NS}__' || true" 2>/dev/null | tr -d '\r')
if [ -n "$PRIOR" ]; then
    log "Cleaning up $(echo "$PRIOR" | wc -l) prior checkpoint dir(s) for ${POD_NAME}"
    for dir in $PRIOR; do
        kubectl -n "$NS" exec "$AGENT_POD" -- rm -rf "/var/lib/nvsnap/checkpoints/$dir" >/dev/null 2>&1 || true
    done
fi

# Step 1: deploy workload + wait ready.
log "Step 1: deploy workload"
step_begin
kubectl apply -f "$WORKLOAD_YAML" >/dev/null
if ! kubectl wait --for=condition=ready pod/"$POD_NAME" -n "$NS" --timeout=600s >/dev/null 2>&1; then
    err "workload pod did not become ready in 600s"
    step_end "Pod ready" FAIL
    print_summary
    exit 1
fi
step_end "Pod ready" OK
log "  ready"

# Step 2: pre-checkpoint /v1/models + /v1/completions.
log "Step 2: pre-checkpoint inference"
step_begin
POD_IP=$(kubectl -n "$NS" get pod "$POD_NAME" -o jsonpath='{.status.podIP}')
infer() {
    local target_pid="$1"
    kubectl -n "$NS" exec "$AGENT_POD" -- nsenter -n -t "$target_pid" -- \
        curl -sS -m 60 -X POST "http://127.0.0.1:${PORT}/v1/completions" \
            -H 'Content-Type: application/json' \
            -d '{"model":"'"$MODEL"'","prompt":"'"$PRE_INFER_PROMPT"'","max_tokens":'"$TEST_TOKENS"'}'
}
# For pre-checkpoint we still talk via the workload's pod IP from inside the agent.
PRE=$(kubectl -n "$NS" exec "$AGENT_POD" -- curl -sS --retry 6 --retry-delay 5 --retry-connrefused -m 30 "http://${POD_IP}:${PORT}/v1/models")
if ! echo "$PRE" | grep -q '"object":"list"'; then
    err "pre-checkpoint /v1/models failed: $PRE"
    step_end "Pre-checkpoint infer" FAIL
    print_summary
    exit 1
fi
PRE_INFER=$(kubectl -n "$NS" exec "$AGENT_POD" -- curl -sS --retry 3 --retry-delay 10 --retry-all-errors -m 120 -X POST "http://${POD_IP}:${PORT}/v1/completions" \
    -H 'Content-Type: application/json' \
    -d '{"model":"'"$MODEL"'","prompt":"'"$PRE_INFER_PROMPT"'","max_tokens":'"$TEST_TOKENS"'}')
if ! echo "$PRE_INFER" | grep -q '"text"'; then
    err "pre-checkpoint /v1/completions failed: $PRE_INFER"
    step_end "Pre-checkpoint infer" FAIL
    print_summary
    exit 1
fi
step_end "Pre-checkpoint infer" OK

# Step 3: checkpoint via agent /v1/checkpoint.
log "Step 3: checkpoint via agent /v1/checkpoint"
step_begin
CKPT_RESP=$(kubectl -n "$NS" exec "$AGENT_POD" -- curl -sS -m 600 -X POST \
    "http://localhost:8081/v1/checkpoint" \
    -H 'Content-Type: application/json' \
    -d '{"namespace":"'"$NS"'","podName":"'"$POD_NAME"'"}')
if ! echo "$CKPT_RESP" | grep -q '"checkpointId"'; then
    err "checkpoint failed: $CKPT_RESP"
    step_end "Checkpoint" FAIL
    print_summary
    exit 1
fi
CKPT_ID=$(echo "$CKPT_RESP" | sed -nE 's/.*"checkpointId":"([^"]+)".*/\1/p')
CKPT_SIZE=$(echo "$CKPT_RESP" | sed -nE 's/.*"checkpointSize":([0-9]+).*/\1/p')
step_end "Checkpoint" OK
log "  id=$CKPT_ID  size=$CKPT_SIZE"

# Step 3.5: delete the source pod now that the checkpoint is on disk.
# CRIU dump SIGKILLs the source process so the container is already in
# Error state; deleting the Pod resource frees the GPU device claim
# (kubelet releases nvidia.com/gpu back to the device plugin) and
# removes clutter from kubectl listings.
log "Step 3.5: delete source pod"
kubectl -n "$NS" delete pod "$POD_NAME" --grace-period=0 --force --ignore-not-found >/dev/null 2>&1 || true

# Step 4: deploy placeholder + wait ready.
log "Step 4: deploy placeholder"
step_begin
kubectl apply -f "$PLACEHOLDER_YAML" >/dev/null
if ! kubectl wait --for=condition=ready pod/"$PLACEHOLDER_NAME" -n "$NS" --timeout=300s >/dev/null 2>&1; then
    err "placeholder pod did not become ready in 300s"
    step_end "Placeholder ready" FAIL
    print_summary
    exit 1
fi
step_end "Placeholder ready" OK
log "  ready"

# Step 5: restore via agent /v1/restore.
log "Step 5: restore via agent /v1/restore"
step_begin
RESTORE_RESP=$(kubectl -n "$NS" exec "$AGENT_POD" -- sh -c "
rm -f /var/lib/nvsnap/checkpoints/${CKPT_ID}/restore.pid /var/lib/nvsnap/checkpoints/${CKPT_ID}/restore.log
curl -sS -m 600 -X POST 'http://localhost:8081/v1/restore' \
    -H 'Content-Type: application/json' \
    -d '{\"checkpointId\":\"${CKPT_ID}\",\"placeholderPodName\":\"${PLACEHOLDER_NAME}\",\"placeholderNamespace\":\"${NS}\"}'")
if ! echo "$RESTORE_RESP" | grep -q '"restoredPid"'; then
    err "restore failed: $RESTORE_RESP"
    step_end "Restore" FAIL
    print_summary
    exit 1
fi
RESTORED_PID=$(echo "$RESTORE_RESP" | sed -nE 's/.*"restoredPid":([0-9]+).*/\1/p')
step_end "Restore" OK
log "  restoredPid=$RESTORED_PID"

# Give the workload a moment to settle (libnvsnap_intercept reinit + wakeRestoredThreads).
sleep 5

# Step 6: post-restore inference via nsenter into the restored process's netns.
log "Step 6: post-restore inference"
step_begin
POST_MODELS=$(kubectl -n "$NS" exec "$AGENT_POD" -- nsenter -n -t "$RESTORED_PID" -- \
    curl -sS -m 60 "http://127.0.0.1:${PORT}/v1/models")
if ! echo "$POST_MODELS" | grep -q '"object":"list"'; then
    err "post-restore /v1/models failed: $POST_MODELS"
    step_end "Post-restore infer" FAIL
    print_summary
    exit 1
fi
POST_INFER=$(kubectl -n "$NS" exec "$AGENT_POD" -- nsenter -n -t "$RESTORED_PID" -- \
    curl -sS -m 120 -X POST "http://127.0.0.1:${PORT}/v1/completions" \
    -H 'Content-Type: application/json' \
    -d '{"model":"'"$MODEL"'","prompt":"'"$PRE_INFER_PROMPT"'","max_tokens":'"$TEST_TOKENS"'}')
if ! echo "$POST_INFER" | grep -q '"text"'; then
    err "post-restore /v1/completions failed: $POST_INFER"
    step_end "Post-restore infer" FAIL
    print_summary
    exit 1
fi
step_end "Post-restore infer" OK

print_summary
final=PASS
for r in "${RESULTS[@]}"; do [ "$r" = "FAIL" ] && final=FAIL; done
[ "$final" = "PASS" ]
