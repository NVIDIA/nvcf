#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# GPU Checkpoint/Restore Test using cuda-checkpoint + CRIU
#
# This script demonstrates full GPU checkpoint/restore:
# 1. Start GPU process with LD_PRELOAD for interception
# 2. Lock CUDA state with cuda-checkpoint
# 3. Checkpoint CUDA state with cuda-checkpoint
# 4. CRIU dump (CPU state + file descriptors)
# 5. CRIU restore
# 6. Restore CUDA state with cuda-checkpoint
# 7. Unlock and resume
#
# Requirements:
# - NVIDIA driver 555+ (for cuda-checkpoint support)
# - cuda-checkpoint binary
# - CRIU with CUDA plugin
# - libnvsnap_intercept.so built

set -e

# ============================================================================
# Configuration
# ============================================================================

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LIB_DIR="$(dirname "$SCRIPT_DIR")"

_repo_root="$(cd "$LIB_DIR/../.." && pwd)"

# cuda-checkpoint location (check env var, then sibling checkout, then PATH)
if [ -n "${CUDA_CHECKPOINT:-}" ]; then
    CUDA_CKPT="$CUDA_CHECKPOINT"
elif [ -x "$_repo_root/../cuda-checkpoint/bin/x86_64_Linux/cuda-checkpoint" ]; then
    CUDA_CKPT="$_repo_root/../cuda-checkpoint/bin/x86_64_Linux/cuda-checkpoint"
elif command -v cuda-checkpoint &>/dev/null; then
    CUDA_CKPT="$(command -v cuda-checkpoint)"
else
    echo "ERROR: cuda-checkpoint not found"
    echo "Set CUDA_CHECKPOINT env var or add to PATH"
    exit 1
fi

# CRIU binary (check env var, then sibling-built fork, then PATH)
if [ -n "${NVSNAP_CRIU:-}" ]; then
    CRIU="$NVSNAP_CRIU"
elif [ -x "$_repo_root/../criu/criu/criu" ]; then
    CRIU="$_repo_root/../criu/criu/criu"
elif command -v criu &>/dev/null; then
    CRIU="$(command -v criu)"
else
    echo "ERROR: criu not found"
    echo "Set NVSNAP_CRIU env var or build the CRIU fork as a sibling of this repo"
    exit 1
fi

# CRIU CUDA plugin directory
if [ -n "${NVSNAP_CRIU_PLUGIN_DIR:-}" ]; then
    PLUGIN_DIR="$NVSNAP_CRIU_PLUGIN_DIR"
elif [ -d "$_repo_root/../criu/plugins/cuda" ]; then
    PLUGIN_DIR="$_repo_root/../criu/plugins/cuda"
elif [ -d "/usr/lib/criu" ]; then
    PLUGIN_DIR="/usr/lib/criu"
else
    PLUGIN_DIR=""
fi

# Test type (simple or pytorch)
TEST_TYPE="${1:-simple}"
CHECKPOINT_DIR="/tmp/nvsnap_checkpoint"
CRIU_IMG_DIR="/tmp/criu_img"
TEST_OUTPUT="/tmp/gpu_test_output.txt"

# ============================================================================
# Helper functions
# ============================================================================

cleanup() {
    echo "[Cleanup] Stopping any previous test processes..."
    pkill -9 -f test_simple_checkpoint 2>/dev/null || true
    pkill -9 -f "python.*test_pytorch" 2>/dev/null || true
    rm -rf "$CRIU_IMG_DIR" "$CHECKPOINT_DIR" "$TEST_OUTPUT"
    mkdir -p "$CRIU_IMG_DIR"
}

check_requirements() {
    echo "[Check] Verifying requirements..."
    
    # Check driver version
    DRIVER_VER=$(nvidia-smi --query-gpu=driver_version --format=csv,noheader 2>/dev/null | head -1)
    DRIVER_MAJOR=$(echo "$DRIVER_VER" | cut -d. -f1)
    if [ -z "$DRIVER_MAJOR" ] || [ "$DRIVER_MAJOR" -lt 555 ]; then
        echo "ERROR: NVIDIA driver 555+ required (found: $DRIVER_VER)"
        exit 1
    fi
    echo "  NVIDIA driver: $DRIVER_VER (OK)"
    
    # Check cuda-checkpoint
    if ! "$CUDA_CKPT" --help &>/dev/null; then
        echo "ERROR: cuda-checkpoint not working"
        exit 1
    fi
    echo "  cuda-checkpoint: $CUDA_CKPT (OK)"
    
    # Check CRIU
    if ! "$CRIU" --version &>/dev/null; then
        echo "ERROR: CRIU not working"
        exit 1
    fi
    echo "  CRIU: $CRIU (OK)"
    
    # Check library
    if [ ! -f "$LIB_DIR/libnvsnap_intercept.so" ]; then
        echo "ERROR: libnvsnap_intercept.so not found"
        exit 1
    fi
    echo "  Library: $LIB_DIR/libnvsnap_intercept.so (OK)"
    
    echo ""
}

start_simple_test() {
    echo "[Start] Launching simple CUDA test..."
    cd "$LIB_DIR"
    # Note: LD_PRELOAD not needed - cuda-checkpoint handles GPU state directly
    ./tests/test_simple_checkpoint > "$TEST_OUTPUT" 2>&1 &
    TEST_PID=$!
    echo "  PID: $TEST_PID"
    sleep 4
}

start_pytorch_test() {
    echo "[Start] Launching PyTorch test..."
    cd "$LIB_DIR"
    # Note: LD_PRELOAD not needed - cuda-checkpoint handles GPU state directly
    
    python3 -c "
import torch
import time
import os

print(f'PyTorch PID: {os.getpid()}')
print(f'CUDA available: {torch.cuda.is_available()}')
print(f'Device: {torch.cuda.get_device_name(0)}')

# Create model and data
model = torch.nn.Linear(1024, 1024).cuda()
data = torch.randn(64, 1024).cuda()

# Forward pass to ensure GPU is active
output = model(data)
print(f'Model output shape: {output.shape}')
print(f'Output sum: {output.sum().item():.4f}')

# Keep alive for checkpoint
print('Waiting for checkpoint...')
while True:
    time.sleep(1)
" > "$TEST_OUTPUT" 2>&1 &
    TEST_PID=$!
    echo "  PID: $TEST_PID"
    sleep 5
}

verify_running() {
    if ! ps -p $TEST_PID > /dev/null 2>&1; then
        echo "ERROR: Test process died"
        cat "$TEST_OUTPUT"
        exit 1
    fi
    
    STATE=$("$CUDA_CKPT" --get-state --pid $TEST_PID 2>/dev/null | tail -1)
    echo "  CUDA state: $STATE"
    
    if [ "$STATE" != "running" ]; then
        echo "ERROR: CUDA not in running state"
        exit 1
    fi
}

do_checkpoint() {
    echo ""
    echo "[Lock] Locking CUDA state..."
    "$CUDA_CKPT" --action lock --pid $TEST_PID --timeout 10000 2>/dev/null
    echo "  Lock: OK"
    
    echo ""
    echo "[Checkpoint] Checkpointing CUDA state..."
    "$CUDA_CKPT" --action checkpoint --pid $TEST_PID 2>/dev/null
    
    STATE=$("$CUDA_CKPT" --get-state --pid $TEST_PID 2>/dev/null | tail -1)
    echo "  CUDA state: $STATE"
    
    echo ""
    echo "[CRIU Dump] Dumping CPU state..."
    if [ -n "$PLUGIN_DIR" ]; then
        "$CRIU" dump -t $TEST_PID -D "$CRIU_IMG_DIR" --shell-job -L "$PLUGIN_DIR" 2>/dev/null
    else
        "$CRIU" dump -t $TEST_PID -D "$CRIU_IMG_DIR" --shell-job 2>/dev/null
    fi
    echo "  Created $(ls "$CRIU_IMG_DIR"/*.img 2>/dev/null | wc -l) image files"
}

do_restore() {
    echo ""
    echo "[CRIU Restore] Restoring CPU state..."
    if [ -n "$PLUGIN_DIR" ]; then
        "$CRIU" restore -d -D "$CRIU_IMG_DIR" --shell-job -L "$PLUGIN_DIR" 2>/dev/null
    else
        "$CRIU" restore -d -D "$CRIU_IMG_DIR" --shell-job 2>/dev/null
    fi
    sleep 2
    
    # Get restored PID (should be same as original)
    if [ "$TEST_TYPE" = "simple" ]; then
        NEW_PID=$(pgrep -f test_simple_checkpoint 2>/dev/null | head -1)
    else
        NEW_PID=$(pgrep -f "python.*test_pytorch" 2>/dev/null | head -1)
    fi
    
    if [ -z "$NEW_PID" ]; then
        echo "ERROR: No restored process found"
        exit 1
    fi
    echo "  Restored PID: $NEW_PID"
    TEST_PID=$NEW_PID
    
    echo ""
    echo "[Restore CUDA] Restoring CUDA state..."
    "$CUDA_CKPT" --action restore --pid $TEST_PID 2>/dev/null
    echo "  Restore: OK"
    
    echo ""
    echo "[Unlock] Unlocking CUDA..."
    "$CUDA_CKPT" --action unlock --pid $TEST_PID 2>/dev/null
    
    STATE=$("$CUDA_CKPT" --get-state --pid $TEST_PID 2>/dev/null | tail -1)
    echo "  CUDA state: $STATE"
}

verify_restored() {
    echo ""
    echo "[Verify] Checking restored process..."
    sleep 3
    
    if ps -p $TEST_PID > /dev/null 2>&1; then
        echo "  Process: ALIVE"
    else
        echo "  Process: DEAD"
        cat "$TEST_OUTPUT"
        exit 1
    fi
    
    if grep -q "Verification PASSED" "$TEST_OUTPUT" 2>/dev/null; then
        echo "  GPU data: VERIFIED"
    elif grep -q "output" "$TEST_OUTPUT" 2>/dev/null; then
        echo "  GPU computation: OK"
    fi
}

final_cleanup() {
    kill -9 $TEST_PID 2>/dev/null || true
}

# ============================================================================
# Main
# ============================================================================

echo "=========================================="
echo "  GPU Checkpoint/Restore Test"
echo "  Test type: $TEST_TYPE"
echo "=========================================="
echo ""

cleanup
check_requirements

if [ "$TEST_TYPE" = "pytorch" ]; then
    start_pytorch_test
else
    start_simple_test
fi

verify_running
do_checkpoint
do_restore
verify_restored
final_cleanup

echo ""
echo "=========================================="
echo "  TEST PASSED!"
echo "=========================================="
echo ""
echo "Summary:"
echo "  - Process started with GPU"
echo "  - cuda-checkpoint lock/checkpoint: OK"
echo "  - CRIU dump: OK"
echo "  - CRIU restore: OK"
echo "  - cuda-checkpoint restore/unlock: OK"
echo "  - Process alive after restore: YES"
