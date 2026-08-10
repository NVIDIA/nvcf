#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

#
# NVSNAP Demo: GPU Checkpoint/Restore
#
# This script demonstrates full GPU process checkpoint/restore
# using cuda-checkpoint + CRIU on a host-based GPU workload.
#
# Requirements:
# - NVIDIA Driver 555+
# - cuda-checkpoint binary
# - Forked CRIU (criu-fork or /usr/local/bin/criu)
# - GPU test binary (/app/gpu_loop or compiled from source)
#
# Usage:
#   ./scripts/demo-checkpoint.sh [checkpoint_dir]

set -e

# Configuration
CHECKPOINT_DIR="${1:-/tmp/nvsnap-demo}"
CRIU_BIN="${CRIU_BIN:-criu}"
GPU_APP="${GPU_APP:-/app/gpu_loop}"
DEMO_DURATION=60  # seconds

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
NC='\033[0m'
BOLD='\033[1m'

# Logging
log_header() { echo -e "\n${BOLD}${BLUE}═══════════════════════════════════════════════════════════════${NC}"; echo -e "${BOLD}${BLUE}  $1${NC}"; echo -e "${BOLD}${BLUE}═══════════════════════════════════════════════════════════════${NC}"; }
log_step() { echo -e "${CYAN}[STEP $1]${NC} $2"; }
log_info() { echo -e "${GREEN}[INFO]${NC} $1"; }
log_warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }
log_error() { echo -e "${RED}[ERROR]${NC} $1"; exit 1; }
log_success() { echo -e "${GREEN}${BOLD}[SUCCESS]${NC} $1"; }

# Cleanup function
cleanup() {
    log_info "Cleaning up..."
    pkill -f gpu_loop 2>/dev/null || true
    pkill -f "$GPU_APP" 2>/dev/null || true
}
trap cleanup EXIT

# Banner
clear
echo -e "${BOLD}${GREEN}"
cat << 'EOF'
   ____  ____  _   _  ____ ____  
  / ___|/ ___|/ \ | |/ ___|  _ \ 
 | |  _| |   / _ \| | |   | |_) |
 | |_| | |__/ ___ \ | |___|  _ < 
  \____|\____/_/   \_\____|_| \_\
                                 
  GPU Checkpoint/Restore Demo
EOF
echo -e "${NC}"

log_header "PREREQUISITES CHECK"

# Check NVIDIA driver
log_step 1 "Checking NVIDIA driver..."
if ! nvidia-smi &>/dev/null; then
    log_error "NVIDIA driver not found"
fi
DRIVER_VER=$(nvidia-smi --query-gpu=driver_version --format=csv,noheader | head -1)
log_info "Driver version: $DRIVER_VER"

DRIVER_MAJOR=$(echo "$DRIVER_VER" | cut -d. -f1)
if [ "$DRIVER_MAJOR" -lt 555 ]; then
    log_error "Driver 555+ required (found $DRIVER_VER)"
fi

# Check cuda-checkpoint
log_step 2 "Checking cuda-checkpoint..."
if ! command -v cuda-checkpoint &>/dev/null; then
    log_error "cuda-checkpoint not found in PATH"
fi
log_info "cuda-checkpoint: $(which cuda-checkpoint)"

# Check CRIU
log_step 3 "Checking CRIU..."
if ! command -v $CRIU_BIN &>/dev/null; then
    log_error "CRIU not found: $CRIU_BIN"
fi
CRIU_VER=$($CRIU_BIN --version 2>&1 | head -1)
log_info "CRIU: $CRIU_VER"

# Check GPU app
log_step 4 "Checking GPU test application..."
if [ ! -f "$GPU_APP" ]; then
    log_warn "GPU app not found at $GPU_APP"
    log_info "Compiling from source..."
    
    # Compile if nvcc available
    if command -v nvcc &>/dev/null; then
        cat > /tmp/gpu_demo.cu << 'CUDASRC'
#include <cuda_runtime.h>
#include <stdio.h>
#include <unistd.h>
#include <signal.h>

volatile sig_atomic_t running = 1;
void handler(int s) { running = 0; }

int main() {
    signal(SIGTERM, handler);
    signal(SIGINT, handler);
    
    float *d_data;
    size_t size = 256 * 1024 * 1024;
    cudaError_t err = cudaMalloc(&d_data, size);
    if (err != cudaSuccess) {
        printf("cudaMalloc failed: %s\n", cudaGetErrorString(err));
        return 1;
    }
    printf("Allocated 256MB GPU memory at %p\n", (void*)d_data);
    
    float *h_data = (float*)malloc(size);
    for (size_t i = 0; i < size/sizeof(float); i++) {
        h_data[i] = (float)(i % 12345);
    }
    cudaMemcpy(d_data, h_data, size, cudaMemcpyHostToDevice);
    printf("GPU memory initialized (checksum: 12345)\n");
    free(h_data);
    
    int iter = 0;
    while (running) {
        printf("[%d] GPU running (PID: %d, ptr: %p)\n", iter++, getpid(), (void*)d_data);
        fflush(stdout);
        sleep(3);
    }
    
    // Verify on exit
    float *h_verify = (float*)malloc(size);
    cudaMemcpy(h_verify, d_data, size, cudaMemcpyDeviceToHost);
    int ok = 1;
    for (size_t i = 0; i < 1000 && ok; i++) {
        if (h_verify[i] != (float)(i % 12345)) ok = 0;
    }
    printf("Data integrity: %s\n", ok ? "PASSED" : "FAILED");
    
    cudaFree(d_data);
    free(h_verify);
    return ok ? 0 : 1;
}
CUDASRC
        nvcc -o /tmp/gpu_demo /tmp/gpu_demo.cu -lcuda
        GPU_APP="/tmp/gpu_demo"
        log_info "Compiled: $GPU_APP"
    else
        log_error "nvcc not found and no GPU app available"
    fi
fi

# Setup
log_header "STARTING GPU WORKLOAD"

rm -rf "$CHECKPOINT_DIR"
mkdir -p "$CHECKPOINT_DIR"
log_info "Checkpoint directory: $CHECKPOINT_DIR"

# Start GPU application
log_step 1 "Starting GPU application..."
$GPU_APP > "$CHECKPOINT_DIR/app.log" 2>&1 &
PID=$!
sleep 3

if ! ps -p $PID > /dev/null 2>&1; then
    log_error "GPU app failed to start. Check $CHECKPOINT_DIR/app.log"
fi

log_info "PID: $PID"
GPU_PTR=$(grep "GPU memory at" "$CHECKPOINT_DIR/app.log" | grep -oP '0x[0-9a-f]+' | head -1)
log_info "GPU pointer: $GPU_PTR"

# Show it running
log_step 2 "Workload running..."
sleep 5
tail -3 "$CHECKPOINT_DIR/app.log"

# Checkpoint
log_header "CHECKPOINT"

log_step 1 "Locking GPU state..."
cuda-checkpoint --action lock --pid $PID --timeout 10000
log_success "GPU locked"

log_step 2 "Saving GPU state..."
cuda-checkpoint --action checkpoint --pid $PID
log_success "GPU state saved"

log_step 3 "Saving process state (CRIU)..."
$CRIU_BIN dump -t $PID -D "$CHECKPOINT_DIR" --shell-job -v0 2>&1
IMG_COUNT=$(ls "$CHECKPOINT_DIR"/*.img 2>/dev/null | wc -l)
log_success "Process state saved ($IMG_COUNT images)"

# Process is now dead
log_info "Process $PID terminated (expected)"

# Show checkpoint contents
log_header "CHECKPOINT CONTENTS"
ls -lh "$CHECKPOINT_DIR"/*.img | head -10
echo "..."
du -sh "$CHECKPOINT_DIR"

# Wait a bit
log_header "SIMULATING DOWNTIME"
log_info "Waiting 5 seconds (simulating node maintenance, migration, etc.)..."
sleep 5

# Restore
log_header "RESTORE"

log_step 1 "Restoring process state (CRIU)..."
cd "$CHECKPOINT_DIR"
$CRIU_BIN restore -D "$CHECKPOINT_DIR" --shell-job --restore-detached -v0 2>&1
sleep 1

NEW_PID=$(pgrep -f "$(basename $GPU_APP)" | head -1)
if [ -z "$NEW_PID" ]; then
    log_error "Restore failed - no process found"
fi
log_success "Process restored (PID: $NEW_PID)"

log_step 2 "Restoring GPU state..."
cuda-checkpoint --action restore --pid $NEW_PID
log_success "GPU state restored"

log_step 3 "Unlocking GPU..."
cuda-checkpoint --action unlock --pid $NEW_PID
log_success "GPU unlocked"

# Verify
log_header "VERIFICATION"

GPU_STATE=$(cuda-checkpoint --get-state --pid $NEW_PID 2>/dev/null)
log_info "GPU state: $GPU_STATE"

log_info "Process status:"
ps -p $NEW_PID -o pid,ppid,stat,etime,comm

log_info "Checking output (should continue from checkpoint):"
sleep 6
tail -5 "$CHECKPOINT_DIR/app.log"

# Success
log_header "DEMO COMPLETE"
echo -e "${GREEN}${BOLD}"
cat << 'EOF'
  ✓ GPU workload checkpointed
  ✓ Process state saved
  ✓ Process restored
  ✓ GPU state restored
  ✓ Workload continues execution
EOF
echo -e "${NC}"

log_info "Process $NEW_PID is running. Kill with: kill $NEW_PID"
log_info "Checkpoint saved at: $CHECKPOINT_DIR"

# Keep running for a bit to show it works
log_info "Watching output for 15 seconds..."
for i in {1..5}; do
    sleep 3
    tail -1 "$CHECKPOINT_DIR/app.log"
done

log_success "Demo complete!"
