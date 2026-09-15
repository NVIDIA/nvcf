#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# bench-rootfs-real.sh — measure rootfs cascade fetch via the
# PRODUCTION code path: apply restore pods, let admission webhook
# trigger EnsureCaptureLocal on each target node's agent.
#
# Polls /var/lib/nvsnap/cache/<hash> size to detect cascade completion.
#
# Inputs are hardcoded for the 2026-05-19 vllm-70b run.

set -uo pipefail

KUBECONFIG="${KUBECONFIG:?KUBECONFIG must point at your cluster kubeconfig — export it before running this script}"
export KUBECONFIG
NS=nvsnap-system
HASH=aca2bd6356d8291e955bf99770e1ad0ee34561f4d7893b826a59011e59ce2cc9
EXPECTED_BYTES=142093237387
TMPL=deploy/k8s/workloads/vllm-70b-restore.yaml
RECEIVERS=(1sd6 hnst)

agent_pod() {
    kubectl -n $NS get pods -l app=nvsnap-agent \
        --field-selector "spec.nodeName=gke-example-gpu-cluster-gpu-b6f5872c-$1" \
        -o name 2>/dev/null | head -1 | sed 's|pod/||'
}

cache_bytes() {
    local agent=$1
    kubectl -n $NS exec "$agent" -- du -sB1 "/var/lib/nvsnap/cache/$HASH" 2>/dev/null | awk '{print $1}'
}

echo "Step 1: clearing cache on receivers"
for NODE in "${RECEIVERS[@]}"; do
    POD=$(agent_pod "$NODE")
    kubectl -n $NS exec "$POD" -- rm -rf "/var/lib/nvsnap/cache/$HASH" >/dev/null 2>&1
    echo "  $NODE: cleared (agent=$POD)"
done

echo ""
echo "Step 2: deleting any prior restore pods"
for NODE in "${RECEIVERS[@]}"; do
    kubectl -n $NS delete pod "vllm-70b-restored-$NODE" --wait=false 2>/dev/null || true
done
sleep 3

echo ""
echo "Step 3: rendering restore manifests"
for NODE in "${RECEIVERS[@]}"; do
    OUT="/tmp/vllm-70b-restore-$NODE.yaml"
    FQDN="gke-example-gpu-cluster-gpu-b6f5872c-$NODE"
    sed -e "s|__CAPTURE_HASH__|$HASH|g" \
        -e "s|name: vllm-70b-restored\$|name: vllm-70b-restored-$NODE|" \
        "$TMPL" > "$OUT"
    if ! grep -q "nodeName:" "$OUT"; then
        sed -i "/^spec:\$/a\\  nodeName: $FQDN" "$OUT"
    fi
    # Verify substitution & nodeName landed
    if ! grep -q "nodeName: $FQDN" "$OUT"; then
        echo "  WARN: nodeName not set in $OUT — adding manually"
        # Force inject under spec:
        awk -v fqdn="$FQDN" '
            /^spec:/ && !done {print; print "  nodeName: " fqdn; done=1; next}
            {print}
        ' "$OUT" > "$OUT.tmp" && mv "$OUT.tmp" "$OUT"
    fi
    echo "  $NODE: $OUT (nodeName: $(grep '^  nodeName:' "$OUT"))"
done

echo ""
echo "Step 4: applying both restore pods (parallel)"
T0=$(date +%s)
for NODE in "${RECEIVERS[@]}"; do
    kubectl apply -f "/tmp/vllm-70b-restore-$NODE.yaml" &
done
wait
T_APPLIED=$(date +%s)
echo "  applied in $((T_APPLIED - T0))s"

echo ""
echo "Step 5: polling caches for completion (target ${EXPECTED_BYTES} bytes ≈ 132 GiB)"
declare -A DONE_AT
for i in $(seq 1 180); do  # poll up to 30 min
    sleep 10
    NOW=$(date +%s)
    ELAPSED=$((NOW - T0))
    LINE="T+${ELAPSED}s"
    for NODE in "${RECEIVERS[@]}"; do
        if [ -n "${DONE_AT[$NODE]:-}" ]; then
            LINE="$LINE  $NODE=DONE@${DONE_AT[$NODE]}s"
            continue
        fi
        POD=$(agent_pod "$NODE")
        B=$(cache_bytes "$POD")
        [ -z "$B" ] && B=0
        GIB=$(awk -v b="$B" 'BEGIN{printf "%.1f", b/(1024*1024*1024)}')
        LINE="$LINE  $NODE=${GIB}G"
        if [ "$B" -ge "$EXPECTED_BYTES" ]; then
            DONE_AT[$NODE]=$ELAPSED
            LINE="$LINE[done]"
        fi
    done
    echo "$LINE"
    if [ -n "${DONE_AT[1sd6]:-}" ] && [ -n "${DONE_AT[hnst]:-}" ]; then
        break
    fi
done

echo ""
echo "===== SUMMARY ====="
for NODE in "${RECEIVERS[@]}"; do
    T="${DONE_AT[$NODE]:-incomplete}"
    if [ "$T" != "incomplete" ]; then
        TPUT=$(awk -v t="$T" -v g=132.4 'BEGIN{printf "%.2f", g/t}')
        echo "  $NODE: ${T}s — ${TPUT} GB/s for 132 GiB"
    else
        echo "  $NODE: did not complete within 30 min"
    fi
done

echo ""
echo "===== AGENT LOG EVENTS (EnsureCaptureLocal) ====="
for NODE in "${RECEIVERS[@]}"; do
    POD=$(agent_pod "$NODE")
    echo "--- $NODE ($POD) ---"
    kubectl -n $NS logs "$POD" --since=30m 2>&1 | grep -iE "EnsureCaptureLocal|capture.*fetch|restore-from" | tail -8
done
