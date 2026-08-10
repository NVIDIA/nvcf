#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Test checkpoint/restore workflow for vLLM container
set -euo pipefail

# Configuration
KUBECONFIG="${KUBECONFIG:?KUBECONFIG must point at your cluster kubeconfig — export it before running this script}"
NAMESPACE="${NAMESPACE:-nvsnap-system}"
POD_NAME="${POD_NAME:-vllm-small}"
AGENT_NS="${AGENT_NS:-nvsnap-system}"
LOCAL_PORT="${LOCAL_PORT:-8081}"

export KUBECONFIG

echo "=== NVSNAP Checkpoint Test ==="
echo "Namespace: $NAMESPACE"
echo "Pod: $POD_NAME"
echo ""

# Find the agent pod on the same node as the target pod
get_agent_pod() {
    local node=$(kubectl get pod -n "$NAMESPACE" "$POD_NAME" -o jsonpath='{.spec.nodeName}')
    if [ -z "$node" ]; then
        echo "ERROR: Pod $POD_NAME not found in namespace $NAMESPACE" >&2
        exit 1
    fi
    echo "Target pod is on node: $node" >&2
    
    local agent=$(kubectl get pods -n "$AGENT_NS" -l app=nvsnap-agent -o jsonpath="{.items[?(@.spec.nodeName=='$node')].metadata.name}")
    if [ -z "$agent" ]; then
        echo "ERROR: No nvsnap-agent found on node $node" >&2
        exit 1
    fi
    echo "$agent"
}

# Kill any existing port-forward
cleanup_port_forward() {
    pkill -f "kubectl port-forward.*$LOCAL_PORT:" 2>/dev/null || true
    sleep 1
}

# Start port-forward to agent
start_port_forward() {
    local agent_pod=$1
    echo "Starting port-forward to $agent_pod..."
    kubectl port-forward -n "$AGENT_NS" "$agent_pod" "$LOCAL_PORT:8081" &
    PF_PID=$!
    sleep 3
    
    # Verify it's working
    if ! curl -s "http://localhost:$LOCAL_PORT/health" > /dev/null; then
        echo "ERROR: Port-forward failed" >&2
        exit 1
    fi
    echo "Port-forward ready (PID: $PF_PID)"
}

# API calls
api_health() {
    curl -s "http://localhost:$LOCAL_PORT/health" | jq .
}

api_checkpoint() {
    echo "Creating checkpoint for $NAMESPACE/$POD_NAME..."
    curl -s --max-time 300 -X POST "http://localhost:$LOCAL_PORT/v1/checkpoint" \
        -H "Content-Type: application/json" \
        -d "{\"podName\": \"$POD_NAME\", \"namespace\": \"$NAMESPACE\"}" | jq .
}

api_list_checkpoints() {
    curl -s "http://localhost:$LOCAL_PORT/v1/checkpoints" | jq .
}

api_list_containers() {
    curl -s "http://localhost:$LOCAL_PORT/v1/containers" | jq "[.[] | select(.labels[\"io.kubernetes.pod.name\"] == \"$POD_NAME\")]"
}

# Commands
case "${1:-help}" in
    health)
        AGENT_POD=$(get_agent_pod)
        cleanup_port_forward
        start_port_forward "$AGENT_POD"
        api_health
        ;;
    checkpoint)
        AGENT_POD=$(get_agent_pod)
        cleanup_port_forward
        start_port_forward "$AGENT_POD"
        api_checkpoint
        ;;
    list)
        AGENT_POD=$(get_agent_pod)
        cleanup_port_forward
        start_port_forward "$AGENT_POD"
        api_list_checkpoints
        ;;
    containers)
        AGENT_POD=$(get_agent_pod)
        cleanup_port_forward
        start_port_forward "$AGENT_POD"
        api_list_containers
        ;;
    clean)
        AGENT_POD=$(get_agent_pod)
        echo "Cleaning old checkpoints on agent $AGENT_POD..."
        kubectl exec -n "$AGENT_NS" "$AGENT_POD" -- bash -c 'rm -rf /var/lib/nvsnap/checkpoints/vllm-small__*'
        echo "Done."
        ;;
    status)
        echo "=== Pods ==="
        kubectl get pods -n "$NAMESPACE" -o wide | grep -E "(NAME|$POD_NAME)"
        echo ""
        echo "=== Agent Pods ==="
        kubectl get pods -n "$AGENT_NS" -l app=nvsnap-agent -o wide
        echo ""
        echo "=== Agent Version ==="
        kubectl get daemonset -n "$AGENT_NS" nvsnap-agent -o jsonpath='{.spec.template.spec.containers[0].image}'
        echo ""
        ;;
    help|*)
        echo "Usage: $0 <command>"
        echo ""
        echo "Commands:"
        echo "  health      - Check agent health"
        echo "  checkpoint  - Create checkpoint of $POD_NAME"
        echo "  list        - List existing checkpoints"
        echo "  containers  - List containers (filtered to $POD_NAME)"
        echo "  clean       - Delete old checkpoints"
        echo "  status      - Show pod and agent status"
        echo ""
        echo "Environment variables:"
        echo "  KUBECONFIG  - Path to kubeconfig (default: $KUBECONFIG)"
        echo "  NAMESPACE   - Pod namespace (default: $NAMESPACE)"
        echo "  POD_NAME    - Pod to checkpoint (default: $POD_NAME)"
        ;;
esac

# Cleanup port-forward on exit
trap 'cleanup_port_forward' EXIT
