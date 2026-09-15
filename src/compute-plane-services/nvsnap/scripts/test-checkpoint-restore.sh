#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

# NVSNAP Checkpoint/Restore Test Script
# Tests the full checkpoint and restore cycle

SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"
TEST_DIR="$PROJECT_ROOT/deploy/k8s"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

log_info() { echo -e "${GREEN}[INFO]${NC} $1"; }
log_warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }
log_error() { echo -e "${RED}[ERROR]${NC} $1"; }

# Configuration
TEST_TYPE="${1:-pytorch}"  # pytorch or vllm
NODE_NAME="${NODE_NAME:-}"
AGENT_PORT="${AGENT_PORT:-8081}"
WAIT_READY="${WAIT_READY:-120}"
WAIT_RESTORE="${WAIT_RESTORE:-60}"

usage() {
    echo "Usage: $0 [pytorch|vllm]"
    echo ""
    echo "Environment variables:"
    echo "  NODE_NAME     - GPU node name (required if not set in YAML)"
    echo "  NODE_IP       - GPU node IP for agent API"
    echo "  AGENT_PORT    - Agent port (default: 8081)"
    echo "  KUBECONFIG    - Path to kubeconfig"
    echo ""
    echo "Examples:"
    echo "  NODE_IP=10.86.2.83 $0 pytorch"
    echo "  NODE_IP=10.86.2.83 $0 vllm"
    exit 1
}

get_node_ip() {
    if [ -n "${NODE_IP:-}" ]; then
        echo "$NODE_IP"
        return
    fi
    
    # Try to get from node
    if [ -n "$NODE_NAME" ]; then
        kubectl get node "$NODE_NAME" -o jsonpath='{.status.addresses[?(@.type=="InternalIP")].address}'
        return
    fi
    
    # Get first GPU node
    kubectl get nodes -l nvidia.com/gpu.present=true -o jsonpath='{.items[0].status.addresses[?(@.type=="InternalIP")].address}'
}

wait_for_pod() {
    local pod_name="$1"
    local timeout="${2:-120}"
    local check_log="${3:-}"
    
    log_info "Waiting for pod $pod_name to be ready..."
    
    local elapsed=0
    while [ $elapsed -lt $timeout ]; do
        local status=$(kubectl get pod "$pod_name" -o jsonpath='{.status.phase}' 2>/dev/null || echo "NotFound")
        
        if [ "$status" = "Running" ]; then
            if [ -n "$check_log" ]; then
                if kubectl logs "$pod_name" 2>/dev/null | grep -q "$check_log"; then
                    log_info "Pod $pod_name is ready (found: $check_log)"
                    return 0
                fi
            else
                log_info "Pod $pod_name is running"
                return 0
            fi
        elif [ "$status" = "Failed" ] || [ "$status" = "Error" ]; then
            log_error "Pod $pod_name failed"
            kubectl logs "$pod_name" 2>&1 | tail -20
            return 1
        fi
        
        sleep 5
        elapsed=$((elapsed + 5))
        echo -n "."
    done
    
    echo ""
    log_error "Timeout waiting for pod $pod_name"
    return 1
}

checkpoint_pod() {
    local namespace="$1"
    local pod_name="$2"
    local container_name="$3"
    local node_ip="$4"
    
    log_info "Checkpointing $pod_name/$container_name..."
    
    local result=$(curl -s -X POST "http://${node_ip}:${AGENT_PORT}/v1/checkpoint" \
        -H "Content-Type: application/json" \
        -d "{\"namespace\":\"$namespace\",\"podName\":\"$pod_name\",\"containerName\":\"$container_name\"}")
    
    local checkpoint_id=$(echo "$result" | python3 -c "import sys,json; print(json.load(sys.stdin).get('checkpointId',''))" 2>/dev/null)
    
    if [ -z "$checkpoint_id" ]; then
        log_error "Checkpoint failed: $result"
        return 1
    fi
    
    local size=$(echo "$result" | python3 -c "import sys,json; print(json.load(sys.stdin).get('checkpointSize',0))" 2>/dev/null)
    local duration=$(echo "$result" | python3 -c "import sys,json; print(json.load(sys.stdin).get('durationSeconds',0))" 2>/dev/null)
    
    log_info "Checkpoint created: $checkpoint_id"
    log_info "  Size: $((size / 1024 / 1024)) MB"
    log_info "  Duration: ${duration}s"
    
    echo "$checkpoint_id"
}

create_restore_pod() {
    local template="$1"
    local checkpoint_id="$2"
    local pod_name="$3"
    
    log_info "Creating restore pod $pod_name from checkpoint $checkpoint_id..."
    
    # Substitute checkpoint path in template
    local checkpoint_path="/var/lib/nvsnap/checkpoints/$checkpoint_id"
    
    sed -e "s|CHECKPOINT_ID_HERE|$checkpoint_id|g" \
        -e "s|path: /var/lib/nvsnap/checkpoints/.*|path: $checkpoint_path|g" \
        -e "s|name: .*-restore|name: $pod_name|g" \
        "$template" | kubectl apply -f -
}

# Main test functions
test_pytorch() {
    local node_ip=$(get_node_ip)
    log_info "Testing PyTorch checkpoint/restore on node $node_ip"
    
    # Clean up
    kubectl delete pod gpu-test gpu-restore --ignore-not-found 2>/dev/null
    sleep 3
    
    # Start test pod
    log_info "Starting PyTorch test pod..."
    if [ -n "$NODE_NAME" ]; then
        sed "s|# nodeName:.*|nodeName: $NODE_NAME|" "$TEST_DIR/01-pytorch-test.yaml" | kubectl apply -f -
    else
        kubectl apply -f "$TEST_DIR/01-pytorch-test.yaml"
    fi
    
    # Wait for ALLOCATED message
    wait_for_pod "gpu-test" "$WAIT_READY" "ALLOCATED"
    
    # Show logs
    echo ""
    log_info "Test pod logs:"
    kubectl logs gpu-test 2>&1 | tail -10
    echo ""
    
    # Checkpoint
    local checkpoint_id=$(checkpoint_pod "default" "gpu-test" "gpu" "$node_ip")
    
    # Delete original
    log_info "Deleting original pod..."
    kubectl delete pod gpu-test --wait=false
    sleep 3
    
    # Restore
    create_restore_pod "$TEST_DIR/02-restore-pod.yaml" "$checkpoint_id" "gpu-restore"
    
    # Wait for restore
    sleep 10
    wait_for_pod "gpu-restore" "$WAIT_RESTORE"
    
    # Show results
    echo ""
    log_info "Restore pod logs:"
    kubectl logs gpu-restore 2>&1 | tail -20
    
    # Verify
    if kubectl logs gpu-restore 2>&1 | grep -q "running"; then
        echo ""
        log_info "✓ PyTorch checkpoint/restore test PASSED"
        return 0
    else
        echo ""
        log_error "✗ PyTorch checkpoint/restore test FAILED"
        return 1
    fi
}

test_vllm() {
    local node_ip=$(get_node_ip)
    log_info "Testing vLLM checkpoint/restore on node $node_ip"
    
    # Clean up
    kubectl delete pod vllm-small vllm-small-restored -n nvsnap-system --ignore-not-found 2>/dev/null
    sleep 3
    
    # Start vLLM pod
    log_info "Starting vLLM test pod (model download may take a few minutes)..."
    kubectl apply -f "$TEST_DIR/vllm-small.yaml"
    
    # Wait for vLLM to be ready (longer timeout for model download)
    kubectl wait --for=condition=ready pod/vllm-small -n nvsnap-system --timeout=300s || log_warn "Pod not ready yet"
    sleep 10  # Extra time for vLLM to fully initialize
    
    # Test API
    log_info "Testing vLLM API..."
    if curl -s "http://${node_ip}:8000/v1/models" | grep -q "TinyLlama"; then
        log_info "vLLM API is responding"
    else
        log_warn "vLLM API not responding yet, continuing..."
    fi

    # Checkpoint
    local checkpoint_id=$(checkpoint_pod "nvsnap-system" "vllm-small" "vllm" "$node_ip")
    
    # Delete original
    log_info "Deleting original pod..."
    kubectl delete pod vllm-small -n nvsnap-system --wait=false
    sleep 3

    # Update restore manifest with checkpoint ID
    log_info "Creating restore pod with checkpoint $checkpoint_id..."
    sed 's|value: "vllm-small__nvsnap-system__[0-9-]*"|value: "'"$checkpoint_id"'"|' \
        "$TEST_DIR/vllm-small-restore.yaml" | kubectl apply -f -

    # Wait for restore
    sleep 15
    kubectl wait --for=condition=ready pod/vllm-small-restored -n nvsnap-system --timeout=120s || log_warn "Restore pod not ready yet"

    # Show results
    echo ""
    log_info "Restore pod logs:"
    kubectl logs vllm-small-restored -n nvsnap-system 2>&1 | tail -30

    # Test restored API
    sleep 5
    log_info "Testing restored vLLM API..."
    if curl -s "http://${node_ip}:8000/v1/models" | grep -q "TinyLlama"; then
        echo ""
        log_info "✓ vLLM checkpoint/restore test PASSED - API responding!"
        return 0
    else
        echo ""
        log_warn "vLLM API not responding after restore (process may still be initializing)"
        log_info "Check: kubectl logs vllm-small-restored -n nvsnap-system"
        return 1
    fi
}

# Main
case "${TEST_TYPE}" in
    pytorch)
        test_pytorch
        ;;
    vllm)
        test_vllm
        ;;
    *)
        usage
        ;;
esac
