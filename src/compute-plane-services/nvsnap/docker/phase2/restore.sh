#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# NVSNAP Restore Script
# Run as container entrypoint to restore from checkpoint
#
# Usage: /app/restore.sh /path/to/checkpoint/dir
#
# This will:
# 1. Verify checkpoint images exist
# 2. Run CRIU restore
# 3. Restore GPU state with cuda-checkpoint
# 4. Process resumes execution

set -e

CHECKPOINT_DIR="${1:-/checkpoints}"
LOG_PREFIX="[NVSNAP-RESTORE]"

log() { echo "$LOG_PREFIX $1"; }
error() { echo "$LOG_PREFIX ERROR: $1" >&2; exit 1; }

log "Starting restore from $CHECKPOINT_DIR"

# Verify checkpoint exists
if [ ! -d "$CHECKPOINT_DIR" ]; then
    error "Checkpoint directory not found: $CHECKPOINT_DIR"
fi

IMG_COUNT=$(ls "$CHECKPOINT_DIR"/*.img 2>/dev/null | wc -l)
if [ "$IMG_COUNT" -eq 0 ]; then
    error "No checkpoint images found in $CHECKPOINT_DIR"
fi

log "Found $IMG_COUNT checkpoint images"

# Show metadata if available
if [ -f "$CHECKPOINT_DIR/metadata.json" ]; then
    log "Checkpoint metadata:"
    cat "$CHECKPOINT_DIR/metadata.json"
fi

# Step 1: CRIU restore
log "Step 1: CRIU restore..."
cd "$CHECKPOINT_DIR"

criu restore \
    -D "$CHECKPOINT_DIR" \
    --shell-job \
    --restore-detached \
    -v0 2>&1 || error "CRIU restore failed"

log "CRIU restore complete"

# Wait a moment for process to start
sleep 1

# Find the restored process
RESTORED_PID=$(pgrep -f "gpu_loop|vllm|python.*torch" | head -1)

if [ -z "$RESTORED_PID" ]; then
    error "Restored process not found"
fi

log "Restored process PID: $RESTORED_PID"

# Step 2: Restore GPU state
log "Step 2: Restoring GPU state..."
cuda-checkpoint --action restore --pid $RESTORED_PID || {
    log "Warning: GPU restore failed (may need manual intervention)"
}

# Step 3: Unlock GPU
log "Step 3: Unlocking GPU..."
cuda-checkpoint --action unlock --pid $RESTORED_PID || {
    log "Warning: GPU unlock failed"
}

# Verify
GPU_STATE=$(cuda-checkpoint --get-state --pid $RESTORED_PID 2>/dev/null || echo "unknown")
log "GPU state: $GPU_STATE"

log "============================================"
log "Restore complete!"
log "Process $RESTORED_PID is now running"
log "============================================"

# Keep container alive by waiting on the restored process
wait $RESTORED_PID 2>/dev/null || true
