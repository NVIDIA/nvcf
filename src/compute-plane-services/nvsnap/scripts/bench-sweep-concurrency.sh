#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Sweep NVSNAP_PEER_FETCH_CONCURRENCY values, bench cascade for each.
#
# Reuses an existing checkpoint (created via test-e2e.sh) and forces
# cold reads on the receiver between runs.
#
# Usage: ./bench-sweep-concurrency.sh <CHECKPOINT_ID> <RECEIVER_NODE>
#
# Sender is whichever peer the catalog picks first (least-loaded sort).

set -uo pipefail

if [ $# -ne 2 ]; then
    echo "Usage: $0 <CHECKPOINT_ID> <RECEIVER_NODE>"
    exit 1
fi
CHECKPOINT_ID="$1"
RECEIVER_NODE="$2"
NS="nvsnap-system"
VALUES=(1 2 4 8 16 32)

GREEN='\033[0;32m'; CYAN='\033[0;36m'; YELLOW='\033[1;33m'; NC='\033[0m'

declare -A RESULT_SEC

for conc in "${VALUES[@]}"; do
    echo ""
    echo -e "${CYAN}[sweep] === NVSNAP_PEER_FETCH_CONCURRENCY=$conc ===${NC}"

    # Bump env var on DaemonSet; that triggers a rolling restart.
    kubectl -n $NS set env ds/nvsnap-agent NVSNAP_PEER_FETCH_CONCURRENCY=$conc >/dev/null
    if ! kubectl -n $NS rollout status ds/nvsnap-agent --timeout=120s >/dev/null 2>&1; then
        echo "[sweep] rollout for conc=$conc failed; skipping"
        continue
    fi

    # Find the receiver's agent pod after rollout.
    agent=$(kubectl -n $NS get pods -l app=nvsnap-agent \
        --field-selector "spec.nodeName=$RECEIVER_NODE" -o name 2>/dev/null | head -1)
    if [ -z "$agent" ]; then
        echo "[sweep] no agent pod on $RECEIVER_NODE after rollout"
        continue
    fi

    # Wipe local copy so cascade is cold.
    kubectl -n $NS exec "$agent" -- rm -rf "/var/lib/nvsnap/checkpoints/$CHECKPOINT_ID" >/dev/null 2>&1 || true

    # Trigger cascade, time it.
    t0=$(date +%s.%N)
    kubectl -n $NS exec "$agent" -- curl -sS -X POST -m 600 \
        -o /dev/null -w "HTTP %{http_code}\n" \
        "http://localhost:8081/v1/checkpoints/$CHECKPOINT_ID/ensure-local" 2>&1
    t1=$(date +%s.%N)
    elapsed=$(awk -v a="$t0" -v b="$t1" 'BEGIN{printf "%.2f", b-a}')
    RESULT_SEC[$conc]=$elapsed

    # Verify landed.
    if kubectl -n $NS exec "$agent" -- test -s "/var/lib/nvsnap/checkpoints/$CHECKPOINT_ID/inventory.img" 2>/dev/null; then
        echo -e "${GREEN}[sweep] conc=$conc → ${elapsed}s${NC}"
    else
        echo -e "${YELLOW}[sweep] conc=$conc → ${elapsed}s (NOT LANDED)${NC}"
    fi
done

echo ""
echo -e "${CYAN}=== Summary ===${NC}"
printf "  %-8s %s\n" "conc" "elapsed (30 GiB)"
printf "  %-8s %s\n" "----" "----------------"
for conc in "${VALUES[@]}"; do
    e="${RESULT_SEC[$conc]:-?}"
    tput=$(awk -v e="$e" 'BEGIN{ if (e ~ /^[0-9.]+$/ && e > 0) printf "%.2f GB/s", 30/e; else print "?" }')
    printf "  %-8s %-10s %s\n" "$conc" "${e}s" "$tput"
done
