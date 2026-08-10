#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# NVSNAP Test Cluster Setup
#
# Sets up a Kubernetes cluster with ContainerCheckpoint feature gate enabled
# for testing both CRI and CRIU checkpoint backends.
#
# Nodes:
#   - 10.34.5.64 (control plane)
#   - 10.86.2.83 (worker)
#   - 10.86.6.104 (worker)

set -e

# Configuration
CONTROL_PLANE="10.34.5.64"
WORKERS=("10.86.2.83" "10.86.6.104")
ALL_NODES=("$CONTROL_PLANE" "${WORKERS[@]}")

# Credentials from environment or config file
CONFIG_FILE="${HOME}/.nvsnap/credentials"
SSH_OPTS="-o StrictHostKeyChecking=no -o PreferredAuthentications=password -o PubkeyAuthentication=no -o ConnectTimeout=30"

INSTALL_SCRIPT="${INSTALL_K8S_SCRIPT:?required: path to your install-k8s-cluster.sh}"

load_credentials() {
    if [[ -f "$CONFIG_FILE" ]]; then
        source "$CONFIG_FILE"
    fi
    
    if [[ -z "${SSH_USER:-}" || -z "${SSH_PASS:-}" ]]; then
        echo "Error: SSH_USER and SSH_PASS must be set"
        echo "Either export them or create $CONFIG_FILE with:"
        echo "  SSH_USER=your_username"
        echo "  SSH_PASS=your_password"
        exit 1
    fi
}
KUBECONFIG_LOCAL="$HOME/.kube/configs/nvsnap-test-cluster"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

log() { echo -e "${GREEN}[INFO]${NC} $1"; }
warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }
error() { echo -e "${RED}[ERROR]${NC} $1"; exit 1; }
step() { echo -e "\n${BLUE}==== $1 ====${NC}\n"; }

# SSH helper
ssh_cmd() {
    local host=$1
    shift
    sshpass -p "$SSH_PASS" ssh $SSH_OPTS "$SSH_USER@$host" "$@"
}

scp_cmd() {
    local src=$1
    local host=$2
    local dst=$3
    sshpass -p "$SSH_PASS" scp $SSH_OPTS "$src" "$SSH_USER@$host:$dst"
}

# Check connectivity
check_connectivity() {
    step "Checking connectivity to all nodes"
    load_credentials
    for node in "${ALL_NODES[@]}"; do
        log "Testing $node..."
        if ssh_cmd "$node" "hostname && uname -a" 2>/dev/null; then
            log "  ✓ $node reachable"
        else
            error "Cannot reach $node"
        fi
    done
}

# Copy install script to all nodes
copy_scripts() {
    step "Copying install script to all nodes"
    for node in "${ALL_NODES[@]}"; do
        log "Copying to $node..."
        scp_cmd "$INSTALL_SCRIPT" "$node" "/tmp/install-k8s-cluster.sh"
        ssh_cmd "$node" "chmod +x /tmp/install-k8s-cluster.sh"
    done
}

# Create kubelet config with feature gates
create_kubelet_config() {
    cat << 'EOF'
apiVersion: kubelet.config.k8s.io/v1beta1
kind: KubeletConfiguration
featureGates:
  ContainerCheckpoint: true
cgroupDriver: systemd
EOF
}

# Create kubeadm config with feature gates
create_kubeadm_config() {
    local control_plane_ip=$1
    cat << EOF
apiVersion: kubeadm.k8s.io/v1beta4
kind: InitConfiguration
nodeRegistration:
  criSocket: unix:///run/containerd/containerd.sock
localAPIEndpoint:
  advertiseAddress: $control_plane_ip
---
apiVersion: kubeadm.k8s.io/v1beta4
kind: ClusterConfiguration
kubernetesVersion: v1.32.0
networking:
  podSubnet: 10.244.0.0/16
apiServer:
  extraArgs:
    - name: feature-gates
      value: ContainerCheckpoint=true
controllerManager:
  extraArgs:
    - name: feature-gates
      value: ContainerCheckpoint=true
scheduler:
  extraArgs:
    - name: feature-gates
      value: ContainerCheckpoint=true
---
apiVersion: kubelet.config.k8s.io/v1beta1
kind: KubeletConfiguration
featureGates:
  ContainerCheckpoint: true
cgroupDriver: systemd
EOF
}

# Initialize control plane
init_control_plane() {
    step "Initializing control plane on $CONTROL_PLANE"
    
    # First, run base installation
    log "Running base installation..."
    ssh_cmd "$CONTROL_PLANE" "echo '$SSH_PASS' | sudo -S /tmp/install-k8s-cluster.sh init 2>&1" || {
        warn "Init may have partially failed, checking status..."
    }
    
    # Wait for API server
    log "Waiting for API server..."
    sleep 30
    
    # Get join command
    log "Getting join command..."
    JOIN_CMD=$(ssh_cmd "$CONTROL_PLANE" "echo '$SSH_PASS' | sudo -S kubeadm token create --print-join-command 2>/dev/null")
    echo "Join command: $JOIN_CMD"
    
    # Copy kubeconfig locally
    log "Copying kubeconfig..."
    mkdir -p "$(dirname $KUBECONFIG_LOCAL)"
    ssh_cmd "$CONTROL_PLANE" "echo '$SSH_PASS' | sudo -S cat /etc/kubernetes/admin.conf" > "$KUBECONFIG_LOCAL"
    chmod 600 "$KUBECONFIG_LOCAL"
    
    log "Control plane initialized"
    echo "$JOIN_CMD" > /tmp/k8s-join-command.txt
}

# Join workers
join_workers() {
    step "Joining worker nodes"
    
    JOIN_CMD=$(cat /tmp/k8s-join-command.txt)
    if [[ -z "$JOIN_CMD" ]]; then
        error "No join command found"
    fi
    
    for worker in "${WORKERS[@]}"; do
        log "Joining $worker..."
        
        # Run base installation first (without init)
        ssh_cmd "$worker" "echo '$SSH_PASS' | sudo -S bash -c '
            # Disable swap
            swapoff -a
            sed -i \"/\\sswap\\s/s/^/#/\" /etc/fstab
            
            # Load modules
            modprobe overlay
            modprobe br_netfilter
            
            # Check if already joined
            if kubectl get nodes 2>/dev/null | grep -q \$(hostname); then
                echo \"Already joined\"
                exit 0
            fi
            
            # Install if needed
            if ! command -v kubeadm &>/dev/null; then
                /tmp/install-k8s-cluster.sh join \"placeholder\"
            fi
        '" 2>&1 || true
        
        # Join the cluster
        ssh_cmd "$worker" "echo '$SSH_PASS' | sudo -S $JOIN_CMD" 2>&1 || {
            warn "Join may have failed for $worker, continuing..."
        }
    done
}

# Setup GPU on all nodes
setup_gpu() {
    step "Setting up GPU support on all nodes"
    
    for node in "${ALL_NODES[@]}"; do
        log "Setting up GPU on $node..."
        ssh_cmd "$node" "echo '$SSH_PASS' | sudo -S /tmp/install-k8s-cluster.sh gpu" 2>&1 || {
            warn "GPU setup may have issues on $node"
        }
    done
}

# Install NVIDIA device plugin
install_device_plugin() {
    step "Installing NVIDIA device plugin"
    
    export KUBECONFIG="$KUBECONFIG_LOCAL"
    kubectl apply -f https://raw.githubusercontent.com/NVIDIA/k8s-device-plugin/v0.17.0/deployments/static/nvidia-device-plugin.yml
}

# Enable ContainerCheckpoint feature gate (requires kubelet restart)
enable_checkpoint_feature() {
    step "Enabling ContainerCheckpoint feature gate"
    
    for node in "${ALL_NODES[@]}"; do
        log "Enabling on $node..."
        ssh_cmd "$node" "echo '$SSH_PASS' | sudo -S bash -c '
            # Create kubelet config drop-in
            mkdir -p /etc/systemd/system/kubelet.service.d
            cat > /etc/systemd/system/kubelet.service.d/20-feature-gates.conf << EOF
[Service]
Environment=\"KUBELET_EXTRA_ARGS=--feature-gates=ContainerCheckpoint=true\"
EOF
            
            # Restart kubelet
            systemctl daemon-reload
            systemctl restart kubelet
        '" 2>&1
    done
    
    log "Feature gate enabled on all nodes"
}

# Deploy NVSNAP components
deploy_nvsnap() {
    step "Deploying NVSNAP components"
    
    export KUBECONFIG="$KUBECONFIG_LOCAL"
    
    # Create namespace
    kubectl create namespace nvsnap-system --dry-run=client -o yaml | kubectl apply -f -
    
    # Create image pull secret
    kubectl create secret generic nvcr-pull-secret \
        --from-file=.dockerconfigjson=$HOME/.docker/config.json \
        --type=kubernetes.io/dockerconfigjson \
        -n nvsnap-system \
        --dry-run=client -o yaml | kubectl apply -f -
    
    # Deploy NVSNAP agent
    kubectl apply -f "$(git -C "$(dirname "${BASH_SOURCE[0]}")" rev-parse --show-toplevel)/deploy/k8s/agent-daemonset.yaml"
    
    log "NVSNAP components deployed"
}

# Print final status
print_status() {
    step "Cluster Status"
    
    export KUBECONFIG="$KUBECONFIG_LOCAL"
    
    echo "Nodes:"
    kubectl get nodes -o wide
    echo ""
    echo "Pods:"
    kubectl get pods -A
    echo ""
    echo "GPU Resources:"
    kubectl describe nodes | grep -A 5 "Allocated resources" | head -20
}

# Main
main() {
    case "${1:-all}" in
        check)
            check_connectivity
            ;;
        copy)
            copy_scripts
            ;;
        init)
            init_control_plane
            ;;
        join)
            join_workers
            ;;
        gpu)
            setup_gpu
            ;;
        feature)
            enable_checkpoint_feature
            ;;
        nvsnap)
            deploy_nvsnap
            ;;
        status)
            print_status
            ;;
        all)
            check_connectivity
            copy_scripts
            init_control_plane
            join_workers
            setup_gpu
            install_device_plugin
            enable_checkpoint_feature
            deploy_nvsnap
            print_status
            ;;
        *)
            echo "Usage: $0 {check|copy|init|join|gpu|feature|nvsnap|status|all}"
            echo ""
            echo "Steps:"
            echo "  check   - Test connectivity to all nodes"
            echo "  copy    - Copy install script to nodes"
            echo "  init    - Initialize control plane"
            echo "  join    - Join worker nodes"
            echo "  gpu     - Setup GPU support"
            echo "  feature - Enable ContainerCheckpoint feature gate"
            echo "  nvsnap   - Deploy NVSNAP components"
            echo "  status  - Show cluster status"
            echo "  all     - Run all steps"
            ;;
    esac
}

main "$@"
