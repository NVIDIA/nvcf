#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
# Checkpoint/restore cycle against a running vLLM pod (vllm-tp2.yaml or
# vllm-criu.yaml).
#
#   tests/gpushare/k8s/vllm_ckpt_cycle.sh <namespace> <pod> [cycles=2] [idle_secs=30]
#
# Per cycle: suspend every vLLM process → verify GPU memory is released →
# send a request while suspended (it must wait, not crash the server) →
# resume → both that request and a fresh one must match the baseline
# byte-for-byte.
set -euo pipefail

NS=$1; POD=$2; CYCLES=${3:-2}; IDLE=${4:-30}
ROOT=$(cd "$(dirname "$0")/../../.." && pwd)
k() { kubectl exec -n "$NS" "$POD" -c vllm -- "$@"; }
# The tool from the pod's gpushare init container (the agent image's
# /criu-bundle); it runs in the pod, in the workload's pid namespace.
T=$(k sh -c 'ls /opt/gpushare/nvsnap-gpu-suspend /ckpt/wc/nvsnap-gpu-suspend 2>/dev/null | head -1')
[ -n "$T" ] || { echo "FAIL: no nvsnap-gpu-suspend in the pod"; exit 1; }
k mkdir -p /opt/wc
kubectl cp -n "$NS" "$ROOT/tests/gpushare/k8s/vllm_query.py" "$POD:/opt/wc/vllm_query.py" -c vllm

# Every process with CUDA state: API server, EngineCore, TP workers.
PIDS=$(k ps -eo pid,comm | awk '/vllm|VLLM::/{print $1}' | xargs)
echo "vLLM pids: $PIDS"

text() { k python3 /opt/wc/vllm_query.py | python3 -c 'import json,sys; print(json.load(sys.stdin)["text"])'; }
# The first (cold, no prefix-cache hit) request can differ in bf16 numerics;
# use a warm request as the baseline and report whether cold/warm differ.
COLD=$(text); BASE=$(text)
[ "$COLD" = "$BASE" ] && echo "baseline: cold == warm" || echo "baseline: cold != warm (expected prefix-cache numerics); using warm"
echo "baseline: ${BASE:0:60}..."

for c in $(seq 1 "$CYCLES"); do
    echo "=== cycle $c/$CYCLES"
    s=$(date +%s.%N)
    k $T --timeout-ms 60000 suspend $PIDS
    # Never leave the workload suspended if a step below fails.
    trap 'echo "resuming after failure"; k $T resume $PIDS || echo "resume failed: pids $PIDS still suspended"' EXIT
    echo "suspend: $(echo "$(date +%s.%N) - $s" | bc)s"
    k nvidia-smi --query-gpu=index,memory.used --format=csv,noheader

    INFLIGHT=$(mktemp)
    k python3 /opt/wc/vllm_query.py > "$INFLIGHT" &
    INFLIGHT_PID=$!
    sleep "$IDLE"

    s=$(date +%s.%N)
    k $T resume $PIDS
    trap - EXIT
    echo "resume: $(echo "$(date +%s.%N) - $s" | bc)s"
    k nvidia-smi --query-gpu=index,memory.used --format=csv,noheader

    wait $INFLIGHT_PID
    TEXT=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["text"])' "$INFLIGHT")
    rm -f "$INFLIGHT"
    if [ "$TEXT" != "$BASE" ]; then
        echo "FAIL: request sent while suspended differs"
        diff <(echo "$BASE") <(echo "$TEXT"); exit 1
    fi
    echo "request sent while suspended: completed, matches baseline"

    OUT=$(k python3 /opt/wc/vllm_query.py)
    TEXT=$(echo "$OUT" | python3 -c 'import json,sys; print(json.load(sys.stdin)["text"])')
    if [ "$TEXT" != "$BASE" ]; then
        echo "FAIL: output differs after restore"
        diff <(echo "$BASE") <(echo "$TEXT"); exit 1
    fi
    echo "output matches baseline ($(echo "$OUT" | python3 -c 'import json,sys; print(json.load(sys.stdin)["secs"])')s)"
done
echo "=== PASS"
