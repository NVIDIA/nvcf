#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

# Deploy nvsnap-agent DaemonSet to Kubernetes
# Works with managed K8s (GKE, EKS, AKS) and self-managed clusters
#
# Prerequisites:
# - kubectl configured with target cluster
# - Image pull secret (if using private registry)

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"

# Configuration
NAMESPACE="nvsnap-system"
REGISTRY="${NVSNAP_REGISTRY:-nvcr.io/0651155215864979/ncp-dev}"
IMAGE_NAME="nvsnap-agent"
VERSION="${VERSION:-$(cat "$PROJECT_ROOT/.container-version" 2>/dev/null || echo "latest")}"
PULL_SECRET_NAME="nvsnap-pull-secret"

log_info() { echo "[INFO] $1"; }
log_error() { echo "[ERROR] $1" >&2; }

# Check kubectl
if ! kubectl cluster-info &>/dev/null; then
    log_error "Cannot connect to cluster. Check KUBECONFIG."
    exit 1
fi

log_info "Deploying nvsnap-agent to Kubernetes"
log_info "  Namespace: $NAMESPACE"
log_info "  Image: $REGISTRY/$IMAGE_NAME:$VERSION"
echo ""

# Create namespace
log_info "Creating namespace..."
kubectl create namespace "$NAMESPACE" --dry-run=client -o yaml | kubectl apply -f -

# Check if pull secret exists in default namespace and copy it
if kubectl get secret "$PULL_SECRET_NAME" -n default &>/dev/null; then
    log_info "Copying image pull secret to $NAMESPACE..."
    kubectl get secret "$PULL_SECRET_NAME" -n default -o yaml | \
        sed "s/namespace: default/namespace: $NAMESPACE/" | \
        kubectl apply -f -
else
    log_info "No pull secret found in default namespace"
    log_info "If using private registry, create secret with:"
    log_info "  kubectl create secret docker-registry $PULL_SECRET_NAME \\"
    log_info "    --docker-server=$REGISTRY \\"
    log_info "    --docker-username=<user> \\"
    log_info "    --docker-password=<password> \\"
    log_info "    -n $NAMESPACE"
fi

# Update image version in manifest and apply
log_info "Deploying DaemonSet..."
sed "s|image: .*nvsnap-agent:.*|image: $REGISTRY/$IMAGE_NAME:$VERSION|" \
    "$PROJECT_ROOT/deploy/k8s/agent-daemonset.yaml" | kubectl apply -f -

# Wait for rollout
log_info "Waiting for rollout..."
kubectl rollout status daemonset/nvsnap-agent -n "$NAMESPACE" --timeout=120s || {
    log_error "Rollout failed. Check pod status:"
    kubectl get pods -n "$NAMESPACE" -l app=nvsnap-agent
    kubectl describe pods -n "$NAMESPACE" -l app=nvsnap-agent | tail -30
    exit 1
}

# Show status
echo ""
log_info "Deployment complete!"
echo ""
kubectl get pods -n "$NAMESPACE" -l app=nvsnap-agent -o wide
echo ""
log_info "Agent API available on port 8081 of each GPU node"
log_info "Test: curl http://<node-ip>:8081/health"
