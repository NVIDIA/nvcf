#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# bench-cross-node-restore.sh — measure pure cross-node EnsureLocal
# cascade wall-clock. Assumes a checkpoint already exists in the
# catalog (e.g. produced by `test-e2e.sh vllm-small`) on CAPTURE_NODE.
#
# For each receiver:
#   1. Wipe any stale local copy of the checkpoint (so EnsureLocal
#      can't short-circuit at the Tier-1 same-node check).
#   2. POST /v1/checkpoints/{id}/ensure-local to that node's agent.
#      The handler blocks until the cascade either completes or fails.
#   3. Time the POST. Scrape agent log for cascade phase events.
#
# Bench shape (sequential receivers, 3 GPU nodes):
#   - 1st receiver: only CAPTURE_NODE has the data
#     → Phase 2 (range chunking) with 1 peer source
#   - 2nd receiver: CAPTURE_NODE + 1st receiver have the data
#     → Phase 2 + Phase 3 (multi-source) with 2 peer sources
#
# Phase 1 (least-loaded peer selection) is not cleanly validated by
# this shape since we only have one capture source until the first
# receiver completes. Note as a known gap.
#
# Inputs:
#   $1 = CHECKPOINT_ID
#   $2 = CAPTURE_NODE node name (informational, for logging)
#   $3 = RECEIVERS (comma-separated node names)

set -euo pipefail

if [ $# -ne 3 ]; then
    echo "Usage: $0 <CHECKPOINT_ID> <CAPTURE_NODE_NAME> <r1,r2,...>"
    exit 1
fi
CHECKPOINT_ID="$1"
CAPTURE_NODE="$2"
IFS=',' read -ra RECEIVERS <<< "$3"

NS="nvsnap-system"

GREEN='\033[0;32m'; CYAN='\033[0;36m'; YELLOW='\033[1;33m'; RED='\033[0;31m'; NC='\033[0m'
log() { echo -e "${CYAN}[bench]${NC} $*"; }
ok()  { echo -e "${GREEN}[ok]${NC}   $*"; }
warn(){ echo -e "${YELLOW}[warn]${NC} $*"; }
err() { echo -e "${RED}[err]${NC}  $*"; }

agent_pod_on() {
    local node="$1"
    kubectl -n $NS get pods -l app=nvsnap-agent \
        --field-selector "spec.nodeName=$node" -o name 2>/dev/null | head -1
}

echo ""
log "Checkpoint:    $CHECKPOINT_ID"
log "Capture node:  $CAPTURE_NODE  (kept for context)"
log "Receivers:     ${RECEIVERS[*]}"
echo ""

declare -A RESULT_SECS

for r in "${RECEIVERS[@]}"; do
    short="${r##*-}"
    log "==== Receiver: $r ===="

    agent=$(agent_pod_on "$r")
    if [ -z "$agent" ]; then
        err "no nvsnap-agent pod on $r"
        continue
    fi
    log "Agent pod: $agent"

    # Step 1: wipe any stale local copy so Tier-1 cannot short-circuit.
    log "Wiping local checkpoint dir (if any) to force cold cascade…"
    kubectl -n $NS exec "$agent" -- rm -rf "/var/lib/nvsnap/checkpoints/$CHECKPOINT_ID" 2>&1 || true

    # Step 2: kick off a CPU pprof on the agent in the BACKGROUND so it
    # captures during the cascade. Profile lives inside the agent pod;
    # copied out after. Skip if BENCH_PROFILE != "1" to keep the
    # default run lightweight.
    if [ "${BENCH_PROFILE:-0}" = "1" ]; then
        log "Starting CPU pprof on agent (30s)…"
        kubectl -n $NS exec "$agent" -- sh -c "curl -sS 'http://localhost:8081/debug/pprof/profile?seconds=30' -o /tmp/cpu.pb && echo PROFILE_DONE" >/tmp/pprof-$short.log 2>&1 &
        PPROF_PID=$!
    fi

    # Step 3: POST ensure-local from inside the agent pod. The agent's
    # HTTP server binds on the host IP via hostNetwork; localhost:8081
    # is the simplest way to hit it from inside the same pod.
    log "Triggering EnsureLocal cascade…"
    t0=$(date +%s.%N)
    if ! kubectl -n $NS exec "$agent" -- curl -sS -X POST -m 600 \
        "http://localhost:8081/v1/checkpoints/$CHECKPOINT_ID/ensure-local" \
        -o /tmp/ensure-local-out.txt -w "HTTP %{http_code}\n" 2>&1 | tee /tmp/bench-$short.curl ; then
        err "curl failed on $r"
        continue
    fi
    t1=$(date +%s.%N)
    elapsed=$(awk -v a="$t0" -v b="$t1" 'BEGIN{printf "%.2f", b-a}')
    RESULT_SECS["$r"]=$elapsed

    # Wait for pprof to finish and pull the profile out.
    if [ "${BENCH_PROFILE:-0}" = "1" ] && [ -n "${PPROF_PID:-}" ]; then
        wait "$PPROF_PID" 2>/dev/null || true
        kubectl -n $NS cp "$agent:/tmp/cpu.pb" "/tmp/cpu-$short.pb" 2>/dev/null || warn "couldn't copy pprof out"
        if [ -s "/tmp/cpu-$short.pb" ]; then
            ok "CPU profile saved to /tmp/cpu-$short.pb (analyze with: go tool pprof /tmp/cpu-$short.pb)"
        fi
    fi

    # Step 3: scrape the receiver agent's log for cascade phase events.
    log "Cascade events on $r:"
    kubectl -n $NS logs "$agent" --tail=200 2>/dev/null | \
        grep -E "EnsureLocal:|cascade starting|peer fetch complete|FSStore|blob store fetch complete" | \
        grep "$CHECKPOINT_ID" | tail -20 || true

    # Step 4: verify the checkpoint actually landed.
    if kubectl -n $NS exec "$agent" -- test -s "/var/lib/nvsnap/checkpoints/$CHECKPOINT_ID/inventory.img" 2>/dev/null; then
        size=$(kubectl -n $NS exec "$agent" -- du -sh "/var/lib/nvsnap/checkpoints/$CHECKPOINT_ID" 2>/dev/null | awk '{print $1}')
        ok "Cascade complete on $r in ${elapsed}s — landed size=$size"
    else
        err "Cascade returned but inventory.img missing on $r"
    fi
    echo ""
done

echo ""
log "==== Summary ===="
log "Checkpoint $CHECKPOINT_ID (30 GiB) — pure EnsureLocal cascade wall-clock per receiver"
echo ""
printf "  %-60s %s\n" "Receiver" "Elapsed"
printf "  %-60s %s\n" "$(printf '%.0s-' {1..60})" "----------"
for r in "${RECEIVERS[@]}"; do
    e="${RESULT_SECS[$r]:-(failed)}"
    printf "  %-60s %s s\n" "$r" "$e"
done
echo ""
log "Baseline (pre-Phase-2): 31.9s for 30 GiB = 0.95 GB/s (TRANSPORT-ARCHITECTURE.md)"
log "Expected (Phase 2): chunks 8-way, projected ~5 GB/s under symmetric scaling"
log "Expected (Phase 3 on 2nd receiver): chunks distributed across 2 peers"
