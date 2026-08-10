#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

#
# Build and run the CUDA interception test suite.
# Requires: GPU, CUDA runtime/driver available.
#
# Usage:
#   ./tests/run_cuda_tests.sh          # from lib/nvsnap_intercept/
#   make test-cuda                     # via Makefile target
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
TEST_BIN="$SCRIPT_DIR/test_cuda_intercept"
LIB="$ROOT_DIR/libnvsnap_intercept.so"

echo "=== Building libnvsnap_intercept.so ==="
make -C "$ROOT_DIR" -j$(nproc)

echo ""
echo "=== Compiling test_cuda_intercept ==="
gcc -g -O0 -Wall -Wextra \
    -o "$TEST_BIN" \
    "$SCRIPT_DIR/test_cuda_intercept.c" \
    -I"$ROOT_DIR/include" \
    -ldl -lpthread

echo ""
echo "=== Running: CUDA interception tests (hooks ENABLED) ==="
NVSNAP_CUDA_INTERCEPT=1 \
NVSNAP_NCCL_INTERCEPT=0 \
NVSNAP_LOG_LEVEL=3 \
LD_PRELOAD="$LIB" \
    "$TEST_BIN"
rc=$?

echo ""
echo "=== Running: Disabled mode (hooks OFF, verify passthrough) ==="
NVSNAP_CUDA_INTERCEPT=0 \
NVSNAP_NCCL_INTERCEPT=0 \
NVSNAP_LOG_LEVEL=1 \
LD_PRELOAD="$LIB" \
    "$TEST_BIN" || true

echo ""
if [ $rc -eq 0 ]; then
    echo "ALL TESTS PASSED"
else
    echo "SOME TESTS FAILED (exit code $rc)"
fi
exit $rc
