#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# install-nvsnap.sh — one-command bootstrap for nvsnap on a fresh K8s cluster.
#
# Walks the prereqs from docs/INSTALL.md:
#   1. Verify kubectl + helm versions
#   2. Detect cluster + check it has at least one GPU node
#   3. Create the nvsnap-system namespace
#   4. Create nvsnap-pull-secret from $NGC_API_KEY (or auto-extract from
#      ~/.docker/config.json if you've already `docker login nvcr.io`)
#   5. Install cert-manager (for the restore admission webhook) — auto-skipped
#      if already present; skip the whole webhook with --without-webhook
#   6. helm install nvsnap from this checkout
#   7. Wait for the agent DaemonSet to be Ready on every GPU node
#
# Idempotent: re-runnable; existing objects are left in place / patched.
#
# The mutating webhook is ON by default: without it, NVCA restore pods are
# never injected with the L2 rox mount and silently cold-start. It's core to
# restore, not optional — only disable it on clusters that genuinely can't run
# cert-manager.
#
# Usage:
#   ./scripts/install-nvsnap.sh                       # full install (webhook on)
#   ./scripts/install-nvsnap.sh --without-webhook     # skip cert-manager + webhook
#   ./scripts/install-nvsnap.sh --namespace my-nvsnap   # custom namespace
#   ./scripts/install-nvsnap.sh --dry-run             # render manifests, don't apply
#   NGC_API_KEY=nvapi-... ./scripts/install-nvsnap.sh
#   KUBECONFIG=/path/to/config ./scripts/install-nvsnap.sh

set -euo pipefail

# ─── Args ──────────────────────────────────────────────────────────────

NAMESPACE="nvsnap-system"
WITH_WEBHOOK=true
DRY_RUN=false
EXTRA_HELM_ARGS=()

while [ "$#" -gt 0 ]; do
    case "$1" in
        --namespace)       NAMESPACE="$2"; shift 2 ;;
        --without-webhook) WITH_WEBHOOK=false; shift ;;
        --with-webhook)    WITH_WEBHOOK=true; shift ;;  # back-compat no-op (default)
        --dry-run)      DRY_RUN=true; shift ;;
        --set)          EXTRA_HELM_ARGS+=(--set "$2"); shift 2 ;;
        -h|--help)
            sed -n '2,30p' "${BASH_SOURCE[0]}" | sed 's|^# \?||'
            exit 0
            ;;
        *)
            echo "ERROR: unknown arg: $1" >&2
            echo "Try --help" >&2
            exit 1
            ;;
    esac
done

# ─── Helpers ───────────────────────────────────────────────────────────

step() { echo; echo "===> $*"; }
info() { echo "     $*"; }
fail() { echo "ERROR: $*" >&2; exit 1; }

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
CHART_DIR="$REPO_ROOT/deploy/helm/nvsnap"

# ─── 1. Tooling prereq checks ──────────────────────────────────────────

step "[1/7] Checking local tooling"

command -v kubectl >/dev/null 2>&1 || fail "kubectl not found in PATH"
info "kubectl: $(kubectl version --client -o yaml 2>/dev/null | awk '/gitVersion/{print $2; exit}')"

command -v helm >/dev/null 2>&1 || fail "helm not found in PATH"
helm_version="$(helm version --short 2>/dev/null)"
info "helm:    $helm_version"
case "$helm_version" in
    v3.*) ;;
    *) fail "helm 3.x required (got $helm_version)" ;;
esac

[ -d "$CHART_DIR" ] || fail "chart directory not found at $CHART_DIR (run from a nvsnap repo checkout)"

# ─── 2. Cluster + GPU node detection ───────────────────────────────────

step "[2/7] Verifying cluster + GPU nodes"

kubectl cluster-info --request-timeout=5s >/dev/null 2>&1 \
    || fail "cannot reach cluster — check KUBECONFIG / tsh login"

context="$(kubectl config current-context)"
info "cluster context: $context"

# Look for any node matching one of the GPU labels the chart targets.
gpu_node_count=0
for label_selector in \
    "nvidia.com/gpu.present=true" \
    "cloud.google.com/gke-gpu=true" \
    "feature.node.kubernetes.io/pci-10de.present=true"
do
    n=$(kubectl get nodes -l "$label_selector" --no-headers 2>/dev/null | wc -l)
    if [ "$n" -gt 0 ]; then
        info "found $n node(s) matching $label_selector"
        gpu_node_count=$((gpu_node_count + n))
    fi
done
if [ "$gpu_node_count" -eq 0 ]; then
    echo
    echo "WARNING: no nodes match any of the GPU labels the chart targets." >&2
    echo "  Labels searched: nvidia.com/gpu.present, cloud.google.com/gke-gpu," >&2
    echo "                   feature.node.kubernetes.io/pci-10de.present" >&2
    echo "  The agent DaemonSet will be installed but won't schedule any pods." >&2
    echo "  Either install the NVIDIA GPU Operator + Node Feature Discovery" >&2
    echo "  on your cluster, or override agent.nodeAffinity at install time." >&2
    echo
    read -p "  Proceed anyway? [y/N] " -r reply
    [[ "$reply" =~ ^[yY]$ ]] || exit 1
fi

# ─── 3. Namespace ──────────────────────────────────────────────────────

step "[3/7] Ensuring namespace $NAMESPACE"

if kubectl get namespace "$NAMESPACE" >/dev/null 2>&1; then
    info "namespace $NAMESPACE already exists"
else
    if $DRY_RUN; then
        info "(dry-run) would create namespace $NAMESPACE"
    else
        kubectl create namespace "$NAMESPACE"
    fi
fi

# ─── 4. Pull secret ────────────────────────────────────────────────────

step "[4/7] Setting up NGC pull secret"

if kubectl -n "$NAMESPACE" get secret nvsnap-pull-secret >/dev/null 2>&1; then
    info "nvsnap-pull-secret already exists in $NAMESPACE — leaving as-is"
else
    # Resolve NGC_API_KEY:
    #   1. $NGC_API_KEY env if set
    #   2. password half of nvcr.io auth in ~/.docker/config.json
    if [ -z "${NGC_API_KEY:-}" ] && [ -r "$HOME/.docker/config.json" ]; then
        NGC_API_KEY="$(python3 - <<'PY' 2>/dev/null || true
import json, base64, sys
try:
    cfg = json.load(open(__import__("os").path.expanduser("~/.docker/config.json")))
except Exception:
    sys.exit("")
auth = cfg.get("auths", {}).get("nvcr.io", {}).get("auth")
if not auth:
    sys.exit("")
decoded = base64.b64decode(auth).decode()
_, _, pwd = decoded.partition(":")
print(pwd)
PY
)"
        [ -n "${NGC_API_KEY:-}" ] && info "extracted NGC API key from ~/.docker/config.json"
    fi
    if [ -z "${NGC_API_KEY:-}" ]; then
        echo
        echo "Need an NGC API key to create the pull secret." >&2
        echo "Get one from https://ngc.nvidia.com/setup/api-key" >&2
        echo "Then re-run with: NGC_API_KEY=nvapi-... ./scripts/install-nvsnap.sh" >&2
        echo "(or run 'docker login nvcr.io' first and this script will pick it up)" >&2
        exit 1
    fi
    if $DRY_RUN; then
        info "(dry-run) would create nvsnap-pull-secret in $NAMESPACE"
    else
        kubectl create secret docker-registry nvsnap-pull-secret \
            --namespace="$NAMESPACE" \
            --docker-server=nvcr.io \
            --docker-username='$oauthtoken' \
            --docker-password="$NGC_API_KEY"
        info "created nvsnap-pull-secret"
    fi
fi

# ─── 5. cert-manager + gpu-operator (auto-detect) ──────────────────────
#
# The chart bundles cert-manager + gpu-operator as subcharts (enabled by
# default). On a cluster that already has either, the subchart's CRDs
# collide with the existing install ("cannot be imported into the current
# release"). Auto-detect and disable the bundled subchart in that case so
# a bare `./install-nvsnap.sh` Just Works — no manual --set needed.

step "[5/7] cert-manager + gpu-operator (auto-detect)"

# Accumulates --set flags computed from cluster state.
AUTO_SET=()

# gpu-operator: if a ClusterPolicy CRD or the gpu-operator namespace
# exists, the cluster already runs the GPU Operator → don't bundle it.
if kubectl get crd clusterpolicies.nvidia.com >/dev/null 2>&1 \
   || kubectl get ns gpu-operator >/dev/null 2>&1; then
    info "gpu-operator already present → disabling bundled subchart"
    AUTO_SET+=(--set gpu-operator.enabled=false)
fi

if $WITH_WEBHOOK; then
    if kubectl get crd certificates.cert-manager.io >/dev/null 2>&1; then
        info "cert-manager already installed"
    else
        info "installing cert-manager (latest stable)"
        if $DRY_RUN; then
            info "(dry-run) would apply https://github.com/cert-manager/cert-manager/releases/latest/download/cert-manager.yaml"
        else
            kubectl apply -f https://github.com/cert-manager/cert-manager/releases/latest/download/cert-manager.yaml
            info "waiting for cert-manager deployments to be Available..."
            kubectl wait --for=condition=Available --timeout=300s \
                -n cert-manager \
                deployment/cert-manager \
                deployment/cert-manager-webhook \
                deployment/cert-manager-cainjector
        fi
    fi
    # This script owns cert-manager (installed standalone above), so the
    # chart's bundled subchart must stay off either way.
    AUTO_SET+=(--set cert-manager.enabled=false)
else
    info "skipping cert-manager + webhook (--without-webhook)"
fi

# ─── 6. Helm install ───────────────────────────────────────────────────

step "[6/7] helm install / upgrade nvsnap"

webhook_flag=()
if ! $WITH_WEBHOOK; then
    webhook_flag=(--set webhook.enabled=false)
fi

if helm status -n "$NAMESPACE" nvsnap >/dev/null 2>&1; then
    op=upgrade
    info "existing release found — running helm upgrade"
else
    op=install
    info "no existing release — running helm install"
fi

helm_cmd=(helm "$op" nvsnap "$CHART_DIR" --namespace "$NAMESPACE" "${webhook_flag[@]}" "${AUTO_SET[@]}" "${EXTRA_HELM_ARGS[@]}")
if $DRY_RUN; then
    info "(dry-run) ${helm_cmd[*]} --dry-run"
    "${helm_cmd[@]}" --dry-run >/dev/null
    info "dry-run rendered OK"
else
    "${helm_cmd[@]}"
fi

# ─── 7. Wait for Ready ─────────────────────────────────────────────────

step "[7/7] Waiting for components to be Ready"

if $DRY_RUN; then
    info "(dry-run) skipping wait"
else
    info "agent DaemonSet rollout..."
    kubectl -n "$NAMESPACE" rollout status ds/nvsnap-agent --timeout=300s
    info "server Deployment rollout..."
    kubectl -n "$NAMESPACE" rollout status deploy/nvsnap-server --timeout=300s || true
    info "blobstore Deployment rollout..."
    kubectl -n "$NAMESPACE" rollout status deploy/nvsnap-blobstore --timeout=300s || true
fi

echo
echo "─────────────────────────────────────────────────────────────"
echo "NvSnap installed in namespace $NAMESPACE on context $context"
echo "─────────────────────────────────────────────────────────────"
kubectl -n "$NAMESPACE" get pods 2>&1 | head -20
echo
echo "Next steps:"
echo "  - Watch logs:   kubectl -n $NAMESPACE logs -f ds/nvsnap-agent"
echo "  - UI / API:     kubectl -n $NAMESPACE port-forward svc/nvsnap-server 8080:8080"
echo "  - E2E test:     ./scripts/test-e2e.sh vllm-small"
echo "  - Uninstall:    helm uninstall nvsnap --namespace $NAMESPACE"
