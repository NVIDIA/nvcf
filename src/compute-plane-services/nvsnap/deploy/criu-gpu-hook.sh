#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

#
# CRIU Action Script for GPU Checkpoint
# This script is called by CRIU at various stages of checkpoint/restore.
# It integrates cuda-checkpoint with CRIU.
#
# Usage: Set as CRIU action script:
#   criu dump ... --action-script /usr/local/bin/criu-gpu-hook.sh
#
# CRIU passes the action as the first argument:
#   pre-dump, post-dump, pre-restore, post-restore, etc.
#
set -euo pipefail

LOG_FILE="/var/log/criu-gpu-hook.log"
CUDA_CHECKPOINT="/usr/local/bin/cuda-checkpoint"

log() {
    echo "[$(date -Iseconds)] $*" >> "$LOG_FILE"
}

get_gpu_pids() {
    # Get all PIDs using GPU
    nvidia-smi --query-compute-apps=pid --format=csv,noheader 2>/dev/null | tr -d ' ' | sort -u
}

# Action received from CRIU
ACTION="${1:-unknown}"
CRIU_PID="${CRTOOLS_INIT_PID:-}"

log "Action: $ACTION, CRIU_PID: $CRIU_PID"

case "$ACTION" in
    pre-dump)
        log "Pre-dump: Locking and checkpointing GPU state"
        for pid in $(get_gpu_pids); do
            log "  Processing GPU PID: $pid"
            if $CUDA_CHECKPOINT --action lock --pid "$pid" --timeout 30000 2>> "$LOG_FILE"; then
                log "    Locked PID $pid"
                if $CUDA_CHECKPOINT --action checkpoint --pid "$pid" 2>> "$LOG_FILE"; then
                    log "    Checkpointed PID $pid"
                else
                    log "    WARNING: Checkpoint failed for PID $pid"
                fi
            else
                log "    WARNING: Lock failed for PID $pid"
            fi
        done
        ;;
        
    post-dump)
        log "Post-dump: GPU state saved, no action needed"
        ;;
        
    pre-restore)
        log "Pre-restore: Preparing for GPU restore"
        ;;
        
    post-restore)
        log "Post-restore: Restoring GPU state"
        for pid in $(get_gpu_pids); do
            log "  Restoring GPU PID: $pid"
            if $CUDA_CHECKPOINT --action restore --pid "$pid" 2>> "$LOG_FILE"; then
                log "    Restored PID $pid"
                if $CUDA_CHECKPOINT --action unlock --pid "$pid" 2>> "$LOG_FILE"; then
                    log "    Unlocked PID $pid"
                else
                    log "    WARNING: Unlock failed for PID $pid"
                fi
            else
                log "    WARNING: Restore failed for PID $pid"
            fi
        done
        ;;
        
    setup-namespaces|post-setup-namespaces|network-lock|network-unlock)
        log "Namespace action: $ACTION (no GPU action needed)"
        ;;
        
    *)
        log "Unknown action: $ACTION"
        ;;
esac

log "Action $ACTION completed"
exit 0
