#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CLUSTER_NAME="ncp-local"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

log_info()  { echo -e "${GREEN}[INFO]${NC} $1"; }
log_warn()  { echo -e "${YELLOW}[WARN]${NC} $1"; }
log_error() { echo -e "${RED}[ERROR]${NC} $1"; }

check_tool() {
    if ! command -v "$1" &> /dev/null; then
        log_error "$1 is not installed. $2"
        exit 1
    fi
}

get_helm_ownership_metadata() {
    local resource="$1"
    local namespace="${2:-}"
    local get_args=("$resource")

    if [[ -n "$namespace" ]]; then
        get_args+=(-n "$namespace")
    fi

    kubectl get "${get_args[@]}" \
        -o jsonpath='{.metadata.labels.app\.kubernetes\.io/managed-by}{"|"}{.metadata.annotations.meta\.helm\.sh/release-name}{"|"}{.metadata.annotations.meta\.helm\.sh/release-namespace}' 2>/dev/null || true
}

cleanup_stale_fake_gpu_operator_resources() {
    if helm status gpu-operator -n gpu-operator &> /dev/null; then
        return 0
    fi

    local namespaced_resources=(
        serviceaccount/nvidia-device-plugin
        serviceaccount/kwok-gpu-device-plugin
        serviceaccount/mig-faker
        serviceaccount/status-exporter
        serviceaccount/status-updater
        serviceaccount/topology-server
        configmap/hostpath-init
        configmap/topology
        role/fake-kwok-gpu-device-plugin
        role/fake-status-updater
        rolebinding/fake-kwok-gpu-device-plugin
        rolebinding/fake-status-updater
        service/nvidia-dcgm-exporter
        service/topology-server
        daemonset/device-plugin
        daemonset/mig-faker
        daemonset/nvidia-dcgm-exporter
        deployment/gpu-operator
        deployment/kwok-gpu-device-plugin
        deployment/nvidia-dcgm-exporter
        deployment/status-updater
        deployment/topology-server
    )
    local cluster_resources=(
        clusterrole/fake-device-plugin
        clusterrole/fake-kwok-gpu-device-plugin
        clusterrole/mig-faker
        clusterrole/fake-status-exporter
        clusterrole/fake-status-updater
        clusterrole/topology-server
        clusterrolebinding/fake-device-plugin
        clusterrolebinding/fake-kwok-gpu-device-plugin
        clusterrolebinding/mig-faker
        clusterrolebinding/fake-status-exporter
        clusterrolebinding/fake-status-updater
        clusterrolebinding/topology-server
        runtimeclass/nvidia
    )

    local resource
    for resource in "${namespaced_resources[@]}"; do
        cleanup_stale_fake_gpu_operator_resource "$resource" gpu-operator
    done
    for resource in "${cluster_resources[@]}"; do
        cleanup_stale_fake_gpu_operator_resource "$resource"
    done

    local generated_resource
    for generated_resource in $(kubectl get configmap -n gpu-operator -l node-topology=true -o name 2>/dev/null || true); do
        cleanup_stale_fake_gpu_operator_resource "$generated_resource" gpu-operator
    done
}

cleanup_stale_fake_gpu_operator_resource() {
    local resource="$1"
    local namespace="${2:-}"
    local get_args=("$resource")
    local delete_args=("$resource")

    if [[ -n "$namespace" ]]; then
        get_args+=(-n "$namespace")
        delete_args+=(-n "$namespace")
    fi

    if ! kubectl get "${get_args[@]}" &> /dev/null; then
        return 0
    fi

    local managed_by
    local release_name
    local release_namespace
    local ownership_metadata

    ownership_metadata="$(get_helm_ownership_metadata "$resource" "$namespace")"
    IFS='|' read -r managed_by release_name release_namespace <<< "$ownership_metadata"

    if [[ "$managed_by" == "Helm" && "$release_name" == "gpu-operator" && "$release_namespace" == "gpu-operator" ]]; then
        log_info "Deleting stale fake GPU operator resource '${resource}' so Helm can reinstall it."
        kubectl delete "${delete_args[@]}" --ignore-not-found
        return 0
    fi

    if [[ "$managed_by" == "Helm" || -n "$release_name" || -n "$release_namespace" ]]; then
        log_error "Fake GPU operator resource '${resource}' exists but is owned by Helm release '${release_name:-unknown}' in namespace '${release_namespace:-unknown}'. Remove that release before running setup.sh."
        exit 1
    fi

    log_warn "Deleting stale fake GPU operator resource '${resource}' without Helm ownership metadata."
    kubectl delete "${delete_args[@]}" --ignore-not-found
}

cleanup_legacy_gateway_resource() {
    if ! kubectl get gateway nvcf-gateway -n envoy-gateway &> /dev/null; then
        return 0
    fi

    local managed_by
    local release_name
    local release_namespace
    local ownership_metadata
    local attached_routes

    ownership_metadata="$(get_helm_ownership_metadata gateway/nvcf-gateway envoy-gateway)"
    IFS='|' read -r managed_by release_name release_namespace <<< "$ownership_metadata"

    if [[ "$managed_by" == "Helm" || -n "$release_name" || -n "$release_namespace" ]]; then
        log_warn "Legacy Gateway 'envoy-gateway/nvcf-gateway' is Helm-owned. Leaving it in place."
        return 0
    fi

    attached_routes="$(kubectl get gateway nvcf-gateway -n envoy-gateway \
        -o jsonpath='{range .status.listeners[*]}{.attachedRoutes}{"\n"}{end}' 2>/dev/null |
        awk '{sum += $1} END {print sum + 0}')" || attached_routes="0"
    if [[ "$attached_routes" != "0" ]]; then
        log_warn "Legacy Gateway 'envoy-gateway/nvcf-gateway' has attached routes. Leaving it in place."
        return 0
    fi

    log_warn "Deleting unused legacy Gateway 'envoy-gateway/nvcf-gateway'. Local setup now uses 'envoy-gateway-system/shared-gw' and 'envoy-gateway-system/grpc-gw'."
    kubectl delete gateway nvcf-gateway -n envoy-gateway --ignore-not-found
}

# --- Pre-flight checks ---
log_info "Checking prerequisites..."
check_tool docker "Install from https://www.docker.com/get-started"
check_tool k3d "Install with: brew install k3d (or see https://k3d.io)"
check_tool kubectl "Install from https://kubernetes.io/docs/tasks/tools/"
check_tool helm "Install with: brew install helm (or see https://helm.sh)"
check_tool helmfile "Install with: brew install helmfile (or see https://helmfile.readthedocs.io)"

if ! helm plugin list 2>/dev/null | grep -q "^diff"; then
    log_error "helm-diff plugin is not installed. Install with: helm plugin install https://github.com/databus23/helm-diff"
    exit 1
fi

if ! docker info &> /dev/null; then
    log_error "Docker is not running. Please start Docker and try again."
    exit 1
fi

# --- Step 1: Create k3d cluster ---
log_info "========== STEP 1: CREATE K3D CLUSTER =========="
if k3d cluster get "$CLUSTER_NAME" &> /dev/null; then
    log_info "Cluster '$CLUSTER_NAME' already exists. Skipping creation."
    k3d cluster start "$CLUSTER_NAME" 2>/dev/null || true
else
    log_info "Creating k3d cluster '$CLUSTER_NAME'..."
    k3d cluster create --config "${SCRIPT_DIR}/k3d-config.yaml"
fi

k3d kubeconfig merge "$CLUSTER_NAME" --kubeconfig-switch-context > /dev/null 2>&1
log_info "kubectl context set to k3d-${CLUSTER_NAME}"

kubectl wait --for=condition=Ready nodes --all --timeout=120s > /dev/null 2>&1
log_info "All nodes are Ready."

# --- Step 2: Install KWOK + Fake GPU Operator ---
log_info "========== STEP 2: FAKE GPU OPERATOR =========="

if kubectl get deployment kwok-controller -n kube-system &> /dev/null; then
    log_info "KWOK controller already installed. Skipping."
else
    log_info "Installing KWOK controller..."
    kubectl apply -f https://github.com/kubernetes-sigs/kwok/releases/download/v0.7.0/kwok.yaml 2>&1 | grep -v "FlowSchema" || true
    kubectl wait --for=condition=Available deployment/kwok-controller -n kube-system --timeout=60s
    kubectl apply -f https://github.com/kubernetes-sigs/kwok/releases/download/v0.7.0/stage-fast.yaml
    log_info "KWOK controller is ready."
fi

if helm status gpu-operator -n gpu-operator &> /dev/null; then
    log_info "Fake GPU operator already installed. Skipping."
else
    log_info "Installing fake GPU operator..."
    helm repo add fake-gpu-operator \
        https://runai.jfrog.io/artifactory/api/helm/fake-gpu-operator-charts-prod --force-update > /dev/null 2>&1

    cleanup_stale_fake_gpu_operator_resources

    helm upgrade -i gpu-operator fake-gpu-operator/fake-gpu-operator \
        -n gpu-operator --create-namespace \
        --set 'topology.nodePools.default.gpuCount=8' \
        --set 'topology.nodePools.default.gpuProduct=NVIDIA-H100-80GB-HBM3' \
        --set 'topology.nodePools.default.gpuMemory=81559' \
        --wait --timeout=120s
    log_info "Fake GPU operator installed."
fi

log_info "Waiting for fake GPUs to appear on nodes..."
for i in {1..30}; do
    GPU_COUNT=$(kubectl get node -l run.ai/simulated-gpu-node-pool=default \
        -o jsonpath='{.items[0].status.allocatable.nvidia\.com/gpu}' 2>/dev/null || echo "")
    if [ -n "$GPU_COUNT" ] && [ "$GPU_COUNT" != "0" ]; then
        log_info "Fake GPUs detected: ${GPU_COUNT} per node."
        break
    fi
    if [ "$i" -eq 30 ]; then
        log_warn "Fake GPUs not yet visible after 60s. They may appear shortly."
        log_warn "Check with: kubectl get nodes -o custom-columns='NAME:.metadata.name,GPU:.status.allocatable.nvidia\\.com/gpu'"
    fi
    sleep 2
done

# --- Step 3: Install CSI SMB Driver ---
log_info "========== STEP 3: CSI SMB DRIVER =========="

if helm status csi-driver-smb -n kube-system &> /dev/null; then
    log_info "CSI SMB driver already installed. Skipping."
else
    log_info "Installing CSI SMB driver..."
    helm repo add csi-driver-smb \
        https://raw.githubusercontent.com/kubernetes-csi/csi-driver-smb/master/charts > /dev/null 2>&1

    helm install csi-driver-smb csi-driver-smb/csi-driver-smb \
        -n kube-system --version v1.17.0 --wait --timeout=120s
    log_info "CSI SMB driver installed."
fi

# --- Step 4: Install Envoy Gateway ---
log_info "========== STEP 4: ENVOY GATEWAY =========="

if helm status eg -n envoy-gateway-system &> /dev/null; then
    log_info "Envoy Gateway already installed. Skipping."
else
    log_info "Installing Envoy Gateway..."
    helm install eg oci://docker.io/envoyproxy/gateway-helm \
        --version v1.5.4 \
        -n envoy-gateway-system --create-namespace \
        --wait --timeout=120s
    log_info "Envoy Gateway installed."
fi

log_info "Applying GatewayClass..."
kubectl apply -f - <<'EOF'
apiVersion: gateway.networking.k8s.io/v1
kind: GatewayClass
metadata:
  name: eg
spec:
  controllerName: gateway.envoyproxy.io/gatewayclass-controller
EOF

log_info "Creating namespaces and labeling for Gateway route access..."
for ns in envoy-gateway-system envoy-gateway api-keys ess sis nvcf; do
    kubectl create namespace "$ns" --dry-run=client -o yaml | kubectl apply -f - > /dev/null 2>&1
    kubectl label namespace "$ns" nvcf/platform=true --overwrite > /dev/null 2>&1
done

cleanup_legacy_gateway_resource

log_info "Applying Gateway resources..."
kubectl apply -f - <<'EOF'
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: shared-gw
  namespace: envoy-gateway-system
spec:
  gatewayClassName: eg
  listeners:
  - name: http
    protocol: HTTP
    port: 80
    allowedRoutes:
      namespaces:
        from: All
---
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: grpc-gw
  namespace: envoy-gateway-system
spec:
  gatewayClassName: eg
  listeners:
  - name: tcp
    protocol: TCP
    port: 10081
    allowedRoutes:
      namespaces:
        from: All
EOF

for gateway in shared-gw grpc-gw; do
    log_info "Waiting for Gateway '${gateway}' to be programmed..."
    kubectl wait --for=condition=Programmed "gateway/${gateway}" -n envoy-gateway-system --timeout=120s 2>/dev/null || \
        log_warn "Gateway '${gateway}' not yet programmed. It may take a moment."
done

# --- Summary ---
echo ""
log_info "=========================================="
log_info "  Local cluster is ready!"
log_info "=========================================="
echo ""
log_info "Cluster:      k3d-${CLUSTER_NAME}"
log_info "Nodes:        6 (1 server + 5 agents)"
log_info "Fake GPUs:    8x H100 on 2 nodes"
log_info "Gateways:     shared-gw and grpc-gw (envoy-gateway-system namespace)"
echo ""
log_info "Next steps:"
log_info "  1. Configure secrets/local-secrets.yaml with your NGC credentials"
log_info "  2. Set up image pull secrets (Kyverno recommended)"
log_info "  3. Run: HELMFILE_ENV=local helmfile sync"
echo ""
log_info "See the self-hosted control plane installation guide for details:"
log_info "  https://docs.nvidia.com/cloud-functions/self-hosted/latest/control-plane-installation.html"
echo ""
