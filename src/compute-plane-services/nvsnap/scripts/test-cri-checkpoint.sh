#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# NVSNAP CRI Checkpoint Test Script
#
# Tests CRI container checkpoint on Kubernetes.
# Requires ContainerCheckpoint feature gate to be enabled.
#
# Usage:
#   ./test-cri-checkpoint.sh [--kubeconfig <path>] [--namespace <ns>]

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
KUBECONFIG="${KUBECONFIG:-$HOME/.kube/config}"
NAMESPACE="${NAMESPACE:-default}"
CHECKPOINT_DIR="${CHECKPOINT_DIR:-/tmp/checkpoints}"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

log() { echo -e "${GREEN}[INFO]${NC} $1"; }
warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }
error() { echo -e "${RED}[ERROR]${NC} $1"; exit 1; }

# Parse arguments
while [[ $# -gt 0 ]]; do
    case "$1" in
        --kubeconfig)
            KUBECONFIG="$2"
            shift 2
            ;;
        --namespace|-n)
            NAMESPACE="$2"
            shift 2
            ;;
        *)
            shift
            ;;
    esac
done

export KUBECONFIG

# Check prerequisites
check_prereqs() {
    log "Checking prerequisites..."
    
    if ! kubectl cluster-info &>/dev/null; then
        error "Cannot connect to Kubernetes cluster"
    fi
    
    log "Connected to cluster"
}

# Create test pod
create_test_pod() {
    log "Creating test pod..."
    
    kubectl delete pod checkpoint-test -n "$NAMESPACE" 2>/dev/null || true
    
    cat << 'EOF' | kubectl apply -n "$NAMESPACE" -f -
apiVersion: v1
kind: Pod
metadata:
  name: checkpoint-test
spec:
  containers:
  - name: busybox
    image: busybox
    command: ["sh", "-c", "i=0; while true; do echo \"iteration $i\"; i=$((i+1)); sleep 5; done"]
EOF

    log "Waiting for pod to be ready..."
    kubectl wait --for=condition=Ready pod/checkpoint-test -n "$NAMESPACE" --timeout=60s
    
    log "Test pod created"
}

# Get container info
get_container_info() {
    local pod=$1
    local ns=$2
    
    CONTAINER_ID=$(kubectl get pod "$pod" -n "$ns" -o jsonpath='{.status.containerStatuses[0].containerID}' | sed 's|containerd://||')
    NODE_IP=$(kubectl get pod "$pod" -n "$ns" -o jsonpath='{.status.hostIP}')
    NODE_NAME=$(kubectl get pod "$pod" -n "$ns" -o jsonpath='{.spec.nodeName}')
    
    log "Container ID: $CONTAINER_ID"
    log "Node: $NODE_NAME ($NODE_IP)"
}

# SSH to node and run command
ssh_node() {
    local node_ip=$1
    shift
    local cmd="$@"
    
    # Try direct SSH first
    if ssh -o BatchMode=yes -o ConnectTimeout=5 "$node_ip" "echo test" &>/dev/null; then
        ssh "$node_ip" "$cmd"
    else
        # Fall back to kubectl debug node
        kubectl debug node/"$NODE_NAME" -it --image=busybox -- sh -c "$cmd"
    fi
}

# Checkpoint container
checkpoint_container() {
    log "Checkpointing container..."
    
    get_container_info "checkpoint-test" "$NAMESPACE"
    
    local checkpoint_file="$CHECKPOINT_DIR/checkpoint-$(date +%Y%m%d-%H%M%S).tar"
    
    log "Creating checkpoint at $checkpoint_file..."
    
    # This requires access to the node
    # In a real scenario, the NVSNAP agent would do this
    cat << EOF
To checkpoint this container, run on node $NODE_NAME:

sudo mkdir -p $CHECKPOINT_DIR
sudo crictl checkpoint --export $checkpoint_file $CONTAINER_ID

The checkpoint will be saved to: $checkpoint_file
EOF
    
    # If we can SSH, try to do it
    if command -v sshpass &>/dev/null && [[ -n "${SSH_PASS:-}" ]]; then
        log "Attempting checkpoint via SSH..."
        sshpass -p "$SSH_PASS" ssh -o StrictHostKeyChecking=no "$SSH_USER@$NODE_IP" \
            "echo $SSH_PASS | sudo -S bash -c 'mkdir -p $CHECKPOINT_DIR && crictl checkpoint --export $checkpoint_file $CONTAINER_ID'" 2>&1
        
        log "Verifying checkpoint..."
        sshpass -p "$SSH_PASS" ssh -o StrictHostKeyChecking=no "$SSH_USER@$NODE_IP" \
            "ls -la $checkpoint_file" 2>&1
        
        log "✅ Checkpoint created successfully!"
    else
        warn "Cannot SSH to node. Set SSH_USER and SSH_PASS environment variables."
        log "Showing current pod logs instead:"
        kubectl logs checkpoint-test -n "$NAMESPACE" --tail=5
    fi
}

# Cleanup
cleanup() {
    log "Cleaning up..."
    kubectl delete pod checkpoint-test -n "$NAMESPACE" 2>/dev/null || true
}

# Main
main() {
    check_prereqs
    create_test_pod
    
    # Show pod is running
    log "Pod status:"
    kubectl get pod checkpoint-test -n "$NAMESPACE"
    
    log "Pod logs:"
    kubectl logs checkpoint-test -n "$NAMESPACE" --tail=5
    
    # Checkpoint
    checkpoint_container
    
    # Ask user before cleanup
    read -p "Delete test pod? [y/N] " -n 1 -r
    echo
    if [[ $REPLY =~ ^[Yy]$ ]]; then
        cleanup
    fi
}

main "$@"
