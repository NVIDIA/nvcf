#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Milestone 2 Validation Script
# Validates: CRIU integration, GPU checkpoint/restore
# REQUIRES: GPU node with NVIDIA drivers

set -euo pipefail

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

PASSED=0
FAILED=0
SKIPPED=0

# Test artifacts directory
TEST_DIR="${TEST_DIR:-/tmp/nvsnap-m2-test}"
CHECKPOINT_DIR="$TEST_DIR/checkpoints"

log_info() { echo -e "${BLUE}[INFO]${NC} $1"; }
log_pass() { echo -e "${GREEN}[PASS]${NC} $1"; ((PASSED++)); }
log_fail() { echo -e "${RED}[FAIL]${NC} $1"; ((FAILED++)); }
log_skip() { echo -e "${YELLOW}[SKIP]${NC} $1"; ((SKIPPED++)); }
log_header() {
    echo ""
    echo -e "${BLUE}========================================${NC}"
    echo -e "${BLUE} $1${NC}"
    echo -e "${BLUE}========================================${NC}"
}

cleanup() {
    log_info "Cleaning up test artifacts..."
    rm -rf "$TEST_DIR"
    # Kill any test processes
    pkill -f "nvsnap-test-" 2>/dev/null || true
    # Clean up test pods
    kubectl delete pod -l nvsnap.io/test=milestone-2 --ignore-not-found 2>/dev/null || true
}

trap cleanup EXIT

setup() {
    log_info "Setting up test environment..."
    mkdir -p "$TEST_DIR" "$CHECKPOINT_DIR"
    
    # Build test binaries
    make build
}

# Check prerequisites
check_prerequisites() {
    log_header "Checking Prerequisites"
    
    # Check CRIU
    if command -v criu &> /dev/null; then
        CRIU_VERSION=$(criu --version | head -1)
        log_pass "CRIU installed: $CRIU_VERSION"
    else
        log_fail "CRIU not installed"
        echo "  Install with: apt-get install criu"
        return 1
    fi
    
    # Check NVIDIA driver
    if command -v nvidia-smi &> /dev/null; then
        DRIVER_VERSION=$(nvidia-smi --query-gpu=driver_version --format=csv,noheader | head -1)
        log_pass "NVIDIA driver: $DRIVER_VERSION"
    else
        log_fail "NVIDIA driver not installed"
        return 1
    fi
    
    # Check GPUs
    GPU_COUNT=$(nvidia-smi --query-gpu=name --format=csv,noheader | wc -l)
    if [[ $GPU_COUNT -gt 0 ]]; then
        GPU_MODEL=$(nvidia-smi --query-gpu=name --format=csv,noheader | head -1)
        log_pass "GPUs detected: $GPU_COUNT x $GPU_MODEL"
    else
        log_fail "No GPUs detected"
        return 1
    fi
    
    # Check CUDA
    if command -v nvcc &> /dev/null; then
        CUDA_VERSION=$(nvcc --version | grep "release" | awk '{print $5}' | sed 's/,//')
        log_pass "CUDA installed: $CUDA_VERSION"
    else
        log_skip "CUDA toolkit not in PATH (may still work with container)"
    fi
    
    # Check kubectl access
    if kubectl cluster-info &> /dev/null; then
        log_pass "Kubernetes cluster accessible"
    else
        log_skip "No Kubernetes cluster (some tests will be skipped)"
    fi
}

# Test 1: GPU Detection
test_gpu_detection() {
    log_header "Test 1: GPU Detection"
    
    log_info "Testing NVML-based GPU detection..."
    
    if ./bin/nvsnap-agent detect-gpus 2>&1; then
        # Verify output contains expected fields
        OUTPUT=$(./bin/nvsnap-agent detect-gpus --format=json 2>&1)
        
        if echo "$OUTPUT" | jq -e '.gpus | length > 0' &> /dev/null; then
            GPU_COUNT=$(echo "$OUTPUT" | jq '.gpus | length')
            log_pass "Detected $GPU_COUNT GPU(s) via NVML"
        else
            log_fail "GPU detection returned empty results"
        fi
    else
        log_fail "GPU detection command failed"
    fi
}

# Test 2: Simple Process Checkpoint (no GPU)
test_simple_checkpoint() {
    log_header "Test 2: Simple Process Checkpoint (No GPU)"
    
    log_info "Starting test process..."
    
    # Start a simple counter process
    COUNTER_FILE="$TEST_DIR/counter.txt"
    (
        i=0
        while true; do
            echo $i > "$COUNTER_FILE"
            ((i++))
            sleep 0.1
        done
    ) &
    TEST_PID=$!
    
    # Wait for counter to reach at least 50
    log_info "Waiting for counter to reach 50..."
    while [[ $(cat "$COUNTER_FILE" 2>/dev/null || echo 0) -lt 50 ]]; do
        sleep 0.1
    done
    
    CHECKPOINT_VALUE=$(cat "$COUNTER_FILE")
    log_info "Counter at checkpoint: $CHECKPOINT_VALUE"
    
    # Create checkpoint
    log_info "Creating checkpoint..."
    CKPT_PATH="$CHECKPOINT_DIR/simple-ckpt"
    
    if ./bin/nvsnap-agent checkpoint-process \
        --pid=$TEST_PID \
        --output="$CKPT_PATH" \
        --leave-running=false 2>&1; then
        log_pass "Checkpoint created successfully"
    else
        log_fail "Checkpoint creation failed"
        kill $TEST_PID 2>/dev/null || true
        return
    fi
    
    # Verify checkpoint files exist
    if [[ -d "$CKPT_PATH" ]] && [[ -f "$CKPT_PATH/core-$TEST_PID.img" ]]; then
        log_pass "Checkpoint files created"
    else
        log_fail "Checkpoint files not found"
        return
    fi
    
    # Restore process
    log_info "Restoring process..."
    if ./bin/nvsnap-agent restore-process \
        --checkpoint="$CKPT_PATH" 2>&1; then
        
        # Wait a moment for process to resume
        sleep 1
        
        # Check that counter continued from checkpoint
        RESTORED_VALUE=$(cat "$COUNTER_FILE")
        if [[ $RESTORED_VALUE -gt $CHECKPOINT_VALUE ]]; then
            log_pass "Process restored and continued (counter: $CHECKPOINT_VALUE -> $RESTORED_VALUE)"
        else
            log_fail "Process did not continue after restore"
        fi
        
        # Cleanup
        pkill -f "while true" 2>/dev/null || true
    else
        log_fail "Process restore failed"
    fi
}

# Test 3: GPU Memory Dump
test_gpu_memory_dump() {
    log_header "Test 3: GPU Memory Dump"
    
    log_info "Starting GPU memory allocation test..."
    
    # Deploy a test pod that allocates GPU memory
    cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: gpu-mem-test
  labels:
    nvsnap.io/test: milestone-2
spec:
  restartPolicy: Never
  containers:
  - name: gpu-test
    image: nvcr.io/nvidia/pytorch:23.10-py3
    command: ["python3", "-c"]
    args:
    - |
      import torch
      import time
      import os
      
      # Allocate 1GB of GPU memory
      x = torch.randn(256, 1024, 1024, device='cuda')
      print(f"Allocated tensor with sum: {x.sum().item()}")
      
      # Write sum to file for verification
      with open('/tmp/gpu-sum.txt', 'w') as f:
          f.write(str(x.sum().item()))
      
      # Keep running
      while True:
          time.sleep(1)
    resources:
      limits:
        nvidia.com/gpu: 1
EOF
    
    # Wait for pod to be running
    log_info "Waiting for test pod..."
    if kubectl wait --for=condition=ready pod/gpu-mem-test --timeout=120s 2>&1; then
        log_pass "Test pod is running"
    else
        log_fail "Test pod failed to start"
        kubectl describe pod gpu-mem-test
        return
    fi
    
    sleep 5  # Let the tensor be allocated
    
    # Get container ID
    CONTAINER_ID=$(kubectl get pod gpu-mem-test -o jsonpath='{.status.containerStatuses[0].containerID}' | sed 's/containerd:\/\///')
    
    log_info "Container ID: $CONTAINER_ID"
    
    # Get original GPU sum
    ORIGINAL_SUM=$(kubectl exec gpu-mem-test -- cat /tmp/gpu-sum.txt)
    log_info "Original GPU tensor sum: $ORIGINAL_SUM"
    
    # Dump GPU memory
    log_info "Dumping GPU memory..."
    GPU_DUMP="$CHECKPOINT_DIR/gpu-dump"
    
    if ./bin/nvsnap-agent dump-gpu-memory \
        --container-id="$CONTAINER_ID" \
        --output="$GPU_DUMP" 2>&1; then
        
        # Check dump size
        DUMP_SIZE=$(du -sh "$GPU_DUMP" | awk '{print $1}')
        log_pass "GPU memory dumped: $DUMP_SIZE"
    else
        log_fail "GPU memory dump failed"
    fi
}

# Test 4: Full Container Checkpoint (CPU + GPU)
test_full_container_checkpoint() {
    log_header "Test 4: Full Container Checkpoint"
    
    # Reuse the pod from test 3 or create new one
    if ! kubectl get pod gpu-mem-test &> /dev/null; then
        log_skip "GPU test pod not available"
        return
    fi
    
    CONTAINER_ID=$(kubectl get pod gpu-mem-test -o jsonpath='{.status.containerStatuses[0].containerID}' | sed 's/containerd:\/\///')
    ORIGINAL_SUM=$(kubectl exec gpu-mem-test -- cat /tmp/gpu-sum.txt 2>/dev/null || echo "unknown")
    
    log_info "Creating full container checkpoint..."
    FULL_CKPT="$CHECKPOINT_DIR/full-ckpt"
    
    if ./bin/nvsnap-agent checkpoint \
        --container-id="$CONTAINER_ID" \
        --output="$FULL_CKPT" \
        --include-gpu=true \
        --storage-type=local 2>&1; then
        
        log_pass "Full checkpoint created"
        
        # Verify checkpoint contents
        if [[ -d "$FULL_CKPT/cpu" ]] && [[ -d "$FULL_CKPT/gpu" ]]; then
            CPU_SIZE=$(du -sh "$FULL_CKPT/cpu" | awk '{print $1}')
            GPU_SIZE=$(du -sh "$FULL_CKPT/gpu" | awk '{print $1}')
            log_info "Checkpoint sizes - CPU: $CPU_SIZE, GPU: $GPU_SIZE"
            log_pass "Checkpoint structure verified"
        else
            log_fail "Checkpoint structure invalid"
        fi
        
        # Save checkpoint info for restore test
        echo "$ORIGINAL_SUM" > "$TEST_DIR/original-sum.txt"
        echo "$FULL_CKPT" > "$TEST_DIR/checkpoint-path.txt"
    else
        log_fail "Full checkpoint creation failed"
    fi
}

# Test 5: Container Restore
test_container_restore() {
    log_header "Test 5: Container Restore"
    
    FULL_CKPT=$(cat "$TEST_DIR/checkpoint-path.txt" 2>/dev/null || echo "")
    ORIGINAL_SUM=$(cat "$TEST_DIR/original-sum.txt" 2>/dev/null || echo "")
    
    if [[ -z "$FULL_CKPT" ]] || [[ ! -d "$FULL_CKPT" ]]; then
        log_skip "No checkpoint available from previous test"
        return
    fi
    
    # Delete original pod
    log_info "Deleting original pod..."
    kubectl delete pod gpu-mem-test --wait=true
    
    # Restore container
    log_info "Restoring container from checkpoint..."
    
    if ./bin/nvsnap-agent restore \
        --checkpoint="$FULL_CKPT" \
        --pod-name="gpu-mem-restored" \
        --namespace="default" 2>&1; then
        
        log_pass "Restore command completed"
        
        # Wait for restored pod
        if kubectl wait --for=condition=ready pod/gpu-mem-restored --timeout=120s 2>&1; then
            log_pass "Restored pod is running"
            
            # Verify GPU state
            sleep 3
            RESTORED_SUM=$(kubectl exec gpu-mem-restored -- cat /tmp/gpu-sum.txt 2>/dev/null || echo "failed")
            
            if [[ "$RESTORED_SUM" == "$ORIGINAL_SUM" ]]; then
                log_pass "GPU state restored correctly (sum: $RESTORED_SUM)"
            else
                log_fail "GPU state mismatch (original: $ORIGINAL_SUM, restored: $RESTORED_SUM)"
            fi
        else
            log_fail "Restored pod failed to become ready"
        fi
    else
        log_fail "Container restore failed"
    fi
}

# Test 6: PyTorch Training Checkpoint
test_pytorch_training() {
    log_header "Test 6: PyTorch Training Checkpoint/Restore"
    
    log_info "Deploying PyTorch training job..."
    
    cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: pytorch-training
  labels:
    nvsnap.io/test: milestone-2
spec:
  restartPolicy: Never
  containers:
  - name: trainer
    image: nvcr.io/nvidia/pytorch:23.10-py3
    command: ["python3", "-c"]
    args:
    - |
      import torch
      import torch.nn as nn
      import time
      import os
      
      class SimpleNet(nn.Module):
          def __init__(self):
              super().__init__()
              self.fc = nn.Linear(1000, 1000)
          def forward(self, x):
              return self.fc(x)
      
      model = SimpleNet().cuda()
      optimizer = torch.optim.SGD(model.parameters(), lr=0.01)
      
      for epoch in range(10000):
          x = torch.randn(32, 1000, device='cuda')
          y = model(x)
          loss = y.sum()
          loss.backward()
          optimizer.step()
          optimizer.zero_grad()
          
          # Save epoch for verification
          with open('/tmp/epoch.txt', 'w') as f:
              f.write(str(epoch))
          
          # Save loss for verification  
          with open('/tmp/loss.txt', 'w') as f:
              f.write(f"{loss.item():.6f}")
          
          print(f"Epoch {epoch}, Loss: {loss.item():.4f}")
          time.sleep(0.5)
    resources:
      limits:
        nvidia.com/gpu: 1
EOF
    
    # Wait for training to start
    log_info "Waiting for training to start..."
    kubectl wait --for=condition=ready pod/pytorch-training --timeout=120s
    
    # Wait for epoch 20
    log_info "Waiting for training to reach epoch 20..."
    while true; do
        EPOCH=$(kubectl exec pytorch-training -- cat /tmp/epoch.txt 2>/dev/null || echo 0)
        if [[ $EPOCH -ge 20 ]]; then
            break
        fi
        sleep 1
    done
    
    CHECKPOINT_EPOCH=$EPOCH
    CHECKPOINT_LOSS=$(kubectl exec pytorch-training -- cat /tmp/loss.txt 2>/dev/null)
    log_info "Checkpointing at epoch $CHECKPOINT_EPOCH (loss: $CHECKPOINT_LOSS)"
    
    # Create checkpoint
    CONTAINER_ID=$(kubectl get pod pytorch-training -o jsonpath='{.status.containerStatuses[0].containerID}' | sed 's/containerd:\/\///')
    TRAINING_CKPT="$CHECKPOINT_DIR/training-ckpt"
    
    if ./bin/nvsnap-agent checkpoint \
        --container-id="$CONTAINER_ID" \
        --output="$TRAINING_CKPT" \
        --include-gpu=true 2>&1; then
        
        log_pass "Training checkpoint created at epoch $CHECKPOINT_EPOCH"
    else
        log_fail "Training checkpoint failed"
        return
    fi
    
    # Delete and restore
    kubectl delete pod pytorch-training --wait=true
    
    log_info "Restoring training from checkpoint..."
    if ./bin/nvsnap-agent restore \
        --checkpoint="$TRAINING_CKPT" \
        --pod-name="pytorch-training-restored" 2>&1; then
        
        kubectl wait --for=condition=ready pod/pytorch-training-restored --timeout=120s
        
        # Wait and check epoch
        sleep 5
        RESTORED_EPOCH=$(kubectl exec pytorch-training-restored -- cat /tmp/epoch.txt 2>/dev/null || echo 0)
        
        if [[ $RESTORED_EPOCH -gt $CHECKPOINT_EPOCH ]]; then
            log_pass "Training resumed from epoch $CHECKPOINT_EPOCH, now at $RESTORED_EPOCH"
        else
            log_fail "Training did not resume correctly (checkpoint: $CHECKPOINT_EPOCH, restored: $RESTORED_EPOCH)"
        fi
    else
        log_fail "Training restore failed"
    fi
}

# Test 7: Cross-node Restore
test_cross_node_restore() {
    log_header "Test 7: Cross-Node Restore"
    
    # Check if we have multiple GPU nodes
    GPU_NODES=$(kubectl get nodes -l nvidia.com/gpu.present=true -o name | wc -l)
    
    if [[ $GPU_NODES -lt 2 ]]; then
        log_skip "Less than 2 GPU nodes available (have $GPU_NODES)"
        return
    fi
    
    NODE1=$(kubectl get nodes -l nvidia.com/gpu.present=true -o name | head -1 | sed 's/node\///')
    NODE2=$(kubectl get nodes -l nvidia.com/gpu.present=true -o name | tail -1 | sed 's/node\///')
    
    log_info "Testing cross-node restore: $NODE1 -> $NODE2"
    
    # Create pod on node1
    cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: cross-node-test
  labels:
    nvsnap.io/test: milestone-2
spec:
  nodeName: $NODE1
  restartPolicy: Never
  containers:
  - name: test
    image: nvcr.io/nvidia/pytorch:23.10-py3
    command: ["python3", "-c"]
    args:
    - |
      import torch
      x = torch.randn(1000, 1000, device='cuda')
      sum_val = x.sum().item()
      with open('/tmp/sum.txt', 'w') as f:
          f.write(str(sum_val))
      import time
      while True:
          time.sleep(1)
    resources:
      limits:
        nvidia.com/gpu: 1
EOF
    
    kubectl wait --for=condition=ready pod/cross-node-test --timeout=120s
    sleep 3
    
    ORIGINAL_SUM=$(kubectl exec cross-node-test -- cat /tmp/sum.txt)
    log_info "Original sum: $ORIGINAL_SUM (on $NODE1)"
    
    # Checkpoint
    CONTAINER_ID=$(kubectl get pod cross-node-test -o jsonpath='{.status.containerStatuses[0].containerID}' | sed 's/containerd:\/\///')
    CROSS_CKPT="$CHECKPOINT_DIR/cross-node-ckpt"
    
    ./bin/nvsnap-agent checkpoint \
        --container-id="$CONTAINER_ID" \
        --output="$CROSS_CKPT" \
        --include-gpu=true
    
    kubectl delete pod cross-node-test --wait=true
    
    # Restore on node2
    if ./bin/nvsnap-agent restore \
        --checkpoint="$CROSS_CKPT" \
        --pod-name="cross-node-restored" \
        --node-name="$NODE2" 2>&1; then
        
        kubectl wait --for=condition=ready pod/cross-node-restored --timeout=120s
        
        ACTUAL_NODE=$(kubectl get pod cross-node-restored -o jsonpath='{.spec.nodeName}')
        RESTORED_SUM=$(kubectl exec cross-node-restored -- cat /tmp/sum.txt)
        
        if [[ "$ACTUAL_NODE" == "$NODE2" ]] && [[ "$RESTORED_SUM" == "$ORIGINAL_SUM" ]]; then
            log_pass "Cross-node restore successful ($NODE1 -> $NODE2)"
        else
            log_fail "Cross-node restore verification failed"
        fi
    else
        log_fail "Cross-node restore failed"
    fi
}

# Print summary
print_summary() {
    log_header "Milestone 2 Validation Summary"
    
    TOTAL=$((PASSED + FAILED + SKIPPED))
    
    echo ""
    echo -e "  ${GREEN}Passed:${NC}  $PASSED"
    echo -e "  ${RED}Failed:${NC}  $FAILED"
    echo -e "  ${YELLOW}Skipped:${NC} $SKIPPED"
    echo -e "  Total:   $TOTAL"
    echo ""
    
    if [[ $FAILED -gt 0 ]]; then
        echo -e "${RED}========================================${NC}"
        echo -e "${RED} MILESTONE 2 VALIDATION FAILED${NC}"
        echo -e "${RED}========================================${NC}"
        exit 1
    else
        echo -e "${GREEN}========================================${NC}"
        echo -e "${GREEN} MILESTONE 2 VALIDATION PASSED${NC}"
        echo -e "${GREEN}========================================${NC}"
        exit 0
    fi
}

# Main
main() {
    echo ""
    echo -e "${BLUE}╔════════════════════════════════════════════════════════════╗${NC}"
    echo -e "${BLUE}║          NVSNAP Milestone 2 Validation                      ║${NC}"
    echo -e "${BLUE}║          Core Engine: GPU Checkpoint/Restore               ║${NC}"
    echo -e "${BLUE}╚════════════════════════════════════════════════════════════╝${NC}"
    echo ""
    
    setup
    
    if ! check_prerequisites; then
        echo -e "${RED}Prerequisites not met. Cannot proceed with validation.${NC}"
        exit 1
    fi
    
    test_gpu_detection
    test_simple_checkpoint
    test_gpu_memory_dump
    test_full_container_checkpoint
    test_container_restore
    test_pytorch_training
    test_cross_node_restore
    
    print_summary
}

main "$@"
