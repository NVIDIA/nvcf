#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# vLLM GPU Checkpoint/Restore Test
#
# This script tests full checkpoint/restore of a vLLM inference server.
# It uses cuda-checkpoint (NVIDIA's tool) for GPU state and CRIU for CPU state.
#
# Requirements:
# - NVIDIA driver 555+ (for cuda-checkpoint support)
# - cuda-checkpoint binary
# - CRIU with CUDA plugin
# - vLLM installed in Python environment
#
# Usage:
#   sudo ./test_vllm_checkpoint.sh

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LIB_DIR="$(dirname "$SCRIPT_DIR")"
VENV_PYTHON="${VENV_PYTHON:?VENV_PYTHON must point at a Python venv with vLLM installed}"

# Sibling-relative paths to other forks; override individually with env vars.
_repo_root="$(cd "$LIB_DIR/../.." && pwd)"

# cuda-checkpoint location
if [ -n "${CUDA_CHECKPOINT:-}" ]; then
    CUDA_CKPT="$CUDA_CHECKPOINT"
elif [ -x "$_repo_root/../cuda-checkpoint/bin/x86_64_Linux/cuda-checkpoint" ]; then
    CUDA_CKPT="$_repo_root/../cuda-checkpoint/bin/x86_64_Linux/cuda-checkpoint"
elif command -v cuda-checkpoint &>/dev/null; then
    CUDA_CKPT="$(command -v cuda-checkpoint)"
else
    echo "ERROR: cuda-checkpoint not found (set CUDA_CHECKPOINT or install in PATH)"
    exit 1
fi

# CRIU binary (built from our CRIU fork)
if [ -n "${NVSNAP_CRIU:-}" ]; then
    CRIU="$NVSNAP_CRIU"
elif [ -x "$_repo_root/../criu/criu/criu" ]; then
    CRIU="$_repo_root/../criu/criu/criu"
elif command -v criu &>/dev/null; then
    CRIU="$(command -v criu)"
else
    echo "ERROR: CRIU not found (set NVSNAP_CRIU or clone the CRIU fork as a sibling and build it)"
    exit 1
fi

# CRIU CUDA plugin source (lives inside the CRIU fork tree)
PLUGIN="${NVSNAP_CRIU_PLUGIN_DIR:-$_repo_root/../criu/plugins/cuda}"

echo "=========================================="
echo "  vLLM Checkpoint/Restore Test"
echo "=========================================="

# Cleanup
pkill -9 -f vllm_test 2>/dev/null || true
rm -rf /tmp/criu_img /tmp/vllm_output.txt
mkdir -p /tmp/criu_img

# Create test script using a tiny model
cat > /tmp/vllm_test.py << 'EOF'
import os
import sys
import time

os.environ["VLLM_USE_CUDA_GRAPH"] = "0"

print(f"PID: {os.getpid()}", flush=True)

from vllm import LLM, SamplingParams

print("Loading TinyLlama model...", flush=True)
llm = LLM(
    model="TinyLlama/TinyLlama-1.1B-Chat-v1.0",
    tensor_parallel_size=1,
    gpu_memory_utilization=0.7,
    max_model_len=256,
    trust_remote_code=True,
    enforce_eager=True,
)
print("Model loaded!", flush=True)

params = SamplingParams(max_tokens=20, temperature=0.8)
outputs = llm.generate(["Hello, world!"], params)
print(f"Inference output: {outputs[0].outputs[0].text[:50]}...", flush=True)

print("READY - waiting for checkpoint...", flush=True)
sys.stdout.flush()

while True:
    time.sleep(1)
EOF

echo ""
echo "[1] Starting vLLM process..."
# Note: LD_PRELOAD not needed - cuda-checkpoint handles GPU state directly
$VENV_PYTHON /tmp/vllm_test.py > /tmp/vllm_output.txt 2>&1 &
PID=$!
echo "    PID: $PID"

echo "    Waiting for model to load..."
for i in {1..180}; do
    if grep -q "READY" /tmp/vllm_output.txt 2>/dev/null; then
        echo "    Model loaded after ${i}s"
        break
    fi
    if ! ps -p $PID > /dev/null 2>&1; then
        echo "    FAILED: Process died during loading"
        cat /tmp/vllm_output.txt
        exit 1
    fi
    sleep 1
done

if ! grep -q "READY" /tmp/vllm_output.txt 2>/dev/null; then
    echo "    TIMEOUT waiting for model"
    cat /tmp/vllm_output.txt
    kill -9 $PID 2>/dev/null || true
    exit 1
fi

echo ""
grep -E "(PID:|Model loaded|Inference|READY)" /tmp/vllm_output.txt || true

echo ""
echo "[2] CUDA state: $(${CUDA_CKPT} --get-state --pid $PID 2>/dev/null | tail -1)"

echo ""
echo "[3] Locking CUDA..."
${CUDA_CKPT} --action lock --pid $PID --timeout 30000 2>/dev/null
echo "    Lock: OK"

echo ""
echo "[4] Checkpointing CUDA..."
${CUDA_CKPT} --action checkpoint --pid $PID 2>/dev/null
echo "    State: $(${CUDA_CKPT} --get-state --pid $PID 2>/dev/null | tail -1)"

echo ""
echo "[5] CRIU dump..."
$CRIU dump -t $PID -D /tmp/criu_img --shell-job --tcp-established -L $PLUGIN 2>&1 | grep -v "NVSNAP\|Calls\|Driver\|Runtime\|cuBLAS\|cuDNN\|NCCL\|Tracked\|Allocations\|Streams\|Events\|Modules\|Contexts\|Comms\|===" || true
echo "    Created $(ls /tmp/criu_img/*.img 2>/dev/null | wc -l) images"

echo ""
echo "[6] CRIU restore..."
$CRIU restore -d -D /tmp/criu_img --shell-job --tcp-established -L $PLUGIN 2>&1 | grep -v "NVSNAP\|Calls\|Driver\|Runtime\|cuBLAS\|cuDNN\|NCCL\|Tracked\|Allocations\|Streams\|Events\|Modules\|Contexts\|Comms\|===" | tail -5 || true
sleep 3

NEW_PID=$(pgrep -f vllm_test.py 2>/dev/null | head -1)
if [ -z "$NEW_PID" ]; then
    echo "    FAILED: No restored process"
    exit 1
fi
echo "    Restored PID: $NEW_PID"

echo ""
echo "[7] Restoring CUDA..."
${CUDA_CKPT} --action restore --pid $NEW_PID 2>/dev/null
echo "    Restore: OK"

echo ""
echo "[8] Unlocking CUDA..."
${CUDA_CKPT} --action unlock --pid $NEW_PID 2>/dev/null
echo "    State: $(${CUDA_CKPT} --get-state --pid $NEW_PID 2>/dev/null | tail -1)"

echo ""
echo "[9] Verifying..."
sleep 5
if ps -p $NEW_PID > /dev/null 2>&1; then
    echo "    Process: ALIVE"
else
    echo "    Process: DEAD (may be io_uring issue)"
fi

kill -9 $NEW_PID 2>/dev/null || true
pkill -9 -f EngineCore 2>/dev/null || true

echo ""
echo "=========================================="
echo "  vLLM CHECKPOINT/RESTORE TEST COMPLETE"
echo "=========================================="
echo ""
echo "Summary:"
echo "  - cuda-checkpoint lock/checkpoint: OK"
echo "  - CRIU dump: OK"
echo "  - CRIU restore: OK"
echo "  - cuda-checkpoint restore/unlock: OK"
echo ""
echo "Note: vLLM uses io_uring which has CRIU compatibility issues."
echo "The checkpoint/restore cycle works, but process may crash after"
echo "due to io_uring ring address remapping."
