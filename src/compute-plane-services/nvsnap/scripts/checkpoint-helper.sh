#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# NVSNAP Checkpoint Helper
# Common operations for checkpoint/restore testing

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/config.sh"

# Agent configuration
AGENT_PORT=8081
AGENT_NAMESPACE="nvsnap-system"

# Get agent pod on same node as a given pod
get_agent_for_pod() {
    local pod_name=$1
    local namespace=${2:-nvsnap-system}
    
    local node=$(kubectl get pod "$pod_name" -n "$namespace" -o jsonpath='{.spec.nodeName}')
    kubectl get pods -n "$AGENT_NAMESPACE" -l app=nvsnap-agent \
        --field-selector spec.nodeName="$node" \
        -o jsonpath='{.items[0].metadata.name}'
}

# Create checkpoint
create_checkpoint() {
    local pod_name=$1
    local container_name=${2:-vllm}
    local namespace=${3:-nvsnap-system}
    local checkpoint_id=${4:-$(date +%Y%m%d-%H%M%S)}
    
    local agent_pod=$(get_agent_for_pod "$pod_name" "$namespace")
    echo "Using agent: $agent_pod"
    
    # Port forward in background
    kubectl port-forward -n "$AGENT_NAMESPACE" "$agent_pod" "$AGENT_PORT:$AGENT_PORT" &
    local pf_pid=$!
    sleep 5
    
    # Create checkpoint
    local result=$(curl -s -X POST "http://localhost:$AGENT_PORT/v1/checkpoint" \
        -H "Content-Type: application/json" \
        -d "{\"podName\":\"$pod_name\",\"containerName\":\"$container_name\",\"namespace\":\"$namespace\",\"checkpointId\":\"$checkpoint_id\"}")
    
    kill $pf_pid 2>/dev/null || true
    
    echo "$result"
}

# List checkpoints on agent
list_checkpoints() {
    local agent_pod=$1
    
    kubectl exec -n "$AGENT_NAMESPACE" "$agent_pod" -- \
        ls -la /var/lib/nvsnap/checkpoints/ 2>/dev/null || echo "No checkpoints or agent unavailable"
}

# Clean checkpoints on agent  
clean_checkpoints() {
    local agent_pod=$1
    local pattern=${2:-"*"}
    
    kubectl exec -n "$AGENT_NAMESPACE" "$agent_pod" -- \
        sh -c "rm -rf /var/lib/nvsnap/checkpoints/$pattern" 2>/dev/null || echo "Cleanup failed"
}

# Usage
case "${1:-help}" in
    checkpoint)
        create_checkpoint "${2:-vllm-small}" "${3:-vllm}" "${4:-nvsnap-system}" "${5:-}"
        ;;
    list)
        list_checkpoints "${2:-}"
        ;;
    clean)
        clean_checkpoints "${2:-}" "${3:-*}"
        ;;
    agent-for)
        get_agent_for_pod "${2:-vllm-small}" "${3:-nvsnap-system}"
        ;;
    *)
        echo "Usage: $0 {checkpoint|list|clean|agent-for} [args...]"
        echo ""
        echo "Commands:"
        echo "  checkpoint <pod> [container] [namespace] [id]  - Create checkpoint"
        echo "  list <agent-pod>                               - List checkpoints"
        echo "  clean <agent-pod> [pattern]                    - Clean checkpoints"
        echo "  agent-for <pod> [namespace]                    - Get agent pod for a pod"
        ;;
esac
