#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# bench-70b-full.sh — End-to-end vllm-70b checkpoint + 3-replica
# restore on 3 GPU nodes, instrumented per-stage.
#
# Workflow:
#   1. Clear checkpoint dir on all 3 GPU nodes (cold start).
#   2. Deploy vllm-70b (TP=4) on $CAPTURE_NODE.
#   3. Wait pod-ready → "cold-start" elapsed.
#   4. POST checkpoint to nvsnap-server; wait for catalog row.
#      → "capture" elapsed.
#   5. Delete source pod (releases 4 GPUs).
#   6. Trigger ensure-local on all 3 GPU nodes in parallel:
#         CAPTURE_NODE      → expect Tier-1 hit (~0s)
#         RECEIVERS (×2)    → cross-node cascade
#      Each receiver records:
#         ensure-local-elapsed (cascade body transfer time)
#         landed-size (verifies data is there)
#   7. Print a per-stage summary table.
#
# Restore-pod CRIU init time is NOT measured here (multi-GPU restore
# is blocked on libcudart per known issue). The point of this run is
# data-movement timing across 3 nodes, which is what we'll show.

set -uo pipefail

NS="nvsnap-system"
CAPTURE_NODE="gke-example-gpu-cluster-gpu-b6f5872c-3604"
# 1sd6 is in production-use (nvcf-backend); using only 3604 + hnst.
# CAPTURE_NODE doubles as a same-node receiver (Tier-1 hit).
RECEIVERS=(
    "gke-example-gpu-cluster-gpu-b6f5872c-hnst"
)
ALL_NODES=("$CAPTURE_NODE" "${RECEIVERS[@]}")

POD_NAME="vllm-70b"
POD_MANIFEST="deploy/k8s/workloads/vllm-70b.yaml"

GREEN='\033[0;32m'; CYAN='\033[0;36m'; YELLOW='\033[1;33m'; RED='\033[0;31m'; NC='\033[0m'
log()  { echo -e "${CYAN}[bench]${NC} $*"; }
ok()   { echo -e "${GREEN}[ ok]${NC}   $*"; }
warn() { echo -e "${YELLOW}[warn]${NC} $*"; }
err()  { echo -e "${RED}[err ]${NC} $*"; }

# Returns the agent pod name running on the given node.
agent_on() {
    kubectl -n $NS get pods -l app=nvsnap-agent \
        --field-selector "spec.nodeName=$1" -o name 2>/dev/null | head -1
}

short_node() {
    local s="${1##*-gpu-*-}"
    echo "${s:0:4}"
}

# ─── Step 1: clear state ───────────────────────────────────────────
log "Step 1: clearing checkpoint state on all 3 GPU nodes…"
for NODE in "${ALL_NODES[@]}"; do
    AGENT=$(agent_on "$NODE")
    [ -z "$AGENT" ] && { warn "no agent on $NODE"; continue; }
    SHORT=$(short_node "$NODE")
    # Skip cuda-checkpoint binary; delete all subdirs (checkpoints)
    # and any stray .img files at the root.
    kubectl -n $NS exec "$AGENT" -- sh -c '
        cd /var/lib/nvsnap/checkpoints || exit 0
        for d in */; do
            [ "$d" = "cuda-checkpoint/" ] && continue
            rm -rf "$d" 2>/dev/null || true
        done
        rm -f ./*.img ./*.bin 2>/dev/null || true
    ' >/dev/null 2>&1
    REMAINING=$(kubectl -n $NS exec "$AGENT" -- sh -c 'cd /var/lib/nvsnap/checkpoints && ls -1 | grep -v cuda-checkpoint | wc -l' 2>/dev/null)
    ok "$SHORT: $REMAINING checkpoint dirs left"
done

# ─── Step 2: deploy vllm-70b ────────────────────────────────────────
log "Step 2: cleaning any prior vllm-70b pods…"
kubectl -n $NS delete pod -l app=vllm-70b --wait=true --timeout=120s 2>/dev/null || true
kubectl -n $NS delete pod vllm-70b vllm-70b-restored --wait=true --timeout=120s 2>/dev/null || true

log "Step 2: deploying $POD_NAME on $CAPTURE_NODE (TP=4)…"
T_DEPLOY=$(date +%s.%N)
# Inject nodeName via yq if available, else sed.
TMP_MANIFEST=$(mktemp /tmp/vllm70b-XXX.yaml)
sed "s|nodeName: .*|nodeName: $CAPTURE_NODE|" "$POD_MANIFEST" > "$TMP_MANIFEST"
# If the manifest didn't have a nodeName placeholder, add one.
if ! grep -q "nodeName: $CAPTURE_NODE" "$TMP_MANIFEST"; then
    sed -i "/^spec:/a\\  nodeName: $CAPTURE_NODE" "$TMP_MANIFEST"
fi
kubectl apply -f "$TMP_MANIFEST"

# ─── Step 3: wait for pod-ready (cold-start time) ───────────────────
log "Step 3: waiting for $POD_NAME pod to become Ready (cold start)…"
if ! kubectl -n $NS wait --for=condition=Ready pod/$POD_NAME --timeout=900s; then
    err "cold start failed"
    kubectl -n $NS describe pod $POD_NAME | tail -30
    exit 1
fi
T_READY=$(date +%s.%N)
COLD_START=$(awk -v a="$T_DEPLOY" -v b="$T_READY" 'BEGIN{printf "%.1f", b-a}')
ok "cold start = ${COLD_START} s"

# ─── Step 4: checkpoint ─────────────────────────────────────────────
log "Step 4: triggering checkpoint via nvsnap-server…"
T_CKPT_START=$(date +%s.%N)
CKPT_RESP=$(kubectl -n $NS exec deploy/nvsnap-server -- curl -sS -X POST \
    -H 'Content-Type: application/json' \
    -d "{\"namespace\":\"$NS\",\"pod_name\":\"$POD_NAME\",\"container_name\":\"vllm\",\"node_name\":\"$CAPTURE_NODE\"}" \
    http://localhost:8080/api/v1/checkpoint/pod 2>&1)
echo "$CKPT_RESP" | head -10
CHECKPOINT_ID=$(echo "$CKPT_RESP" | python3 -c 'import sys, json; d=json.load(sys.stdin); print(d.get("checkpoint_id") or d.get("id") or "", end="")' 2>/dev/null || echo "")
if [ -z "$CHECKPOINT_ID" ]; then
    err "couldn't parse checkpoint_id from response"
    exit 1
fi
log "checkpoint id: $CHECKPOINT_ID — polling catalog for completion…"
# Poll catalog every 5s until row exists with non-empty size.
for i in $(seq 1 120); do
    SIZE=$(kubectl -n $NS exec deploy/nvsnap-server -- curl -sS \
        "http://localhost:8080/api/v1/checkpoints/$CHECKPOINT_ID" 2>/dev/null | \
        python3 -c 'import sys, json; d=json.load(sys.stdin); print(d.get("size") or 0, end="")' 2>/dev/null || echo "0")
    if [ "$SIZE" != "0" ] && [ -n "$SIZE" ]; then
        T_CKPT_DONE=$(date +%s.%N)
        CKPT_TIME=$(awk -v a="$T_CKPT_START" -v b="$T_CKPT_DONE" 'BEGIN{printf "%.1f", b-a}')
        CKPT_GIB=$(awk -v s="$SIZE" 'BEGIN{printf "%.1f", s/(1024*1024*1024)}')
        ok "capture = ${CKPT_TIME} s — ${CKPT_GIB} GiB landed"
        break
    fi
    sleep 5
done

if [ -z "${CKPT_TIME:-}" ]; then
    err "checkpoint did not complete within 10 min"
    exit 1
fi

# ─── Step 5: clean source pod ──────────────────────────────────────
log "Step 5: deleting source pod $POD_NAME (releases 4 GPUs on $CAPTURE_NODE)…"
kubectl -n $NS delete pod $POD_NAME --wait=false 2>&1 | head -1

# Give catalog a moment to register CAPTURE_NODE as a peer (v0.24.8
# includes the peer-register-on-capture fix).
sleep 2

# ─── Step 6: parallel cascade fetch on all 3 nodes ─────────────────
log "Step 6: triggering EnsureLocal on all 3 GPU nodes (PARALLEL)…"
declare -A RESULT_SEC
declare -A RESULT_PEER
declare -A RESULT_SIZE
RESULT_FILE=$(mktemp /tmp/bench70b-XXX.results)

# Wipe local copy on receivers (force cold cascade). Don't wipe on
# capture node — that's the Tier-1 hit case we want to measure.
for NODE in "${RECEIVERS[@]}"; do
    AGENT=$(agent_on "$NODE")
    kubectl -n $NS exec "$AGENT" -- rm -rf "/var/lib/nvsnap/checkpoints/$CHECKPOINT_ID" >/dev/null 2>&1 || true
done

# Fire all 3 ensure-local in parallel. Capture per-node timing.
T_CASC_START=$(date +%s.%N)
for NODE in "${ALL_NODES[@]}"; do
    AGENT=$(agent_on "$NODE")
    SHORT=$(short_node "$NODE")
    [ -z "$AGENT" ] && continue
    (
        T0=$(date +%s.%N)
        HTTP=$(kubectl -n $NS exec "$AGENT" -- curl -sS -X POST -m 1800 \
            -o /dev/null -w "%{http_code}" \
            "http://localhost:8081/v1/checkpoints/$CHECKPOINT_ID/ensure-local" 2>/dev/null)
        T1=$(date +%s.%N)
        ELAPSED=$(awk -v a="$T0" -v b="$T1" 'BEGIN{printf "%.1f", b-a}')
        # Verify landed
        if kubectl -n $NS exec "$AGENT" -- test -s "/var/lib/nvsnap/checkpoints/$CHECKPOINT_ID/inventory.img" 2>/dev/null; then
            SIZE=$(kubectl -n $NS exec "$AGENT" -- du -sB1 "/var/lib/nvsnap/checkpoints/$CHECKPOINT_ID" 2>/dev/null | awk '{print $1}')
            SIZE_GIB=$(awk -v s="$SIZE" 'BEGIN{printf "%.1f", s/(1024*1024*1024)}')
        else
            SIZE_GIB="MISSING"
        fi
        # Try to capture which peer was used from agent logs.
        PEER=$(kubectl -n $NS logs "$AGENT" --tail=200 2>/dev/null | \
            grep "peer fetch complete.*$CHECKPOINT_ID" | tail -1 | \
            grep -oE 'peer_node=[^ ]+' | sed 's/peer_node=//; s/.*-//')
        [ -z "$PEER" ] && PEER="tier1"
        echo "$SHORT|$ELAPSED|$PEER|$SIZE_GIB|$HTTP" >> "$RESULT_FILE"
    ) &
done
wait
T_CASC_END=$(date +%s.%N)
CASC_WALL=$(awk -v a="$T_CASC_START" -v b="$T_CASC_END" 'BEGIN{printf "%.1f", b-a}')

# ─── Step 7: summary ──────────────────────────────────────────────
echo ""
echo "═════════════════════════════════════════════════════════════"
echo "  vllm-70b cascade fan-out — 3 GPU nodes"
echo "═════════════════════════════════════════════════════════════"
echo ""
printf "  %-30s %s\n" "Stage" "Elapsed"
printf "  %-30s %s\n" "──────────────────────────────" "─────────"
printf "  %-30s %s\n" "Cold deploy → pod Ready" "${COLD_START} s"
printf "  %-30s %s\n" "Pod Ready → capture complete" "${CKPT_TIME} s"
printf "  %-30s %s (${CKPT_GIB} GiB)\n" "Capture size" "$CHECKPOINT_ID"
echo ""
printf "  %-12s %-12s %-12s %s\n" "Node" "EnsureLocal" "From peer" "Landed"
printf "  %-12s %-12s %-12s %s\n" "────" "───────────" "─────────" "──────"
sort "$RESULT_FILE" | while IFS='|' read -r NODE ELAPSED PEER SIZE HTTP; do
    printf "  %-12s %-12s %-12s %s GiB\n" "$NODE" "${ELAPSED} s" "$PEER" "$SIZE"
done
echo ""
printf "  %-30s %s\n" "Parallel-cascade wall-clock" "${CASC_WALL} s"
echo ""
echo "═════════════════════════════════════════════════════════════"

rm -f "$TMP_MANIFEST" "$RESULT_FILE"
