#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

#
# NVSNAP Installation Script
#
# Deploys the full NvSnap stack onto a Kubernetes cluster:
#   - nvsnap-system namespace
#   - CRDs (GPUCheckpoint, GPURestore)
#   - nvsnap-agent DaemonSet (runs on every GPU node, hostNetwork)
#   - nvsnap-server Deployment (catalog + UI)
#   - nvsnap-blobstore Deployment (durable backstop for cross-node restore)
#   - nvsnap-webhook (Issuer + Cert + Service + MutatingWebhookConfiguration)
#     — the mutating admission webhook that injects restore init
#     containers for pods carrying `nvsnap.io/restore-from: <hash>`.
#     The webhook is served by the nvsnap-agent DaemonSet directly
#     (no separate pod). Requires cert-manager pre-installed.
#
# The nvsnap-pull-secret for nvcr.io/0651155215864979/ncp-dev/* must already
# exist in nvsnap-system before running this. See docs/PULL-SECRET-SETUP.md
# for the full walkthrough (NGC API key + per-namespace secret + rotation).
#
# Variants:
#   --crio       Use the CRI-O daemonset variant (default: containerd).
#   --no-server  Skip nvsnap-server (admin UI + catalog) — for agent-only
#                clusters that talk to an out-of-cluster server.
#   --no-blob    Skip nvsnap-blobstore — disables Phase 5d.2 durability.

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
NAMESPACE="nvsnap-system"

USE_CRIO=0
SKIP_SERVER=0
SKIP_BLOB=0
for arg in "$@"; do
    case "$arg" in
        --crio)       USE_CRIO=1 ;;
        --no-server)  SKIP_SERVER=1 ;;
        --no-blob)    SKIP_BLOB=1 ;;
        -h|--help)
            grep -E '^# ' "$0" | sed -E 's/^#\s?//'
            exit 0
            ;;
        *)
            echo "Unknown arg: $arg" >&2
            exit 2
            ;;
    esac
done

DAEMONSET="$SCRIPT_DIR/k8s/agent-daemonset.yaml"
if [ "$USE_CRIO" = "1" ]; then
    DAEMONSET="$SCRIPT_DIR/k8s/agent-daemonset-crio.yaml"
fi

echo "╔═══════════════════════════════════════════╗"
echo "║      NvSnap - GPU Checkpoint/Restore       ║"
echo "║              Installation                ║"
echo "╚═══════════════════════════════════════════╝"
echo ""

# [1] Prerequisites
echo "[1/7] Checking prerequisites..."
command -v kubectl >/dev/null 2>&1 || { echo "kubectl required but not found"; exit 1; }
kubectl cluster-info >/dev/null 2>&1 || { echo "Cannot connect to cluster — check KUBECONFIG"; exit 1; }
echo "  OK kubectl connected to: $(kubectl config current-context)"

# [2] Namespace
echo ""
echo "[2/7] Creating namespace $NAMESPACE..."
kubectl create namespace $NAMESPACE --dry-run=client -o yaml | kubectl apply -f -
echo "  OK namespace ready"

# [3] Pull secret check (warn-only — admin must seed this before agents can pull)
echo ""
echo "[3/7] Checking image pull secret..."
if ! kubectl get secret -n $NAMESPACE nvsnap-pull-secret >/dev/null 2>&1; then
    echo "  WARN: nvsnap-pull-secret missing in $NAMESPACE."
    echo "        Agents and server will ImagePullBackOff until this is created."
    echo "        See docs/PULL-SECRET-SETUP.md for the setup walkthrough."
else
    echo "  OK pull secret present"
fi

# [4] CRDs
echo ""
echo "[4/7] Installing CRDs..."
kubectl apply -f "$SCRIPT_DIR/crds/"
kubectl wait --for=condition=Established crd/gpucheckpoints.nvsnap.io --timeout=60s
kubectl wait --for=condition=Established crd/gpurestores.nvsnap.io --timeout=60s
echo "  OK CRDs established"

# [5] nvsnap-server (catalog + UI)
echo ""
if [ "$SKIP_SERVER" = "1" ]; then
    echo "[5/7] Skipping nvsnap-server (--no-server)"
else
    echo "[5/7] Deploying nvsnap-server..."
    kubectl apply -f "$SCRIPT_DIR/k8s/nvsnap-server.yaml"
    kubectl rollout status -n $NAMESPACE deployment/nvsnap-server --timeout=180s
    echo "  OK nvsnap-server ready"
fi

# [6] nvsnap-blobstore (Phase 5d.2 durable backstop)
echo ""
if [ "$SKIP_BLOB" = "1" ]; then
    echo "[6/7] Skipping nvsnap-blobstore (--no-blob)"
else
    echo "[6/7] Deploying nvsnap-blobstore..."
    kubectl apply -f "$SCRIPT_DIR/k8s/nvsnap-blobstore.yaml"
    kubectl rollout status -n $NAMESPACE deployment/nvsnap-blobstore --timeout=180s
    echo "  OK nvsnap-blobstore ready"
fi

# [7] Mutating admission webhook (Issuer + Cert + Service + Config)
echo ""
echo "[7/8] Deploying nvsnap-webhook..."
# Requires cert-manager for the self-signed Issuer/Certificate that
# produces the TLS secret consumed by the agent. The webhook is served
# by the nvsnap-agent DaemonSet directly; the agent must therefore be
# rolled out *after* the TLS Secret is materialised (next step).
if ! kubectl get crd certificates.cert-manager.io >/dev/null 2>&1; then
    echo "  WARN: cert-manager CRDs missing. Install cert-manager first:"
    echo "        kubectl apply -f https://github.com/cert-manager/cert-manager/releases/latest/download/cert-manager.yaml"
    echo "        Skipping webhook deploy — multi-GPU rootfs-only restore will not have init container injection."
else
    kubectl apply -f "$SCRIPT_DIR/k8s/webhook.yaml"
    echo "  Waiting for nvsnap-webhook-tls Secret (cert-manager Issuer → Certificate → Secret)..."
    for i in $(seq 1 30); do
        if kubectl get secret -n $NAMESPACE nvsnap-webhook-tls >/dev/null 2>&1; then
            echo "  OK nvsnap-webhook-tls Secret materialised"
            break
        fi
        sleep 2
    done
    if ! kubectl get secret -n $NAMESPACE nvsnap-webhook-tls >/dev/null 2>&1; then
        echo "  WARN: nvsnap-webhook-tls Secret not produced within 60s. Webhook will fail-open."
    fi
fi

# [8] Agent DaemonSet
echo ""
echo "[8/8] Deploying nvsnap-agent ($([ "$USE_CRIO" = "1" ] && echo crio || echo containerd) variant)..."
kubectl apply -f "$DAEMONSET"
kubectl rollout status -n $NAMESPACE daemonset/nvsnap-agent --timeout=300s
echo "  OK agent rolled out on all GPU nodes"

echo ""
echo "════════════════════════════════════════════"
echo "Installation complete."
echo ""
echo "Cluster: $(kubectl config current-context)"
kubectl get pods -n $NAMESPACE
echo ""
echo "Next steps:"
echo "  - Run e2e: ./scripts/test-e2e.sh vllm-small"
echo "  - Tail an agent: kubectl logs -n $NAMESPACE -l app=nvsnap-agent -f"
echo "  - nvsnap-server UI: kubectl port-forward -n $NAMESPACE svc/nvsnap-server 8080"
echo "════════════════════════════════════════════"
