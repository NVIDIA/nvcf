#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Builds and runs the --action save ordering tests.
#
# Needs CUDA 13 headers for the r580 migration types (CUcheckpointGpuPair,
# CUcheckpointRestoreArgs.gpuPairs), which 12.x does not declare. It does NOT
# need a GPU or a driver: the CUDA entry points are stubbed in the test, and
# nothing is linked against libcuda.
#
# Uses the same image as Dockerfile.base's cuda-cli-builder stage so the tests
# compile against exactly the headers the shipped binary is built with.

set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
IMAGE="${CUDA_BUILD_IMAGE:-nvidia/cuda:13.0.3-devel-ubuntu22.04}"

if command -v gcc >/dev/null 2>&1 && [ -f /usr/local/cuda/include/cuda.h ] && \
   grep -q "gpuPairs" /usr/local/cuda/include/cuda.h 2>/dev/null; then
    echo "Building locally (CUDA headers with gpuPairs found)"
    gcc -O2 -Wall -o /tmp/nvsnap-save-order-test "$HERE/save_order_test.c" \
        -I/usr/local/cuda/include
    exec /tmp/nvsnap-save-order-test
fi

echo "Building in $IMAGE"
exec docker run --rm -v "$HERE/..:/src:ro" "$IMAGE" bash -c '
    gcc -O2 -Wall -o /tmp/t /src/tests/save_order_test.c -I/usr/local/cuda/include && exec /tmp/t
'
