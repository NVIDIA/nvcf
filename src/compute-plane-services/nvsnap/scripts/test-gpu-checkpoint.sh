#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# NVSNAP GPU Checkpoint Test Script
#
# Tests GPU checkpoint/restore using cuda-checkpoint on a running GPU process.
# Can be run standalone or as part of the NVSNAP test suite.
#
# Usage:
#   ./test-gpu-checkpoint.sh [--pid <pid>] [--simple]
#
# Options:
#   --pid <pid>    Checkpoint an existing process
#   --simple       Run a simple CUDA test program instead of vLLM

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
NVSNAP_ROOT="$(dirname "$SCRIPT_DIR")"
CHECKPOINT_DIR="${NVSNAP_CHECKPOINT_DIR:-/tmp/nvsnap-checkpoint}"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

log() { echo -e "${GREEN}[INFO]${NC} $1"; }
warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }
error() { echo -e "${RED}[ERROR]${NC} $1"; exit 1; }

# Check prerequisites
check_prereqs() {
    log "Checking prerequisites..."
    
    if ! command -v cuda-checkpoint &>/dev/null; then
        error "cuda-checkpoint not found. Install with: scripts/install-node.sh cuda-checkpoint"
    fi
    
    if ! nvidia-smi &>/dev/null; then
        error "nvidia-smi not available. Is the NVIDIA driver installed?"
    fi
    
    log "Prerequisites OK"
}

# Get GPU state for a process
get_gpu_state() {
    local pid=$1
    cuda-checkpoint --get-state --pid "$pid" 2>/dev/null || echo "unknown"
}

# Full checkpoint cycle
checkpoint_process() {
    local pid=$1
    local timeout_ms=${2:-30000}
    
    log "Checkpointing process $pid..."
    
    # Get initial state
    local state=$(get_gpu_state "$pid")
    log "Initial GPU state: $state"
    
    if [[ "$state" != "running" ]]; then
        error "Process $pid is not in running state (state: $state)"
    fi
    
    # Lock
    log "Locking GPU state..."
    if ! sudo cuda-checkpoint --action lock --pid "$pid" --timeout "$timeout_ms"; then
        error "Failed to lock GPU state"
    fi
    
    state=$(get_gpu_state "$pid")
    log "State after lock: $state"
    
    # Checkpoint
    log "Checkpointing GPU state..."
    if ! sudo cuda-checkpoint --action checkpoint --pid "$pid"; then
        warn "Checkpoint failed, attempting restore..."
        sudo cuda-checkpoint --action restore --pid "$pid" || true
        sudo cuda-checkpoint --action unlock --pid "$pid" || true
        error "Failed to checkpoint GPU state"
    fi
    
    state=$(get_gpu_state "$pid")
    log "State after checkpoint: $state"
    
    log "GPU checkpoint complete!"
    return 0
}

# Restore GPU state
restore_process() {
    local pid=$1
    
    log "Restoring process $pid..."
    
    local state=$(get_gpu_state "$pid")
    log "Current GPU state: $state"
    
    # Restore
    log "Restoring GPU state..."
    if ! sudo cuda-checkpoint --action restore --pid "$pid"; then
        error "Failed to restore GPU state"
    fi
    
    state=$(get_gpu_state "$pid")
    log "State after restore: $state"
    
    # Unlock
    log "Unlocking GPU state..."
    if ! sudo cuda-checkpoint --action unlock --pid "$pid"; then
        error "Failed to unlock GPU state"
    fi
    
    state=$(get_gpu_state "$pid")
    log "Final state: $state"
    
    log "GPU restore complete!"
    return 0
}

# Run simple CUDA test
run_simple_test() {
    log "Running simple CUDA checkpoint test..."
    
    # Build test program if needed
    local test_prog="$NVSNAP_ROOT/lib/nvsnap_intercept/tests/test_simple_checkpoint"
    if [[ ! -f "$test_prog" ]]; then
        log "Building test program..."
        make -C "$NVSNAP_ROOT/lib/nvsnap_intercept" tests/test_simple_checkpoint
    fi
    
    # Start test program in background
    log "Starting test program..."
    "$test_prog" &
    local pid=$!
    
    # Wait for it to initialize
    sleep 2
    
    if ! kill -0 "$pid" 2>/dev/null; then
        error "Test program exited prematurely"
    fi
    
    log "Test program running with PID $pid"
    
    # Do checkpoint cycle
    checkpoint_process "$pid"
    
    log "Waiting 2 seconds..."
    sleep 2
    
    restore_process "$pid"
    
    # Verify process is still running
    sleep 1
    if kill -0 "$pid" 2>/dev/null; then
        log "✅ Test PASSED - Process still running after checkpoint/restore"
        kill "$pid" 2>/dev/null || true
        return 0
    else
        error "❌ Test FAILED - Process died after restore"
    fi
}

# Full checkpoint/restore with CRIU
run_full_test() {
    local pid=$1
    
    log "Running full checkpoint/restore test with CRIU..."
    
    if ! command -v criu &>/dev/null; then
        error "CRIU not found. Install with: scripts/install-node.sh criu"
    fi
    
    mkdir -p "$CHECKPOINT_DIR"
    
    # GPU checkpoint
    checkpoint_process "$pid"
    
    # CRIU dump
    log "Running CRIU dump..."
    if ! sudo criu dump -t "$pid" -D "$CHECKPOINT_DIR" --shell-job -v2; then
        warn "CRIU dump failed, restoring GPU..."
        restore_process "$pid"
        error "CRIU dump failed"
    fi
    
    log "Process checkpointed to $CHECKPOINT_DIR"
    
    # CRIU restore
    log "Running CRIU restore..."
    if ! sudo criu restore -D "$CHECKPOINT_DIR" --shell-job -d -v2; then
        error "CRIU restore failed"
    fi
    
    # Find new PID (CRIU may assign new PID)
    local new_pid=$(pgrep -f "test_simple_checkpoint" | head -1)
    if [[ -z "$new_pid" ]]; then
        error "Could not find restored process"
    fi
    
    log "Process restored with PID $new_pid"
    
    # GPU restore
    restore_process "$new_pid"
    
    log "✅ Full checkpoint/restore complete!"
}

# Main
main() {
    local mode="simple"
    local target_pid=""
    
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --pid)
                target_pid="$2"
                shift 2
                ;;
            --simple)
                mode="simple"
                shift
                ;;
            --full)
                mode="full"
                shift
                ;;
            *)
                echo "Usage: $0 [--pid <pid>] [--simple|--full]"
                exit 1
                ;;
        esac
    done
    
    check_prereqs
    
    if [[ -n "$target_pid" ]]; then
        checkpoint_process "$target_pid"
        sleep 2
        restore_process "$target_pid"
    elif [[ "$mode" == "simple" ]]; then
        run_simple_test
    else
        run_simple_test
        # run_full_test uses the simple test process
    fi
}

main "$@"
