#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# NVSNAP Node Installation Script
#
# Installs all required components on a Kubernetes node:
# - NVIDIA drivers (open kernel modules for newer GPUs)
# - NVIDIA Container Toolkit with CDI
# - CRIU (built from source)
# - cuda-checkpoint
#
# Usage:
#   sudo ./install-node.sh [driver|toolkit|criu|cuda-checkpoint|all]

set -euo pipefail

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

log() { echo -e "${GREEN}[INFO]${NC} $1"; }
warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }
error() { echo -e "${RED}[ERROR]${NC} $1"; exit 1; }

check_root() {
    if [[ $EUID -ne 0 ]]; then
        error "This script must be run as root (use sudo)"
    fi
}

install_nvidia_driver() {
    log "Installing NVIDIA driver..."
    
    # Check if already installed
    if command -v nvidia-smi &>/dev/null; then
        log "NVIDIA driver already installed:"
        nvidia-smi --query-gpu=name --format=csv,noheader
        return 0
    fi
    
    apt-get update
    
    # Check if GPU needs open kernel modules
    GPU_NEEDS_OPEN=$(lspci | grep -i nvidia | grep -E "2bbc|2f3f" || true)
    
    if [[ -n "$GPU_NEEDS_OPEN" ]]; then
        log "Installing nvidia-driver-570-open (required for newer GPUs)"
        apt-get install -y nvidia-driver-570-open
    else
        log "Installing nvidia-driver-570"
        apt-get install -y nvidia-driver-570
    fi
    
    log "NVIDIA driver installed. Reboot may be required."
}

install_container_toolkit() {
    log "Installing NVIDIA Container Toolkit..."
    
    # Add repo
    curl -fsSL https://nvidia.github.io/libnvidia-container/gpgkey | \
        gpg --dearmor -o /usr/share/keyrings/nvidia-container-toolkit-keyring.gpg
    
    curl -s -L https://nvidia.github.io/libnvidia-container/stable/deb/nvidia-container-toolkit.list | \
        sed 's#deb https://#deb [signed-by=/usr/share/keyrings/nvidia-container-toolkit-keyring.gpg] https://#g' \
        > /etc/apt/sources.list.d/nvidia-container-toolkit.list
    
    apt-get update
    apt-get install -y nvidia-container-toolkit
    
    # Generate CDI spec
    mkdir -p /etc/cdi
    nvidia-ctk cdi generate --output=/etc/cdi/nvidia.yaml
    
    # Configure containerd
    nvidia-ctk runtime configure --runtime=containerd
    
    log "NVIDIA Container Toolkit installed"
    nvidia-ctk cdi list
}

configure_containerd_nvidia() {
    log "Configuring containerd with NVIDIA runtime..."
    
    cat > /etc/containerd/config.toml << 'EOF'
version = 3

[plugins]
  [plugins."io.containerd.cri.v1.runtime"]
    [plugins."io.containerd.cri.v1.runtime".containerd]
      default_runtime_name = "nvidia"
      [plugins."io.containerd.cri.v1.runtime".containerd.runtimes]
        [plugins."io.containerd.cri.v1.runtime".containerd.runtimes.nvidia]
          privileged_without_host_devices = false
          runtime_engine = ""
          runtime_root = ""
          runtime_type = "io.containerd.runc.v2"
          [plugins."io.containerd.cri.v1.runtime".containerd.runtimes.nvidia.options]
            BinaryName = "/usr/bin/nvidia-container-runtime"
            SystemdCgroup = true
  [plugins."io.containerd.grpc.v1.cri"]
    sandbox_image = "registry.k8s.io/pause:3.10"
    enable_cdi = true
    cdi_spec_dirs = ["/etc/cdi", "/var/run/cdi"]
EOF

    systemctl restart containerd
    log "Containerd configured"
}

install_criu() {
    log "Installing CRIU from source..."
    
    # Check if already installed
    if command -v criu &>/dev/null; then
        log "CRIU already installed: $(criu --version)"
        return 0
    fi
    
    # Install dependencies
    apt-get update
    apt-get install -y \
        build-essential \
        libprotobuf-dev \
        libprotobuf-c-dev \
        protobuf-c-compiler \
        protobuf-compiler \
        python3-protobuf \
        libnl-3-dev \
        libcap-dev \
        uuid-dev \
        libaio-dev \
        libnet1-dev \
        pkg-config \
        git
    
    # Clone and build
    cd /tmp
    rm -rf criu
    git clone --depth 1 https://github.com/checkpoint-restore/criu.git
    cd criu
    make -j$(nproc)
    make install-criu install-lib install-compel SKIP_DOC=1
    
    log "CRIU installed: $(criu --version)"
}

install_cuda_checkpoint() {
    log "Installing cuda-checkpoint..."
    
    # Check for local copy first
    LOCAL_CUDA_CKPT="${CUDA_CHECKPOINT_BIN:-../cuda-checkpoint/bin/x86_64_Linux/cuda-checkpoint}"
    
    if [[ -f "$LOCAL_CUDA_CKPT" ]]; then
        cp "$LOCAL_CUDA_CKPT" /usr/local/bin/cuda-checkpoint
        chmod +x /usr/local/bin/cuda-checkpoint
        log "cuda-checkpoint installed from local path"
    else
        warn "cuda-checkpoint not found at $LOCAL_CUDA_CKPT"
        warn "Please copy cuda-checkpoint to /usr/local/bin/ manually"
        return 1
    fi
    
    cuda-checkpoint --help 2>&1 | head -3
}

enable_checkpoint_feature() {
    log "Enabling ContainerCheckpoint feature gate..."
    
    mkdir -p /etc/systemd/system/kubelet.service.d
    cat > /etc/systemd/system/kubelet.service.d/20-feature-gates.conf << 'EOF'
[Service]
Environment="KUBELET_EXTRA_ARGS=--feature-gates=ContainerCheckpoint=true"
EOF

    systemctl daemon-reload
    systemctl restart kubelet
    
    log "ContainerCheckpoint feature gate enabled"
}

show_status() {
    echo ""
    echo "=========================================="
    echo "  Node Status"
    echo "=========================================="
    
    echo -n "NVIDIA Driver: "
    nvidia-smi --query-gpu=name,driver_version --format=csv,noheader 2>/dev/null || echo "Not installed"
    
    echo -n "Container Toolkit: "
    nvidia-ctk --version 2>/dev/null || echo "Not installed"
    
    echo -n "CRIU: "
    criu --version 2>/dev/null || echo "Not installed"
    
    echo -n "cuda-checkpoint: "
    cuda-checkpoint --help 2>&1 | head -1 || echo "Not installed"
    
    echo ""
}

main() {
    check_root
    
    case "${1:-all}" in
        driver)
            install_nvidia_driver
            ;;
        toolkit)
            install_container_toolkit
            configure_containerd_nvidia
            ;;
        criu)
            install_criu
            ;;
        cuda-checkpoint)
            install_cuda_checkpoint
            ;;
        feature)
            enable_checkpoint_feature
            ;;
        status)
            show_status
            ;;
        all)
            install_nvidia_driver
            install_container_toolkit
            configure_containerd_nvidia
            install_criu
            install_cuda_checkpoint
            enable_checkpoint_feature
            show_status
            ;;
        *)
            echo "Usage: $0 {driver|toolkit|criu|cuda-checkpoint|feature|status|all}"
            ;;
    esac
}

main "$@"
