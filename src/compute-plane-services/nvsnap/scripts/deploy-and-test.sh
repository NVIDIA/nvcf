#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# NVSNAP Deploy and Test — Single Entry Point
#
# Deploys the nvsnap-agent and runs a full vLLM checkpoint/restore test
# on any Kubernetes cluster with NVIDIA GPUs.
#
# Usage:
#   KUBECONFIG=/path/to/kubeconfig ./scripts/deploy-and-test.sh
#
# Environment variables:
#   KUBECONFIG           - Path to kubeconfig (required)
#   PULL_SECRET_SOURCE   - Name of existing pull secret to copy (auto-detected)
#   PULL_SECRET_NS       - Namespace where source secret lives (default: "default")
#   AGENT_IMAGE          - Full agent image (default: nvcr.io/0651155215864979/ncp-dev/nvsnap-agent:v0.9.21)
#   SKIP_DEPLOY          - Set to "1" to skip deployment and run test only
#   SKIP_TEST            - Set to "1" to deploy only, skip test

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"

# Source centralized versions
source "${SCRIPT_DIR}/versions.sh"

# Colors
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

log_info()  { echo -e "${GREEN}[INFO]${NC} $*"; }
log_error() { echo -e "${RED}[ERROR]${NC} $*" >&2; }
log_warn()  { echo -e "${YELLOW}[WARN]${NC} $*"; }
log_step()  { echo -e "${BLUE}[STEP]${NC} $*"; }

# Configuration
NAMESPACE="nvsnap-system"
PULL_SECRET_NAME="nvsnap-pull-secret"
PULL_SECRET_NS="${PULL_SECRET_NS:-default}"
AGENT_IMAGE="${AGENT_IMAGE:-${NVSNAP_REGISTRY}/nvsnap-agent:${NVSNAP_APP_VERSION}}"

# ─── Step 1: Validate cluster connectivity ────────────────────────────────────
log_step "Step 1: Validating cluster connectivity..."

if ! kubectl cluster-info >/dev/null 2>&1; then
    log_error "Cannot connect to cluster. Check KUBECONFIG."
    log_error "  KUBECONFIG=${KUBECONFIG:-<not set>}"
    exit 1
fi

CLUSTER_NAME=$(kubectl config current-context 2>/dev/null || echo "unknown")
log_info "Connected to cluster: $CLUSTER_NAME"

# ─── Step 2: Check for GPU nodes ─────────────────────────────────────────────
log_step "Step 2: Checking for GPU nodes..."

GPU_NODES=$(kubectl get nodes -l nvidia.com/gpu.present=true -o name 2>/dev/null || true)
if [ -z "$GPU_NODES" ]; then
    log_warn "No nodes with label nvidia.com/gpu.present=true found."
    log_warn "Checking for nvidia.com/gpu resource..."
    GPU_NODES=$(kubectl get nodes -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.status.allocatable.nvidia\.com/gpu}{"\n"}{end}' 2>/dev/null | awk '$2 > 0 {print $1}')
    if [ -z "$GPU_NODES" ]; then
        log_error "No GPU nodes found in cluster. Cannot proceed."
        exit 1
    fi
    log_info "Found GPU nodes (by allocatable resource):"
    echo "$GPU_NODES" | while read -r node; do echo "  - $node"; done
else
    log_info "Found GPU nodes:"
    echo "$GPU_NODES" | while read -r node; do echo "  - $node"; done
fi
echo ""

if [ "${SKIP_DEPLOY:-0}" = "1" ]; then
    log_info "SKIP_DEPLOY=1, skipping deployment steps..."
else

# ─── Step 3: Create namespace ─────────────────────────────────────────────────
log_step "Step 3: Creating namespace $NAMESPACE..."
kubectl create namespace "$NAMESPACE" --dry-run=client -o yaml | kubectl apply -f -

# ─── Step 4: Set up image pull secret ─────────────────────────────────────────
log_step "Step 4: Setting up image pull secret..."

# Check if nvsnap-pull-secret already exists in target namespace
if kubectl get secret "$PULL_SECRET_NAME" -n "$NAMESPACE" >/dev/null 2>&1; then
    log_info "Pull secret '$PULL_SECRET_NAME' already exists in $NAMESPACE"
else
    # Auto-detect: look for common pull secret names
    PULL_SECRET_SOURCE="${PULL_SECRET_SOURCE:-}"
    if [ -z "$PULL_SECRET_SOURCE" ]; then
        for candidate in "nvidia-ngcuser-pull-secret" "nvsnap-pull-secret" "regcred" "registry-secret"; do
            # Search in default namespace and kube-system
            for ns in "$PULL_SECRET_NS" "kube-system" "default"; do
                if kubectl get secret "$candidate" -n "$ns" >/dev/null 2>&1; then
                    PULL_SECRET_SOURCE="$candidate"
                    PULL_SECRET_NS="$ns"
                    log_info "Auto-detected pull secret: $candidate in namespace $ns"
                    break 2
                fi
            done
        done
    fi

    if [ -n "$PULL_SECRET_SOURCE" ]; then
        log_info "Copying secret '$PULL_SECRET_SOURCE' from $PULL_SECRET_NS to $NAMESPACE as '$PULL_SECRET_NAME'..."
        kubectl get secret "$PULL_SECRET_SOURCE" -n "$PULL_SECRET_NS" -o json | \
            python3 -c "
import sys, json
s = json.load(sys.stdin)
s['metadata'] = {'name': '$PULL_SECRET_NAME', 'namespace': '$NAMESPACE'}
json.dump(s, sys.stdout)
" | kubectl apply -f -
    else
        log_warn "No pull secret found to copy."
        log_warn "If images fail to pull, create one with:"
        log_warn "  kubectl create secret docker-registry $PULL_SECRET_NAME \\"
        log_warn "    --docker-server=stg.nvcr.io \\"
        log_warn "    --docker-username=<user> --docker-password=<password> \\"
        log_warn "    -n $NAMESPACE"
    fi
fi

# ─── Step 5: Deploy agent DaemonSet ──────────────────────────────────────────
log_step "Step 5: Deploying agent DaemonSet (image: $AGENT_IMAGE)..."

# Update image in manifest and apply
sed "s|image: nvcr.io/0651155215864979/ncp-dev/nvsnap-agent:.*|image: $AGENT_IMAGE|" \
    "$PROJECT_ROOT/deploy/k8s/agent-daemonset.yaml" | kubectl apply -f -

# ─── Step 6: Wait for agent pods to be ready ─────────────────────────────────
log_step "Step 6: Waiting for agent rollout (up to 180s)..."

if kubectl rollout status daemonset/nvsnap-agent -n "$NAMESPACE" --timeout=180s; then
    log_info "Agent DaemonSet rolled out successfully"
else
    log_error "Agent rollout failed. Checking pod status..."
    kubectl get pods -n "$NAMESPACE" -l app=nvsnap-agent -o wide
    echo ""
    kubectl describe pods -n "$NAMESPACE" -l app=nvsnap-agent | tail -30
    exit 1
fi

echo ""
log_info "Agent pods:"
kubectl get pods -n "$NAMESPACE" -l app=nvsnap-agent -o wide
echo ""

fi  # end SKIP_DEPLOY

# ─── Step 7: Run test ────────────────────────────────────────────────────────
if [ "${SKIP_TEST:-0}" = "1" ]; then
    log_info "SKIP_TEST=1, skipping test."
    log_info "Deployment complete. Run test manually with:"
    log_info "  $SCRIPT_DIR/test-vllm-zmq.sh"
    exit 0
fi

log_step "Step 7: Running vLLM checkpoint/restore test..."
echo ""

exec "$SCRIPT_DIR/test-vllm-zmq.sh"
