#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

#
# Test GPU container checkpoint
# This script demonstrates the full checkpoint flow:
#   1. cuda-checkpoint to freeze GPU
#   2. crictl checkpoint to save container state
#   3. Verify checkpoint was created
#
set -euo pipefail

CONTAINER_ID="${1:-}"
CHECKPOINT_DIR="${2:-/var/lib/nvsnap/checkpoints}"
CHECKPOINT_NAME="${3:-checkpoint-$(date +%Y%m%d-%H%M%S)}"

if [[ -z "$CONTAINER_ID" ]]; then
    echo "Usage: $0 <container-id> [checkpoint-dir] [checkpoint-name]"
    echo ""
    echo "Example:"
    echo "  $0 235a413700d5e274e706fd4435e72d22536959731ef588ed19f113c5b418c214"
    exit 1
fi

echo "╔═══════════════════════════════════════════════════════════════╗"
echo "║     GPU Container Checkpoint Test                              ║"
echo "╚═══════════════════════════════════════════════════════════════╝"
echo ""
echo "Container ID: $CONTAINER_ID"
echo "Checkpoint:   $CHECKPOINT_DIR/$CHECKPOINT_NAME"
echo ""

# Create checkpoint directory
mkdir -p "$CHECKPOINT_DIR/$CHECKPOINT_NAME"

# Step 1: Get container process info
echo "[Step 1/5] Getting container process info..."
CONTAINER_PID=$(crictl inspect "$CONTAINER_ID" 2>/dev/null | python3 -c "import sys,json; d=json.load(sys.stdin); print(d['info']['pid'])")
echo "  Container init PID: $CONTAINER_PID"

# Find GPU process (child with CUDA context)
GPU_PID=$(nvidia-smi --query-compute-apps=pid --format=csv,noheader | head -1 | tr -d ' ')
if [[ -z "$GPU_PID" ]]; then
    echo "  WARNING: No GPU process found via nvidia-smi"
    GPU_PID="$CONTAINER_PID"
else
    echo "  GPU process PID: $GPU_PID"
fi

# Step 2: Lock GPU
echo ""
echo "[Step 2/5] Locking GPU (cuda-checkpoint)..."
if cuda-checkpoint --action lock --pid "$GPU_PID" --timeout 30000 2>&1; then
    echo "  GPU locked successfully"
else
    echo "  WARNING: cuda-checkpoint lock failed (continuing anyway)"
fi

# Step 3: Checkpoint GPU state
echo ""
echo "[Step 3/5] Checkpointing GPU state..."
if cuda-checkpoint --action checkpoint --pid "$GPU_PID" 2>&1; then
    echo "  GPU state saved"
else
    echo "  WARNING: cuda-checkpoint failed (continuing anyway)"
fi

# Step 4: Checkpoint container with crictl
echo ""
echo "[Step 4/5] Checkpointing container (crictl)..."
CHECKPOINT_TAR="$CHECKPOINT_DIR/$CHECKPOINT_NAME/container.tar"

# Set a long timeout for crictl (5 minutes)
export CONTAINER_RUNTIME_ENDPOINT="unix:///run/containerd/containerd.sock"

# Run crictl checkpoint with timeout wrapper
timeout 300 crictl checkpoint --export "$CHECKPOINT_TAR" "$CONTAINER_ID" 2>&1 || {
    RESULT=$?
    if [[ $RESULT -eq 124 ]]; then
        echo "  ERROR: Checkpoint timed out after 5 minutes"
    else
        echo "  ERROR: Checkpoint failed with exit code $RESULT"
    fi
    
    # Try to unlock GPU even on failure
    echo "  Attempting GPU unlock..."
    cuda-checkpoint --action restore --pid "$GPU_PID" 2>/dev/null || true
    cuda-checkpoint --action unlock --pid "$GPU_PID" 2>/dev/null || true
    exit 1
}

echo "  Container checkpoint saved"

# Step 5: Verify
echo ""
echo "[Step 5/5] Verifying checkpoint..."
if [[ -f "$CHECKPOINT_TAR" ]]; then
    SIZE=$(du -h "$CHECKPOINT_TAR" | cut -f1)
    echo "  ✓ Checkpoint created: $CHECKPOINT_TAR ($SIZE)"
    
    # Save metadata
    cat > "$CHECKPOINT_DIR/$CHECKPOINT_NAME/metadata.json" << EOF
{
    "timestamp": "$(date -Iseconds)",
    "container_id": "$CONTAINER_ID",
    "container_pid": $CONTAINER_PID,
    "gpu_pid": $GPU_PID,
    "checkpoint_file": "$CHECKPOINT_TAR",
    "size_bytes": $(stat -c%s "$CHECKPOINT_TAR")
}
EOF
    echo "  ✓ Metadata saved"
    
    ls -la "$CHECKPOINT_DIR/$CHECKPOINT_NAME/"
else
    echo "  ✗ Checkpoint file not found!"
    exit 1
fi

echo ""
echo "═══════════════════════════════════════════════════════════════"
echo "Checkpoint completed successfully!"
echo "═══════════════════════════════════════════════════════════════"
