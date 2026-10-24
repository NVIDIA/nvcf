#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

#
# Local checkpoint/restore test WITHOUT Kubernetes
#
# This tests our intercept library with CRIU directly.
# Much faster iteration than deploying to k8s.
#
# Prerequisites:
# - CRIU installed (apt install criu or from our fork)
# - cuda-checkpoint tool
# - GPU available
# - Root privileges (for CRIU)
#
# Usage:
#   sudo ./test_local_checkpoint.sh              # Full test
#   sudo ./test_local_checkpoint.sh --no-gpu     # CPU-only test (no cuda-checkpoint)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LIB_DIR="$(dirname "$SCRIPT_DIR")"
PROJECT_ROOT="$(dirname "$(dirname "$LIB_DIR")")"
LIB_PATH="${LIB_DIR}/libnvsnap_intercept.so"

# Checkpoint directory
CKPT_DIR="/tmp/nvsnap-local-test-$$"

# Options
USE_GPU=1
for arg in "$@"; do
    case $arg in
        --no-gpu) USE_GPU=0 ;;
    esac
done

cleanup() {
    echo "=== Cleanup ==="
    # Kill any remaining test processes
    pkill -f "test_checkpoint_app" 2>/dev/null || true
    rm -rf "$CKPT_DIR"
}
trap cleanup EXIT

echo "========================================"
echo " Local Checkpoint/Restore Test"
echo "========================================"
echo ""
echo "Library: $LIB_PATH"
echo "Checkpoint dir: $CKPT_DIR"
echo "GPU mode: $USE_GPU"
echo ""

# Check prerequisites
if [ ! -f "$LIB_PATH" ]; then
    echo "Building intercept library..."
    cd "$LIB_DIR" && make
fi

if ! command -v criu &>/dev/null; then
    echo "ERROR: criu not found. Install with: apt install criu"
    exit 1
fi

if [ "$USE_GPU" -eq 1 ]; then
    if ! command -v nvidia-smi &>/dev/null; then
        echo "WARNING: nvidia-smi not found, disabling GPU mode"
        USE_GPU=0
    fi
fi

mkdir -p "$CKPT_DIR"

# Create a simple test application
cat > /tmp/test_checkpoint_app.py << 'PYTHON'
#!/usr/bin/env python3
"""Simple app for checkpoint/restore testing"""
import os
import sys
import time
import signal

# Track state
counter = 0
restored = os.environ.get("NVSNAP_RESTORED") == "1"

def signal_handler(sig, frame):
    print(f"[APP] Received signal {sig}, counter={counter}")
    if sig == signal.SIGUSR1:
        print("[APP] Preparing for checkpoint...")
        sys.stdout.flush()

signal.signal(signal.SIGUSR1, signal_handler)
signal.signal(signal.SIGUSR2, signal_handler)

print(f"[APP] Started, PID={os.getpid()}, restored={restored}")
sys.stdout.flush()

# If GPU available, do some CUDA work
try:
    import torch
    if torch.cuda.is_available():
        print(f"[APP] CUDA available: {torch.cuda.get_device_name(0)}")
        x = torch.zeros(1000, device='cuda')
        print(f"[APP] Created CUDA tensor: {x.shape}")
except ImportError:
    print("[APP] PyTorch not available, CPU-only mode")
except Exception as e:
    print(f"[APP] CUDA init error: {e}")

# Main loop
print("[APP] Entering main loop...")
while True:
    counter += 1
    if counter % 10 == 0:
        print(f"[APP] Counter: {counter}")
        sys.stdout.flush()
    time.sleep(0.5)
PYTHON
chmod +x /tmp/test_checkpoint_app.py

echo "=== Step 1: Starting test application ==="
cd /tmp

# Start the app with our intercept library
NVSNAP_LOG_LEVEL=3 LD_PRELOAD="$LIB_PATH" \
    python3 /tmp/test_checkpoint_app.py &
APP_PID=$!

echo "App PID: $APP_PID"
sleep 3  # Let it run a bit

# Verify it's running
if ! kill -0 $APP_PID 2>/dev/null; then
    echo "ERROR: App failed to start"
    exit 1
fi
echo "App is running"

echo ""
echo "=== Step 2: Sending quiesce signal (SIGUSR1) ==="
kill -USR1 $APP_PID
sleep 1

echo ""
echo "=== Step 3: Creating checkpoint ==="

# GPU checkpoint (if enabled)
if [ "$USE_GPU" -eq 1 ]; then
    CUDA_CKPT="${PROJECT_ROOT}/bin/criu-bundle/cuda-checkpoint"
    if [ -f "$CUDA_CKPT" ]; then
        echo "Locking GPU with cuda-checkpoint..."
        $CUDA_CKPT --lock --pid $APP_PID || echo "cuda-checkpoint lock failed (may be OK if no GPU context)"
    else
        echo "WARNING: cuda-checkpoint not found at $CUDA_CKPT"
    fi
fi

# CRIU dump
echo "Running CRIU dump..."
criu dump \
    --tree $APP_PID \
    --images-dir "$CKPT_DIR" \
    --leave-running \
    --shell-job \
    --tcp-established \
    -v4 \
    -o "$CKPT_DIR/dump.log" || {
        echo "CRIU dump failed! Log:"
        cat "$CKPT_DIR/dump.log" | tail -50
        exit 1
    }

echo "Checkpoint created successfully!"
ls -la "$CKPT_DIR"

# GPU unlock (if enabled)
if [ "$USE_GPU" -eq 1 ] && [ -f "$CUDA_CKPT" ]; then
    echo "Unlocking GPU..."
    $CUDA_CKPT --unlock --pid $APP_PID || true
fi

# Send resume signal
echo ""
echo "=== Step 4: Sending resume signal (SIGUSR2) ==="
kill -USR2 $APP_PID
sleep 2

echo ""
echo "=== Step 5: Stopping original process ==="
kill $APP_PID 2>/dev/null || true
wait $APP_PID 2>/dev/null || true
sleep 1

echo ""
echo "=== Step 6: Restoring from checkpoint ==="

# Set restored flag
export NVSNAP_RESTORED=1
export NVSNAP_LOG_LEVEL=3
export LD_PRELOAD="$LIB_PATH"

# CRIU restore
echo "Running CRIU restore..."
criu restore \
    --images-dir "$CKPT_DIR" \
    --shell-job \
    --tcp-established \
    -v4 \
    -o "$CKPT_DIR/restore.log" &
RESTORE_PID=$!

sleep 3

# Check if restored process is running
if kill -0 $RESTORE_PID 2>/dev/null; then
    echo "Restored process is running!"
    
    # Let it run a bit
    sleep 5
    
    # Check it's still alive
    if kill -0 $RESTORE_PID 2>/dev/null; then
        echo ""
        echo "========================================"
        echo " SUCCESS: Checkpoint/Restore works!"
        echo "========================================"
        
        # Cleanup
        kill $RESTORE_PID 2>/dev/null || true
    else
        echo "ERROR: Restored process died!"
        cat "$CKPT_DIR/restore.log" | tail -30
        exit 1
    fi
else
    echo "ERROR: Restore failed!"
    cat "$CKPT_DIR/restore.log" | tail -50
    exit 1
fi
