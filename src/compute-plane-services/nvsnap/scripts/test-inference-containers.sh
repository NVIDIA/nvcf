#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

#
# NVSNAP Inference Container Test Suite
#
# Tests checkpoint/restore for various inference containers:
# - vLLM
# - SGLang
# - TensorRT-LLM
# - Text Generation Inference (TGI)
# - llama.cpp
#
# Usage:
#   ./scripts/test-inference-containers.sh [container]
#
# Examples:
#   ./scripts/test-inference-containers.sh          # Test all
#   ./scripts/test-inference-containers.sh vllm     # Test only vLLM
#   ./scripts/test-inference-containers.sh sglang   # Test only SGLang

set -euo pipefail

# Configuration
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"
NAMESPACE="nvsnap-system"
AGENT_POD=""
RESULTS_DIR="${PROJECT_ROOT}/test-results/$(date +%Y%m%d-%H%M%S)"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Load kubeconfig if available
if [ -f "${PROJECT_ROOT}/scripts/config.sh" ]; then
    source "${PROJECT_ROOT}/scripts/config.sh"
fi
export KUBECONFIG="${KUBECONFIG:?KUBECONFIG must point at your cluster kubeconfig — export it before running this script}"

log() { echo -e "${BLUE}[$(date +%H:%M:%S)]${NC} $1"; }
success() { echo -e "${GREEN}[✓]${NC} $1"; }
fail() { echo -e "${RED}[✗]${NC} $1"; }
warn() { echo -e "${YELLOW}[!]${NC} $1"; }

# Container configurations
declare -A CONTAINERS=(
    ["vllm"]="vllm-small|8000|/v1/models"
    ["sglang"]="sglang-small|30000|/v1/models"
    ["trtllm"]="trtllm-small|8000|/"
    ["tgi"]="tgi-small|8080|/health"
    ["llamacpp"]="llamacpp-small|8080|/health"
)

declare -A INFERENCE_ENDPOINTS=(
    ["vllm"]="/v1/completions"
    ["sglang"]="/v1/completions"
    ["trtllm"]="/"
    ["tgi"]="/generate"
    ["llamacpp"]="/completion"
)

declare -A INFERENCE_PAYLOADS=(
    ["vllm"]='{"model":"TinyLlama/TinyLlama-1.1B-Chat-v1.0","prompt":"Hello","max_tokens":10}'
    ["sglang"]='{"model":"TinyLlama/TinyLlama-1.1B-Chat-v1.0","prompt":"Hello","max_tokens":10}'
    ["trtllm"]='{"prompts":["Hello"]}'
    ["tgi"]='{"inputs":"Hello","parameters":{"max_new_tokens":10}}'
    ["llamacpp"]='{"prompt":"Hello","n_predict":10}'
)

# Setup
setup() {
    log "Setting up test environment..."
    
    mkdir -p "$RESULTS_DIR"
    log "Results will be saved to: $RESULTS_DIR"
    
    # Find agent pod
    AGENT_POD=$(kubectl get pods -n "$NAMESPACE" -l app=nvsnap-agent -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
    if [ -z "$AGENT_POD" ]; then
        fail "NVSNAP agent not found in namespace $NAMESPACE"
        exit 1
    fi
    success "Found agent pod: $AGENT_POD"
    
    # Clean old checkpoints
    log "Cleaning old checkpoints..."
    kubectl exec -n "$NAMESPACE" "$AGENT_POD" -- rm -rf /var/lib/nvsnap/checkpoints/* 2>/dev/null || true
}

# Wait for pod to be ready
wait_for_pod() {
    local pod_name="$1"
    local timeout="${2:-300}"
    
    log "Waiting for pod $pod_name to be ready (timeout: ${timeout}s)..."
    
    local start_time=$(date +%s)
    while true; do
        local status=$(kubectl get pod -n "$NAMESPACE" "$pod_name" -o jsonpath='{.status.phase}' 2>/dev/null || echo "NotFound")
        
        if [ "$status" = "Running" ]; then
            # Check if container is ready
            local ready=$(kubectl get pod -n "$NAMESPACE" "$pod_name" -o jsonpath='{.status.containerStatuses[0].ready}' 2>/dev/null || echo "false")
            if [ "$ready" = "true" ]; then
                success "Pod $pod_name is ready"
                return 0
            fi
        elif [ "$status" = "Failed" ] || [ "$status" = "Error" ]; then
            fail "Pod $pod_name failed"
            kubectl logs -n "$NAMESPACE" "$pod_name" --tail=50 2>/dev/null || true
            return 1
        fi
        
        local elapsed=$(($(date +%s) - start_time))
        if [ $elapsed -ge $timeout ]; then
            fail "Timeout waiting for pod $pod_name"
            kubectl describe pod -n "$NAMESPACE" "$pod_name" 2>/dev/null || true
            return 1
        fi
        
        sleep 5
    done
}

# Wait for service to be ready (health check)
wait_for_service() {
    local pod_name="$1"
    local port="$2"
    local health_path="$3"
    local timeout="${4:-120}"
    
    log "Waiting for service on port $port to be ready..."
    
    local start_time=$(date +%s)
    while true; do
        # Port-forward and check health
        local response=$(kubectl exec -n "$NAMESPACE" "$pod_name" -- curl -s -o /dev/null -w "%{http_code}" "http://localhost:${port}${health_path}" 2>/dev/null || echo "000")
        
        if [ "$response" = "200" ] || [ "$response" = "204" ]; then
            success "Service is healthy (HTTP $response)"
            return 0
        fi
        
        local elapsed=$(($(date +%s) - start_time))
        if [ $elapsed -ge $timeout ]; then
            fail "Timeout waiting for service health"
            return 1
        fi
        
        sleep 5
    done
}

# Run inference test
test_inference() {
    local container_type="$1"
    local pod_name="$2"
    local port="$3"
    
    local endpoint="${INFERENCE_ENDPOINTS[$container_type]}"
    local payload="${INFERENCE_PAYLOADS[$container_type]}"
    
    log "Testing inference on $pod_name..."
    
    local response=$(kubectl exec -n "$NAMESPACE" "$pod_name" -- curl -s -X POST \
        "http://localhost:${port}${endpoint}" \
        -H "Content-Type: application/json" \
        -d "$payload" 2>/dev/null || echo "FAILED")
    
    if [ "$response" != "FAILED" ] && [ -n "$response" ]; then
        success "Inference succeeded"
        echo "$response" | head -c 500
        echo ""
        return 0
    else
        fail "Inference failed"
        return 1
    fi
}

# Checkpoint a container
checkpoint_container() {
    local pod_name="$1"
    local results_file="$2"
    
    log "Creating checkpoint for $pod_name..."
    
    local start_time=$(date +%s)
    
    local response=$(kubectl exec -n "$NAMESPACE" "$AGENT_POD" -- curl -s -X POST \
        "http://localhost:8081/checkpoint" \
        -H "Content-Type: application/json" \
        -d "{\"namespace\":\"$NAMESPACE\",\"podName\":\"$pod_name\"}" 2>/dev/null)
    
    local end_time=$(date +%s)
    local duration=$((end_time - start_time))
    
    echo "$response" > "${results_file}.checkpoint.json"
    
    if echo "$response" | grep -q '"checkpointId"'; then
        local checkpoint_id=$(echo "$response" | grep -o '"checkpointId":"[^"]*"' | cut -d'"' -f4)
        success "Checkpoint created: $checkpoint_id (${duration}s)"
        echo "$checkpoint_id"
        return 0
    else
        fail "Checkpoint failed"
        echo "$response"
        return 1
    fi
}

# Delete source pod
delete_pod() {
    local pod_name="$1"
    
    log "Deleting pod $pod_name..."
    kubectl delete pod -n "$NAMESPACE" "$pod_name" --ignore-not-found --wait=false 2>/dev/null || true
    
    # Wait for pod to be gone
    local timeout=60
    local start_time=$(date +%s)
    while kubectl get pod -n "$NAMESPACE" "$pod_name" &>/dev/null; do
        local elapsed=$(($(date +%s) - start_time))
        if [ $elapsed -ge $timeout ]; then
            warn "Pod $pod_name taking too long to delete, continuing..."
            break
        fi
        sleep 2
    done
    
    success "Pod $pod_name deleted"
}

# Deploy restore pod
deploy_restore() {
    local container_type="$1"
    local checkpoint_id="$2"
    
    local manifest_file="${PROJECT_ROOT}/deploy/k8s/${container_type}-small.yaml"
    local restore_pod="${container_type}-restored"
    
    log "Deploying restore pod for $container_type..."
    
    # Delete existing restore pod
    kubectl delete pod -n "$NAMESPACE" "$restore_pod" --ignore-not-found 2>/dev/null || true
    sleep 2
    
    # Extract restore pod from manifest and update checkpoint ID
    # The restore pod is the second document in the YAML file
    kubectl get -f "$manifest_file" -o yaml 2>/dev/null | \
        yq eval-all 'select(.metadata.name == "'$restore_pod'")' - | \
        sed "s/CHECKPOINT_ID.*/CHECKPOINT_ID\n      value: \"$checkpoint_id\"/" | \
        kubectl apply -f - 2>/dev/null || {
            # Fallback: manually create restore pod
            log "Using fallback restore pod creation..."
            cat "$manifest_file" | \
                grep -A 1000 "^---" | \
                sed "s/value: \".*__TIMESTAMP\"/value: \"$checkpoint_id\"/" | \
                kubectl apply -f -
        }
    
    success "Restore pod deployed"
}

# Test a single container
test_container() {
    local container_type="$1"
    local config="${CONTAINERS[$container_type]}"
    
    IFS='|' read -r pod_name port health_path <<< "$config"
    
    local result_file="${RESULTS_DIR}/${container_type}"
    
    echo ""
    log "=============================================="
    log "Testing: $container_type"
    log "=============================================="
    
    # Step 1: Deploy source pod
    log "Deploying source pod..."
    kubectl apply -f "${PROJECT_ROOT}/deploy/k8s/${container_type}-small.yaml" 2>/dev/null || {
        fail "Failed to deploy $container_type"
        echo "DEPLOY_FAILED" > "${result_file}.status"
        return 1
    }
    
    # Step 2: Wait for pod to be ready
    if ! wait_for_pod "$pod_name" 300; then
        echo "POD_NOT_READY" > "${result_file}.status"
        return 1
    fi
    
    # Step 3: Wait for service to be ready
    if ! wait_for_service "$pod_name" "$port" "$health_path" 180; then
        echo "SERVICE_NOT_READY" > "${result_file}.status"
        kubectl logs -n "$NAMESPACE" "$pod_name" > "${result_file}.source.log" 2>&1 || true
        return 1
    fi
    
    # Step 4: Test inference before checkpoint
    log "Testing inference before checkpoint..."
    if ! test_inference "$container_type" "$pod_name" "$port"; then
        echo "INFERENCE_BEFORE_FAILED" > "${result_file}.status"
        return 1
    fi
    
    # Step 5: Create checkpoint
    local checkpoint_id
    checkpoint_id=$(checkpoint_container "$pod_name" "$result_file") || {
        echo "CHECKPOINT_FAILED" > "${result_file}.status"
        return 1
    }
    
    # Step 6: Delete source pod
    delete_pod "$pod_name"
    
    # Step 7: Deploy restore pod
    deploy_restore "$container_type" "$checkpoint_id"
    
    # Step 8: Wait for restore pod
    local restore_pod="${container_type}-restored"
    if ! wait_for_pod "$restore_pod" 300; then
        echo "RESTORE_POD_NOT_READY" > "${result_file}.status"
        kubectl logs -n "$NAMESPACE" "$restore_pod" > "${result_file}.restore.log" 2>&1 || true
        return 1
    fi
    
    # Step 9: Wait for restored service
    sleep 10  # Give the restored process time to stabilize
    if ! wait_for_service "$restore_pod" "$port" "$health_path" 60; then
        echo "RESTORED_SERVICE_NOT_READY" > "${result_file}.status"
        kubectl logs -n "$NAMESPACE" "$restore_pod" > "${result_file}.restore.log" 2>&1 || true
        return 1
    fi
    
    # Step 10: Test inference after restore
    log "Testing inference after restore..."
    if ! test_inference "$container_type" "$restore_pod" "$port"; then
        echo "INFERENCE_AFTER_FAILED" > "${result_file}.status"
        kubectl logs -n "$NAMESPACE" "$restore_pod" > "${result_file}.restore.log" 2>&1 || true
        return 1
    fi
    
    # Success!
    echo "SUCCESS" > "${result_file}.status"
    success "$container_type: All tests passed!"
    
    # Cleanup
    delete_pod "$restore_pod"
    delete_pod "$pod_name"
    
    return 0
}

# Generate summary report
generate_report() {
    log "Generating test report..."
    
    local report_file="${RESULTS_DIR}/summary.md"
    
    cat > "$report_file" << EOF
# NVSNAP Inference Container Test Report

**Date**: $(date)
**Results Directory**: $RESULTS_DIR

## Summary

| Container | Status | Notes |
|-----------|--------|-------|
EOF
    
    for container_type in "${!CONTAINERS[@]}"; do
        local status_file="${RESULTS_DIR}/${container_type}.status"
        local status="NOT_TESTED"
        local notes=""
        
        if [ -f "$status_file" ]; then
            status=$(cat "$status_file")
        fi
        
        case "$status" in
            SUCCESS) status="✅ Pass" ;;
            DEPLOY_FAILED) status="❌ Fail"; notes="Deployment failed" ;;
            POD_NOT_READY) status="❌ Fail"; notes="Pod not ready" ;;
            SERVICE_NOT_READY) status="❌ Fail"; notes="Service health check failed" ;;
            INFERENCE_BEFORE_FAILED) status="❌ Fail"; notes="Pre-checkpoint inference failed" ;;
            CHECKPOINT_FAILED) status="❌ Fail"; notes="Checkpoint creation failed" ;;
            RESTORE_POD_NOT_READY) status="❌ Fail"; notes="Restore pod failed" ;;
            RESTORED_SERVICE_NOT_READY) status="❌ Fail"; notes="Restored service not healthy" ;;
            INFERENCE_AFTER_FAILED) status="❌ Fail"; notes="Post-restore inference failed" ;;
            *) status="⏸️ Not tested" ;;
        esac
        
        echo "| $container_type | $status | $notes |" >> "$report_file"
    done
    
    cat >> "$report_file" << EOF

## Details

See individual log files in this directory for details.

## Files

EOF
    
    ls -la "$RESULTS_DIR" >> "$report_file"
    
    success "Report saved to $report_file"
    cat "$report_file"
}

# Main
main() {
    local target_container="${1:-all}"
    
    echo ""
    echo "=============================================="
    echo " NVSNAP Inference Container Test Suite"
    echo "=============================================="
    echo ""
    
    setup
    
    if [ "$target_container" = "all" ]; then
        for container_type in vllm sglang tgi llamacpp; do
            test_container "$container_type" || warn "Test failed for $container_type"
        done
    else
        if [ -z "${CONTAINERS[$target_container]:-}" ]; then
            fail "Unknown container: $target_container"
            echo "Available: ${!CONTAINERS[*]}"
            exit 1
        fi
        test_container "$target_container"
    fi
    
    generate_report
}

# Cleanup on exit
cleanup() {
    log "Cleaning up..."
    # Optional: delete all test pods
    # kubectl delete pods -n "$NAMESPACE" -l nvsnap.io/inference-engine --ignore-not-found 2>/dev/null || true
}
trap cleanup EXIT

main "$@"
