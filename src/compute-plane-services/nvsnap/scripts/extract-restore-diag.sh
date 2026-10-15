#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Extract CRIU restore diagnostics for io_uring/epoll from a restored pod.
# Produces compact output — safe to paste into Claude or other LLMs.
#
# Usage: ./extract-restore-diag.sh [pod-name] [namespace]
#   Defaults: pod=sglang-small-restored, ns=nvsnap-system

set -euo pipefail

POD="${1:-sglang-small-restored}"
NS="${2:-nvsnap-system}"
CONTAINER="restore"
export KUBECONFIG="${KUBECONFIG:?KUBECONFIG must point at your cluster kubeconfig — export it before running this script}"

echo "=== CRIU Restore Diagnostics: $POD ==="

# Find checkpoint directory
CKPT_DIR=$(kubectl exec -n "$NS" "$POD" -c "$CONTAINER" -- \
    sh -c 'unset LD_PRELOAD; ls -d /checkpoints/*__* 2>/dev/null | head -1' 2>&1 | tail -1)

if [ -z "$CKPT_DIR" ] || [[ "$CKPT_DIR" == *"error"* ]]; then
    echo "ERROR: No checkpoint directory found in $POD"
    exit 1
fi
echo "Checkpoint: $(basename "$CKPT_DIR")"
echo ""

# Extract CRIU-format log lines for io_uring (filter out LD_PRELOAD noise)
echo "--- io_uring prepare & restore ---"
kubectl exec -n "$NS" "$POD" -c "$CONTAINER" -- \
    sh -c "unset LD_PRELOAD; grep -E 'Prepared io_uring|Restoring io_uring|Restored io_uring|Moved fd|n_epoll_refs|epoll registered|epoll_ctl.*epfd|deferred epoll|Deferred|io_uring fd' $CKPT_DIR/restore.log 2>/dev/null" 2>&1 | \
    grep "^(" | head -20

echo ""
echo "--- Restore result ---"
# Check if inference works
RESULT=$(kubectl exec -n "$NS" "$POD" -c "$CONTAINER" -- \
    env LD_PRELOAD= curl -s -m 10 -X POST http://localhost:30000/v1/completions \
    -H "Content-Type: application/json" \
    -d '{"model":"TinyLlama/TinyLlama-1.1B-Chat-v1.0","prompt":"Hello","max_tokens":3}' 2>/dev/null || echo "TIMEOUT")

if echo "$RESULT" | grep -q '"choices"'; then
    echo "POST /v1/completions: PASS"
    echo "$RESULT" | python3 -c "import sys,json; d=json.load(sys.stdin); print(f'  text: {d[\"choices\"][0][\"text\"]}')" 2>/dev/null || true
else
    echo "POST /v1/completions: FAIL"
    # Check if models endpoint works
    MODELS=$(kubectl exec -n "$NS" "$POD" -c "$CONTAINER" -- \
        env LD_PRELOAD= curl -s -m 5 http://localhost:30000/v1/models 2>/dev/null || echo "TIMEOUT")
    if echo "$MODELS" | grep -q "TinyLlama"; then
        echo "  GET /v1/models: OK (server running but inference broken)"
    else
        echo "  GET /v1/models: FAIL (server not responding)"
    fi
fi
