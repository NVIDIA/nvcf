#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# NVSNAP Checkpoint Script
# Run inside a container to checkpoint the main process
#
# Usage: /app/checkpoint.sh /path/to/checkpoint/dir
#
# This will:
# 1. Find the main GPU process (gpu_loop or similar)
# 2. Lock GPU with cuda-checkpoint
# 3. Checkpoint GPU state
# 4. Dump process with CRIU
# 5. Process is killed (normal for CRIU dump)

set -e

CHECKPOINT_DIR="${1:-/checkpoints}"
LOG_PREFIX="[NVSNAP-CHECKPOINT]"

log() { echo "$LOG_PREFIX $1"; }
error() { echo "$LOG_PREFIX ERROR: $1" >&2; exit 1; }

log "Starting checkpoint to $CHECKPOINT_DIR"

# Find the main process (not this script, not CRIU)
MAIN_PID=$(pgrep -f "gpu_loop|vllm|python.*torch" | grep -v $$ | head -1)

if [ -z "$MAIN_PID" ]; then
    error "No GPU process found to checkpoint"
fi

log "Found process PID: $MAIN_PID"
log "Process: $(ps -p $MAIN_PID -o comm= 2>/dev/null)"

# Create checkpoint directory
mkdir -p "$CHECKPOINT_DIR"
rm -rf "$CHECKPOINT_DIR"/*

# Step 1: Lock GPU
log "Step 1: Locking GPU..."
cuda-checkpoint --action lock --pid $MAIN_PID --timeout 30000 || error "Failed to lock GPU"

# Step 2: Checkpoint GPU state
log "Step 2: Checkpointing GPU state..."
cuda-checkpoint --action checkpoint --pid $MAIN_PID || error "Failed to checkpoint GPU"

# Step 3: CRIU dump
log "Step 3: CRIU dump..."
criu dump \
    -t $MAIN_PID \
    -D "$CHECKPOINT_DIR" \
    --shell-job \
    -v0 2>&1 || error "CRIU dump failed"

# Verify checkpoint
IMG_COUNT=$(ls "$CHECKPOINT_DIR"/*.img 2>/dev/null | wc -l)
log "Checkpoint complete: $IMG_COUNT images created"

# Save metadata
cat > "$CHECKPOINT_DIR/metadata.json" << EOF
{
    "timestamp": "$(date -Iseconds)",
    "original_pid": $MAIN_PID,
    "image_count": $IMG_COUNT,
    "version": "${VERSION:-unknown}"
}
EOF

log "Checkpoint saved to $CHECKPOINT_DIR"
log "Process $MAIN_PID has been terminated (normal for checkpoint)"
log "To restore, start a new container with: /app/restore.sh $CHECKPOINT_DIR"
