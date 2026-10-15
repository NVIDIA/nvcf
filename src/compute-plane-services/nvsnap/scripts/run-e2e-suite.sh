#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# run-e2e-suite.sh — sequential e2e runner with per-test checkpoint cleanup.
#
# Runs scripts/test-e2e.sh against each named workload, then wipes
# checkpoint + rootfs-cache state on every agent so the next test
# starts clean. Without this, repeated runs accumulate ~30GB+ per
# test on every GPU node and eventually fill the disk.
#
# Usage:
#   ./scripts/run-e2e-suite.sh                              # default suite
#   ./scripts/run-e2e-suite.sh vllm-small vllm-8b           # specific subset
#   KEEP_CHECKPOINTS=1 ./scripts/run-e2e-suite.sh ...       # skip cleanup
#
# Exit code: 0 if all tests pass; non-zero count of failed tests otherwise.
# Prints a structured summary table at the end.

set -uo pipefail

NAMESPACE="${NAMESPACE:-nvsnap-system}"
WORKLOADS=("$@")
if [ "${#WORKLOADS[@]}" -eq 0 ]; then
    # Default suite: single-GPU first (fast feedback), then multi-GPU.
    # vllm-small is intentionally first — fastest, covers the CRIU path.
    WORKLOADS=(vllm-small vllm-8b sglang-small sglang-8b trtllm-small vllm-70b)
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TEST_E2E="$SCRIPT_DIR/test-e2e.sh"
[ -x "$TEST_E2E" ] || { echo "ERROR: $TEST_E2E not found or not executable" >&2; exit 1; }

# ─── Helpers ───────────────────────────────────────────────────────────

step() { echo; echo "═══════════════════════════════════════════════════════════════"; echo "$*"; echo "═══════════════════════════════════════════════════════════════"; }

cleanup_workload_pods() {
    # Catch-all for demo workload pods that test-e2e.sh may leave behind
    # on failure. Safe to run when nothing matches.
    kubectl delete pod -l nvsnap.io/demo=true -n "$NAMESPACE" --wait=false --ignore-not-found >/dev/null 2>&1 || true
    # The restored-pod naming pattern from test-e2e.sh.
    kubectl get pods -n "$NAMESPACE" -o name 2>/dev/null \
        | grep -E "/(vllm|sglang|trtllm|nim)" \
        | xargs -r kubectl delete -n "$NAMESPACE" --wait=false --ignore-not-found >/dev/null 2>&1 || true
}

cleanup_checkpoint_storage() {
    # Wipe the agent's hostPath-mounted checkpoint + rootfs cache dirs on
    # every node. Done by exec'ing into each agent pod (the agent mounts
    # both directories from the host, so deleting inside the container
    # deletes on the host).
    local pods
    pods=$(kubectl get pods -n "$NAMESPACE" -l app=nvsnap-agent -o jsonpath='{.items[*].metadata.name}' 2>/dev/null)
    if [ -z "$pods" ]; then return; fi
    for pod in $pods; do
        # Don't exit the whole script on a single failed kubectl exec
        # (an agent might be transiently unhealthy mid-rollout).
        kubectl exec -n "$NAMESPACE" "$pod" -c agent -- sh -c '
            rm -rf /var/lib/nvsnap/checkpoints/* 2>/dev/null || true
            rm -rf /var/lib/nvsnap/cache/* 2>/dev/null || true
            rm -rf /var/lib/nvsnap/staging/* 2>/dev/null || true
        ' 2>/dev/null || true
    done
}

cleanup_blobstore() {
    # Wipe nvsnap-blobstore data. Blobstore is a single-replica Deployment;
    # the data dir is at /data inside the pod (PVC-backed). We rm the
    # contents (not the dir itself or the PVC).
    local pod
    pod=$(kubectl get pods -n "$NAMESPACE" -l app=nvsnap-blobstore -o jsonpath='{.items[0].metadata.name}' 2>/dev/null)
    if [ -z "$pod" ]; then return; fi
    kubectl exec -n "$NAMESPACE" "$pod" -- sh -c '
        find /data -mindepth 1 -maxdepth 1 -exec rm -rf {} + 2>/dev/null || true
    ' 2>/dev/null || true
}

full_cleanup() {
    if [ "${KEEP_CHECKPOINTS:-0}" = "1" ]; then
        echo "  (KEEP_CHECKPOINTS=1, skipping)"
        return
    fi
    echo "  cleaning up workload pods..."
    cleanup_workload_pods
    echo "  wiping checkpoint storage on $(kubectl get pods -n "$NAMESPACE" -l app=nvsnap-agent --no-headers 2>/dev/null | wc -l) agent(s)..."
    cleanup_checkpoint_storage
    echo "  wiping blobstore data..."
    cleanup_blobstore
}

# ─── Pre-flight ────────────────────────────────────────────────────────

step "Pre-flight"
echo "  workloads:  ${WORKLOADS[*]}"
echo "  namespace:  $NAMESPACE"
echo "  cluster:    $(kubectl config current-context 2>&1 | head -1)"
if ! kubectl get ds nvsnap-agent -n "$NAMESPACE" >/dev/null 2>&1; then
    echo "ERROR: nvsnap-agent DaemonSet not found in $NAMESPACE. Install nvsnap first:" >&2
    echo "  ./scripts/install-nvsnap.sh" >&2
    exit 1
fi

# Sanity: clean state before the suite.
step "Initial cleanup (in case prior runs left state)"
full_cleanup

# ─── Run the suite ─────────────────────────────────────────────────────

declare -a RESULTS
SUITE_START=$(date +%s)

for w in "${WORKLOADS[@]}"; do
    step "Running e2e: $w"
    start=$(date +%s)
    if "$TEST_E2E" "$w"; then
        end=$(date +%s)
        RESULTS+=("PASS  $w  $((end - start))s")
        echo
        echo "─── $w PASSED in $((end - start))s ───"
    else
        rc=$?
        end=$(date +%s)
        RESULTS+=("FAIL  $w  $((end - start))s  (exit $rc)")
        echo
        echo "─── $w FAILED (exit $rc) after $((end - start))s ───"
    fi

    step "Cleanup after $w"
    full_cleanup
done

SUITE_END=$(date +%s)

# ─── Summary ───────────────────────────────────────────────────────────

step "E2E suite summary"
echo
printf '%s\n' "${RESULTS[@]}"
echo
pass_count=$(printf '%s\n' "${RESULTS[@]}" | grep -c "^PASS" || true)
fail_count=$(printf '%s\n' "${RESULTS[@]}" | grep -c "^FAIL" || true)
echo "  Total: ${#RESULTS[@]}   Pass: $pass_count   Fail: $fail_count"
echo "  Suite elapsed: $((SUITE_END - SUITE_START))s"
echo

exit "$fail_count"
