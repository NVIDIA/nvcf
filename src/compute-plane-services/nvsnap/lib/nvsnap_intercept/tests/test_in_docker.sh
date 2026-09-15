#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

#
# Test intercept library inside Docker container
#
# This creates a reproducible test environment with PyTorch/vLLM.
#
# Usage:
#   ./tests/test_in_docker.sh                # Run all tests
#   ./tests/test_in_docker.sh dlsym          # Just dlsym test
#   ./tests/test_in_docker.sh torch          # PyTorch distributed
#   ./tests/test_in_docker.sh torch-light    # Lightweight mode
#   ./tests/test_in_docker.sh vllm           # Full vLLM

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LIB_DIR="$(dirname "$SCRIPT_DIR")"

# Build library locally first
echo "=== Building intercept library ==="
cd "$LIB_DIR"
make clean && make
echo ""

# Get test type
TEST_TYPE="${1:-all}"

# Docker image with PyTorch and CUDA
DOCKER_IMAGE="docker.io/pytorch/pytorch:2.1.0-cuda12.1-cudnn8-runtime"

# Check for GPU
if ! command -v nvidia-smi &>/dev/null; then
    echo "WARNING: nvidia-smi not found, tests may fail without GPU"
    GPU_FLAG=""
else
    GPU_FLAG="--gpus all"
fi

run_in_docker() {
    local cmd="$1"
    docker run --rm -it \
        $GPU_FLAG \
        -v "$LIB_DIR":/nvsnap_intercept:ro \
        -v "$SCRIPT_DIR":/tests:ro \
        -e NVSNAP_LOG_LEVEL=4 \
        -e PYTHONFAULTHANDLER=1 \
        -w /tests \
        "$DOCKER_IMAGE" \
        bash -c "$cmd"
}

case "$TEST_TYPE" in
    dlsym)
        echo "=== Testing dlsym in Docker ==="
        run_in_docker "
            apt-get update -qq && apt-get install -qq -y gcc > /dev/null
            gcc -o /tmp/test_dlsym /tests/test_dlsym_basic.c -ldl -lpthread
            echo '--- Without intercept ---'
            /tmp/test_dlsym
            echo ''
            echo '--- With intercept ---'
            LD_PRELOAD=/nvsnap_intercept/libnvsnap_intercept.so /tmp/test_dlsym
        "
        ;;
    
    torch)
        echo "=== Testing PyTorch distributed in Docker ==="
        run_in_docker "
            echo '--- Without intercept ---'
            python3 /tests/test_pytorch_distributed.py
            echo ''
            echo '--- With intercept ---'
            LD_PRELOAD=/nvsnap_intercept/libnvsnap_intercept.so python3 /tests/test_pytorch_distributed.py
        "
        ;;
    
    torch-light)
        echo "=== Testing PyTorch distributed (lightweight mode) ==="
        run_in_docker "
            NVSNAP_LIGHTWEIGHT=1 LD_PRELOAD=/nvsnap_intercept/libnvsnap_intercept.so \
                python3 /tests/test_pytorch_distributed.py
        "
        ;;
    
    vllm)
        echo "=== Testing with vLLM ==="
        docker run --rm -it \
            $GPU_FLAG \
            -v "$LIB_DIR":/nvsnap_intercept:ro \
            -e NVSNAP_LOG_LEVEL=4 \
            -e PYTHONFAULTHANDLER=1 \
            docker.io/vllm/vllm-openai:v0.6.6.post1 \
            bash -c "
                echo '--- Without intercept ---'
                python3 -c 'import vllm; print(\"vLLM:\", vllm.__version__)' || echo 'vLLM import OK'
                echo ''
                echo '--- With intercept ---'
                LD_PRELOAD=/nvsnap_intercept/libnvsnap_intercept.so \
                    python3 -c 'import vllm; print(\"vLLM:\", vllm.__version__)' || echo 'vLLM import FAILED'
            "
        ;;
    
    all)
        echo "=== Running all tests in Docker ==="
        echo ""
        "$0" dlsym
        echo ""
        "$0" torch
        echo ""
        "$0" torch-light
        ;;
    
    *)
        echo "Unknown test: $TEST_TYPE"
        echo "Usage: $0 [dlsym|torch|torch-light|vllm|all]"
        exit 1
        ;;
esac

echo ""
echo "=== Done ==="
