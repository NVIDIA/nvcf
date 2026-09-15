#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Create a local kind cluster for NVSNAP development
# Supports both GPU and non-GPU development modes

set -euo pipefail

CLUSTER_NAME="${CLUSTER_NAME:-nvsnap}"
K8S_VERSION="${K8S_VERSION:-v1.29.2}"
GPU_MODE="${GPU_MODE:-false}"  # Set to 'true' if you have a GPU

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

log_info() { echo -e "${BLUE}[INFO]${NC} $1"; }
log_success() { echo -e "${GREEN}[SUCCESS]${NC} $1"; }
log_warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }
log_error() { echo -e "${RED}[ERROR]${NC} $1"; }

# Check prerequisites
check_prerequisites() {
    log_info "Checking prerequisites..."
    
    if ! command -v kind &> /dev/null; then
        log_error "kind is not installed. Install from: https://kind.sigs.k8s.io/docs/user/quick-start/#installation"
        exit 1
    fi
    
    if ! command -v kubectl &> /dev/null; then
        log_error "kubectl is not installed"
        exit 1
    fi
    
    if ! command -v helm &> /dev/null; then
        log_error "helm is not installed"
        exit 1
    fi
    
    if ! docker info &> /dev/null; then
        log_error "Docker is not running"
        exit 1
    fi
    
    log_success "All prerequisites met"
}

# Check if cluster already exists
check_existing_cluster() {
    if kind get clusters 2>/dev/null | grep -q "^${CLUSTER_NAME}$"; then
        log_warn "Cluster '$CLUSTER_NAME' already exists"
        read -p "Delete and recreate? [y/N] " -n 1 -r
        echo
        if [[ $REPLY =~ ^[Yy]$ ]]; then
            log_info "Deleting existing cluster..."
            kind delete cluster --name "$CLUSTER_NAME"
        else
            log_info "Using existing cluster"
            exit 0
        fi
    fi
}

# Create kind cluster configuration
create_cluster_config() {
    log_info "Creating cluster configuration..."
    
    if [[ "$GPU_MODE" == "true" ]]; then
        # GPU-enabled configuration
        cat <<EOF
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
name: ${CLUSTER_NAME}
nodes:
- role: control-plane
  image: kindest/node:${K8S_VERSION}
  kubeadmConfigPatches:
  - |
    kind: InitConfiguration
    nodeRegistration:
      kubeletExtraArgs:
        node-labels: "ingress-ready=true"
  extraPortMappings:
  - containerPort: 80
    hostPort: 80
    protocol: TCP
  - containerPort: 443
    hostPort: 443
    protocol: TCP
  - containerPort: 30080
    hostPort: 30080
    protocol: TCP
- role: worker
  image: kindest/node:${K8S_VERSION}
  extraMounts:
  - hostPath: /dev/null
    containerPath: /dev/null
  # GPU passthrough mounts
  - hostPath: /dev/nvidia0
    containerPath: /dev/nvidia0
  - hostPath: /dev/nvidiactl
    containerPath: /dev/nvidiactl
  - hostPath: /dev/nvidia-uvm
    containerPath: /dev/nvidia-uvm
  - hostPath: /dev/nvidia-uvm-tools
    containerPath: /dev/nvidia-uvm-tools
- role: worker
  image: kindest/node:${K8S_VERSION}
containerdConfigPatches:
- |-
  [plugins."io.containerd.grpc.v1.cri".containerd.runtimes.nvidia]
    runtime_type = "io.containerd.runc.v2"
    [plugins."io.containerd.grpc.v1.cri".containerd.runtimes.nvidia.options]
      BinaryName = "/usr/bin/nvidia-container-runtime"
EOF
    else
        # Non-GPU configuration
        cat <<EOF
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
name: ${CLUSTER_NAME}
nodes:
- role: control-plane
  image: kindest/node:${K8S_VERSION}
  kubeadmConfigPatches:
  - |
    kind: InitConfiguration
    nodeRegistration:
      kubeletExtraArgs:
        node-labels: "ingress-ready=true"
  extraPortMappings:
  - containerPort: 80
    hostPort: 80
    protocol: TCP
  - containerPort: 443
    hostPort: 443
    protocol: TCP
  - containerPort: 30080
    hostPort: 30080
    protocol: TCP
- role: worker
  image: kindest/node:${K8S_VERSION}
- role: worker
  image: kindest/node:${K8S_VERSION}
EOF
    fi
}

# Create the cluster
create_cluster() {
    log_info "Creating kind cluster '$CLUSTER_NAME'..."
    
    create_cluster_config | kind create cluster --config=-
    
    log_success "Cluster created successfully"
}

# Install required components
install_components() {
    log_info "Installing required components..."
    
    # Install metrics-server
    log_info "Installing metrics-server..."
    kubectl apply -f https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml
    kubectl patch deployment metrics-server -n kube-system --type='json' \
        -p='[{"op": "add", "path": "/spec/template/spec/containers/0/args/-", "value": "--kubelet-insecure-tls"}]'
    
    # Install ingress-nginx
    log_info "Installing ingress-nginx..."
    kubectl apply -f https://raw.githubusercontent.com/kubernetes/ingress-nginx/main/deploy/static/provider/kind/deploy.yaml
    
    # Wait for ingress to be ready
    kubectl wait --namespace ingress-nginx \
        --for=condition=ready pod \
        --selector=app.kubernetes.io/component=controller \
        --timeout=90s || true
    
    # Install cert-manager (for webhook certificates)
    log_info "Installing cert-manager..."
    kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.14.4/cert-manager.yaml
    kubectl wait --namespace cert-manager \
        --for=condition=ready pod \
        --selector=app.kubernetes.io/instance=cert-manager \
        --timeout=90s || true
    
    log_success "Components installed"
}

# Install MinIO for S3-compatible storage
install_minio() {
    log_info "Installing MinIO for S3 storage..."
    
    helm repo add minio https://charts.min.io/ 2>/dev/null || true
    helm repo update
    
    helm upgrade --install minio minio/minio \
        --namespace minio \
        --create-namespace \
        --set mode=standalone \
        --set replicas=1 \
        --set persistence.enabled=false \
        --set resources.requests.memory=256Mi \
        --set rootUser=minioadmin \
        --set rootPassword=minioadmin \
        --set service.type=NodePort \
        --set consoleService.type=NodePort \
        --wait
    
    # Create test bucket
    kubectl run minio-client --rm -i --restart=Never \
        --image=minio/mc:latest \
        --command -- /bin/sh -c "
            mc alias set myminio http://minio.minio.svc.cluster.local:9000 minioadmin minioadmin &&
            mc mb myminio/nvsnap-checkpoints --ignore-existing
        " || true
    
    log_success "MinIO installed (access: minioadmin/minioadmin)"
}

# Install GPU operator (if GPU mode)
install_gpu_operator() {
    if [[ "$GPU_MODE" != "true" ]]; then
        log_warn "Skipping GPU operator installation (GPU_MODE=$GPU_MODE)"
        return
    fi
    
    log_info "Installing NVIDIA GPU Operator..."
    
    helm repo add nvidia https://helm.ngc.nvidia.com/nvidia 2>/dev/null || true
    helm repo update
    
    helm upgrade --install gpu-operator nvidia/gpu-operator \
        --namespace gpu-operator \
        --create-namespace \
        --set driver.enabled=false \
        --set toolkit.enabled=true \
        --set devicePlugin.enabled=true \
        --wait
    
    log_success "GPU Operator installed"
}

# Print cluster info
print_info() {
    echo ""
    echo -e "${GREEN}========================================${NC}"
    echo -e "${GREEN} NVSNAP Development Cluster Ready${NC}"
    echo -e "${GREEN}========================================${NC}"
    echo ""
    echo "Cluster Name: $CLUSTER_NAME"
    echo "K8s Version:  $K8S_VERSION"
    echo "GPU Mode:     $GPU_MODE"
    echo ""
    echo "Nodes:"
    kubectl get nodes -o wide
    echo ""
    echo "MinIO:"
    echo "  Endpoint: http://localhost:$(kubectl get svc minio -n minio -o jsonpath='{.spec.ports[0].nodePort}')"
    echo "  Console:  http://localhost:$(kubectl get svc minio-console -n minio -o jsonpath='{.spec.ports[0].nodePort}' 2>/dev/null || echo 'N/A')"
    echo "  Credentials: minioadmin / minioadmin"
    echo ""
    echo "Next steps:"
    echo "  1. Build NVSNAP:     make build"
    echo "  2. Build images:    make docker-build"
    echo "  3. Load images:     kind load docker-image ghcr.io/nvsnap/nvsnap-controller:dev --name $CLUSTER_NAME"
    echo "  4. Deploy:          helm install nvsnap deploy/helm/nvsnap --namespace nvsnap-system --create-namespace"
    echo ""
}

# Main
main() {
    echo ""
    echo -e "${BLUE}╔════════════════════════════════════════════════════════════╗${NC}"
    echo -e "${BLUE}║          NVSNAP Development Cluster Setup                   ║${NC}"
    echo -e "${BLUE}╚════════════════════════════════════════════════════════════╝${NC}"
    echo ""
    
    check_prerequisites
    check_existing_cluster
    create_cluster
    install_components
    install_minio
    install_gpu_operator
    print_info
}

main "$@"
