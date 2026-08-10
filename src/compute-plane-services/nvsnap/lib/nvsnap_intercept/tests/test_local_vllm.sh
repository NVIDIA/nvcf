#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

#
# Local vLLM test with libnvsnap_intercept.so
#
# This script tests the intercept library locally without Kubernetes.
# Much faster iteration for debugging segfaults and other issues.
#
# Prerequisites:
# - CUDA installed locally
# - vLLM installed: pip install vllm
# - GPU available
#
# Usage:
#   ./test_local_vllm.sh              # Run with intercept library
#   ./test_local_vllm.sh --no-preload # Run without intercept (baseline)
#   ./test_local_vllm.sh --gdb        # Run with gdb for debugging
#   ./test_local_vllm.sh --strace     # Run with strace
#   ./test_local_vllm.sh --lightweight # Run with NVSNAP_LIGHTWEIGHT=1

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LIB_DIR="$(dirname "$SCRIPT_DIR")"
LIB_PATH="${LIB_DIR}/libnvsnap_intercept.so"

# Parse arguments
USE_PRELOAD=1
USE_GDB=0
USE_STRACE=0
LIGHTWEIGHT=0

for arg in "$@"; do
    case $arg in
        --no-preload) USE_PRELOAD=0 ;;
        --gdb) USE_GDB=1 ;;
        --strace) USE_STRACE=1 ;;
        --lightweight) LIGHTWEIGHT=1 ;;
        --help|-h)
            echo "Usage: $0 [OPTIONS]"
            echo ""
            echo "Options:"
            echo "  --no-preload    Run without LD_PRELOAD (baseline test)"
            echo "  --gdb           Run under gdb for debugging"
            echo "  --strace        Run with strace"
            echo "  --lightweight   Disable io_uring/libuv interception"
            exit 0
            ;;
    esac
done

# Build library if needed
echo "=== Building intercept library ==="
cd "$LIB_DIR"
make clean && make
echo ""

# Check if library exists
if [ ! -f "$LIB_PATH" ]; then
    echo "ERROR: Library not found at $LIB_PATH"
    exit 1
fi

echo "=== Library built: $LIB_PATH ==="
ls -la "$LIB_PATH"
echo ""

# Setup environment
export CUDA_VISIBLE_DEVICES=0
export NVSNAP_LOG_LEVEL=4  # Debug level
export PYTHONFAULTHANDLER=1
export PYTHONUNBUFFERED=1

if [ $LIGHTWEIGHT -eq 1 ]; then
    export NVSNAP_LIGHTWEIGHT=1
    echo "=== Lightweight mode enabled (no io_uring/libuv interception) ==="
fi

# Build the command
VLLM_CMD="python3 -m vllm.entrypoints.openai.api_server \
    --model TinyLlama/TinyLlama-1.1B-Chat-v1.0 \
    --host 127.0.0.1 \
    --port 8000 \
    --max-model-len 512 \
    --gpu-memory-utilization 0.3"

if [ $USE_PRELOAD -eq 1 ]; then
    export LD_PRELOAD="$LIB_PATH"
    echo "=== Running with LD_PRELOAD=$LIB_PATH ==="
else
    echo "=== Running WITHOUT LD_PRELOAD (baseline) ==="
fi

echo ""
echo "=== Starting vLLM ==="
echo "Command: $VLLM_CMD"
echo ""

if [ $USE_GDB -eq 1 ]; then
    echo "=== Running under GDB ==="
    echo "Type 'run' to start, 'bt' for backtrace on crash"
    gdb -ex "set follow-fork-mode child" -ex "run" --args $VLLM_CMD
elif [ $USE_STRACE -eq 1 ]; then
    echo "=== Running with strace ==="
    strace -f -e trace=openat,socket,connect,bind $VLLM_CMD
else
    $VLLM_CMD
fi
