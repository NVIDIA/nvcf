#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# NVSNAP Checkpoint Helper
#
# Usage:
#   ./checkpoint.sh create <pod-name> [container-name] [namespace]
#   ./checkpoint.sh list [namespace]
#   ./checkpoint.sh delete <checkpoint-id> [namespace]
#   ./checkpoint.sh cleanup [namespace]  # Remove all checkpoints on node
#
# Examples:
#   ./checkpoint.sh create vllm-small
#   ./checkpoint.sh create vllm-small vllm nvsnap-system
#   ./checkpoint.sh list
#   ./checkpoint.sh cleanup

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/config.sh" 2>/dev/null || true

# Agent API port - IMPORTANT: Agent listens on 8081, not 8080!
AGENT_PORT="${AGENT_PORT:-8081}"
NAMESPACE="${NAMESPACE:-nvsnap-system}"

usage() {
    echo "Usage: $0 <command> [args...]"
    echo ""
    echo "Commands:"
    echo "  create <pod-name> [container-name] [namespace]  - Create checkpoint"
    echo "  list [namespace]                                 - List checkpoints"
    echo "  delete <checkpoint-id> [namespace]              - Delete checkpoint"
    echo "  cleanup [namespace]                             - Remove ALL checkpoints"
    echo ""
    echo "Environment:"
    echo "  AGENT_PORT         - Agent API port (default: 8081)"
    echo "  NAMESPACE          - Kubernetes namespace (default: nvsnap-system)"
    echo "  NVSNAP_CAPTURE_PATH  - Force capture path: criu | rootfs (default: cluster default)"
    exit 1
}

find_agent_for_pod() {
    local pod_name="$1"
    local namespace="${2:-$NAMESPACE}"
    
    # Get the node where the pod is running
    local node=$(kubectl get pod "$pod_name" -n "$namespace" -o jsonpath='{.spec.nodeName}')
    if [[ -z "$node" ]]; then
        echo "Error: Could not find node for pod $pod_name" >&2
        return 1
    fi
    
    # Find the agent pod on that node
    local agent=$(kubectl get pods -n "$namespace" -l app=nvsnap-agent \
        --field-selector "spec.nodeName=$node" \
        -o jsonpath='{.items[0].metadata.name}')
    
    if [[ -z "$agent" ]]; then
        echo "Error: No agent found on node $node" >&2
        return 1
    fi
    
    echo "$agent"
}

get_node_for_pod() {
    local pod_name="$1"
    local namespace="${2:-$NAMESPACE}"
    local node
    node=$(kubectl get pod "$pod_name" -n "$namespace" -o jsonpath='{.spec.nodeName}')
    if [[ -z "$node" ]]; then
        echo "Error: Could not find node for pod $pod_name" >&2
        return 1
    fi
    echo "$node"
}

cleanup_node_checkpoints() {
    local pod_name="$1"
    local namespace="${2:-$NAMESPACE}"
    local node
    local agent

    node=$(get_node_for_pod "$pod_name" "$namespace")
    agent=$(find_agent_for_pod "$pod_name" "$namespace")

    echo "Cleaning checkpoints on node $node via nvsnap-server DELETE API..."

    # We delete via the nvsnap-server DELETE endpoint (not by rm-ing the
    # hostPath directly) so the cascade actually fires:
    #   - nvsnap-server removes the catalog row
    #   - cascade calls the L1 agent to drop the hostPath dump
    #   - cascade calls nvsnap-blobstore to drop the uploaded blobs
    # Direct hostPath rm leaks the L4 blobstore data — every bench run
    # used to add ~30 GB of orphan blobs that never went away (the
    # blob disk on GCP-H100-a hit ENOSPC after ~30 such runs).
    #
    # We reach nvsnap-server through an agent pod (curl is installed in
    # the agent image) so this works without a public LoadBalancer or
    # a port-forward. Override NVSNAP_SERVER_URL if you want to point at
    # a different server (e.g., a remote URL).
    local nvsnap_server_url="${NVSNAP_SERVER_URL:-http://nvsnap-server.${namespace}.svc.cluster.local:8080}"

    # List checkpoints for this namespace (catalog supports pagination —
    # 1000 is well above what any bench cluster accumulates).
    local ckpt_ids
    ckpt_ids=$(kubectl exec -n "$namespace" "$agent" -c agent -- \
        curl -sf "${nvsnap_server_url}/api/v1/checkpoints?limit=1000" 2>/dev/null \
        | python3 -c "
import json, sys
try:
    d = json.load(sys.stdin)
    for c in d.get('checkpoints') or []:
        if c.get('namespace') == '${namespace}':
            print(c.get('id', ''))
except Exception:
    pass
" 2>/dev/null)

    local deleted=0
    if [ -n "$ckpt_ids" ]; then
        while IFS= read -r ckpt_id; do
            [ -z "$ckpt_id" ] && continue
            if kubectl exec -n "$namespace" "$agent" -c agent -- \
                curl -sf -X DELETE "${nvsnap_server_url}/api/v1/checkpoints/${ckpt_id}" \
                >/dev/null 2>&1; then
                deleted=$((deleted + 1))
            fi
        done <<< "$ckpt_ids"
        echo "  Deleted $deleted catalog row(s) via DELETE API (cascade clears L1+L4)"
    else
        echo "  No catalog rows in namespace $namespace (nothing to cascade)"
    fi

    # Belt-and-braces: also rm any leftover hostPath data that lacks a
    # catalog row (e.g., a previous checkpoint that crashed mid-upload).
    # The cascade DELETE above handles the catalog+blobstore side; this
    # catches the edge cases the catalog never knew about.
    if kubectl exec -n "$namespace" "$agent" -c agent -- \
        sh -c 'rm -rf /var/lib/nvsnap/checkpoints/* 2>/dev/null' >/dev/null 2>&1; then
        echo "  Cleared local hostPath residue on $agent"
        return 0
    fi

    local debug_pod="node-debugger-${node}"
    kubectl debug "node/${node}" -n "$namespace" --image=busybox -- \
        chroot /host sh -c 'rm -rf /var/lib/nvsnap/checkpoints/*' >/dev/null 2>&1 || true
    kubectl delete pod "$debug_pod" -n "$namespace" >/dev/null 2>&1 || true
    echo "  Cleared local hostPath residue via node debug on $node"
}

call_agent_api() {
    local agent_pod="$1"
    local method="$2"
    local endpoint="$3"
    local data="${4:-}"
    
    # Start port-forward in background (redirect output to avoid contaminating API response)
    kubectl port-forward -n "$NAMESPACE" "$agent_pod" "${AGENT_PORT}:${AGENT_PORT}" >/dev/null 2>&1 &
    local pf_pid=$!
    trap "kill $pf_pid 2>/dev/null || true" EXIT

    # Wait for port-forward to be ready (poll instead of fixed wait)
    # Port-forward can take time to establish, especially on remote clusters
    local ready=false
    for i in {1..15}; do
        if timeout 5 curl -s http://localhost:${AGENT_PORT}/v1/checkpoints >/dev/null 2>&1; then
            ready=true
            break
        fi
        sleep 2
    done

    if [ "$ready" = "false" ]; then
        echo "Error: Port-forward failed to establish after 30s" >&2
        return 1
    fi
    
    # Make the API call (10 min timeout for large checkpoints like 70B)
    local max_time="${CHECKPOINT_TIMEOUT:-600}"
    if [[ -n "$data" ]]; then
        curl -s --max-time "$max_time" -X "$method" "http://localhost:${AGENT_PORT}${endpoint}" \
            -H "Content-Type: application/json" \
            -d "$data"
    else
        curl -s --max-time "$max_time" -X "$method" "http://localhost:${AGENT_PORT}${endpoint}"
    fi
    
    # Cleanup
    kill $pf_pid 2>/dev/null || true
}

cmd_create() {
    local pod_name="${1:-}"
    local container_name="${2:-}"
    local namespace="${3:-$NAMESPACE}"
    
    if [[ -z "$pod_name" ]]; then
        echo "Error: pod-name required" >&2
        usage
    fi
    
    # Default container name to pod name if not specified
    if [[ -z "$container_name" ]]; then
        # Try to get the first container name from the pod
        container_name=$(kubectl get pod "$pod_name" -n "$namespace" \
            -o jsonpath='{.spec.containers[0].name}')
    fi
    
    cleanup_node_checkpoints "$pod_name" "$namespace"
    local agent=$(find_agent_for_pod "$pod_name" "$namespace")

    echo "Creating checkpoint for $pod_name/$container_name..."
    echo "Agent: $agent"

    # Optional per-request capture-path override. NVSNAP_CAPTURE_PATH=criu forces
    # CRIU + cuda-checkpoint even when the cluster defaults to rootfs (and the
    # agent hard-errors if the cell can't do CRIU — Riva/Triton or multi-GPU);
    # =rootfs forces the rootfs path. Unset = cluster default. See
    # internal/agent resolveCheckpointRedirect.
    local capture_path_line=""
    if [[ -n "${NVSNAP_CAPTURE_PATH:-}" ]]; then
        case "$NVSNAP_CAPTURE_PATH" in
            criu|criu-v2|rootfs) capture_path_line="    \"capturePath\": \"${NVSNAP_CAPTURE_PATH}\","
                         echo "Capture path override: $NVSNAP_CAPTURE_PATH" ;;
            *) echo "Error: NVSNAP_CAPTURE_PATH must be 'criu', 'criu-v2', or 'rootfs' (got '$NVSNAP_CAPTURE_PATH')" >&2
               return 1 ;;
        esac
    fi

    local payload=$(cat <<EOF
{
$capture_path_line
    "podName": "$pod_name",
    "containerName": "$container_name",
    "namespace": "$namespace"
}
EOF
)

    local response=$(call_agent_api "$agent" "POST" "/v1/checkpoint" "$payload")

    # Agent may return a structured 422-style redirect when the workload's
    # backend (Riva or Triton) requires the rootfs capture path. The JSON
    # body looks like:
    #   {"redirect":"rootfs","backend":"riva","reason":"..."}
    # Surface this with a stable marker line + exit code 42 so the caller
    # (test-bench.sh, test-e2e.sh, NVCA reconciler) can re-issue the
    # capture against the rootfsonly watcher instead of failing.
    local redirect=$(echo "$response" | python3 -c "import sys,json; print(json.load(sys.stdin).get('redirect',''))" 2>/dev/null)
    if [ "$redirect" = "rootfs" ]; then
        local backend=$(echo "$response" | python3 -c "import sys,json; print(json.load(sys.stdin).get('backend',''))" 2>/dev/null)
        echo "REDIRECT: rootfs (backend=${backend})"
        echo "Response: $response"
        return 42
    fi

    # Parse actual checkpoint ID + phase 5b artifact pointers (hash +
    # reader PVC name) from agent response. test-e2e.sh greps these
    # to swap the restore manifest's `checkpoints` volume from the
    # legacy node-local hostPath to the per-capture reader PVC when
    # the agent took the phase 5b direct-to-PVC path.
    local checkpoint_id=$(echo "$response" | python3 -c "import sys,json; print(json.load(sys.stdin).get('checkpointId',''))" 2>/dev/null)
    local hash=$(echo "$response" | python3 -c "import sys,json; print(json.load(sys.stdin).get('hash',''))" 2>/dev/null)
    local reader_pvc=$(echo "$response" | python3 -c "import sys,json; print(json.load(sys.stdin).get('readerPvcName',''))" 2>/dev/null)

    if [ -n "$checkpoint_id" ]; then
        echo "Checkpoint ID: $checkpoint_id"
    else
        echo "Error: Failed to get checkpoint ID from response"
        echo "Response: $response"
        return 1
    fi
    if [ -n "$hash" ]; then
        echo "Hash: $hash"
    fi
    if [ -n "$reader_pvc" ]; then
        echo "Reader PVC: $reader_pvc"
    fi
    echo ""
}

cmd_list() {
    local namespace="${1:-$NAMESPACE}"
    
    # Get any agent pod
    local agent=$(kubectl get pods -n "$namespace" -l app=nvsnap-agent \
        -o jsonpath='{.items[0].metadata.name}')
    
    if [[ -z "$agent" ]]; then
        echo "Error: No agent pods found" >&2
        return 1
    fi
    
    echo "Listing checkpoints via agent $agent..."
    call_agent_api "$agent" "GET" "/v1/checkpoints"
    echo ""
}

cmd_cleanup() {
    local namespace="${1:-$NAMESPACE}"
    
    echo "Cleaning up ALL checkpoints on all nodes..."
    echo ""
    
    # Get all agent pods
    local agents=$(kubectl get pods -n "$namespace" -l app=nvsnap-agent \
        -o jsonpath='{.items[*].metadata.name}')
    
    for agent in $agents; do
        echo "Cleaning $agent..."
        kubectl exec -n "$namespace" "$agent" -- \
            sh -c 'rm -rf /var/lib/nvsnap/checkpoints/* 2>/dev/null; echo "Cleaned"' \
            2>&1 || echo "  Failed (disk may be full)"
    done
    
    echo ""
    echo "Done. You may need to SSH to nodes if disk is completely full."
}

# Main
case "${1:-}" in
    create)
        shift
        cmd_create "$@"
        ;;
    list)
        shift
        cmd_list "$@"
        ;;
    cleanup)
        shift
        cmd_cleanup "$@"
        ;;
    *)
        usage
        ;;
esac
