#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

#
# NVSNAP Agent Installation Script
# Installs nvsnap-agent as a systemd service on GPU nodes
#
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

log_info() { echo -e "${GREEN}[INFO]${NC} $1"; }
log_warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }
log_error() { echo -e "${RED}[ERROR]${NC} $1"; exit 1; }

# Check if running as root
if [ "$EUID" -ne 0 ]; then
    log_error "Please run as root (sudo)"
fi

echo "╔═══════════════════════════════════════════════════════════════╗"
echo "║           NVSNAP Agent Installation                            ║"
echo "╚═══════════════════════════════════════════════════════════════╝"
echo ""

# Check prerequisites
log_info "Checking prerequisites..."

# Check NVIDIA driver
if ! command -v nvidia-smi &>/dev/null; then
    log_error "NVIDIA driver not found"
fi
DRIVER_VER=$(nvidia-smi --query-gpu=driver_version --format=csv,noheader | head -1)
log_info "NVIDIA driver: $DRIVER_VER"

# Check cuda-checkpoint
if ! command -v cuda-checkpoint &>/dev/null; then
    log_warn "cuda-checkpoint not found in PATH"
    if [ -f /usr/local/bin/cuda-checkpoint ]; then
        log_info "Found cuda-checkpoint at /usr/local/bin/cuda-checkpoint"
    else
        log_error "cuda-checkpoint not found. Please install NVIDIA cuda-checkpoint tool."
    fi
fi

# Check CRIU
CRIU_PATH=""
if command -v criu &>/dev/null; then
    CRIU_PATH=$(which criu)
elif [ -f /usr/local/sbin/criu ]; then
    CRIU_PATH=/usr/local/sbin/criu
elif [ -f /usr/sbin/criu ]; then
    CRIU_PATH=/usr/sbin/criu
else
    log_error "CRIU not found. Please install CRIU."
fi
log_info "CRIU: $CRIU_PATH"

# Check containerd
if ! command -v crictl &>/dev/null; then
    log_warn "crictl not found - container discovery may not work"
fi

# Create directories
log_info "Creating directories..."
mkdir -p /var/lib/nvsnap/checkpoints
mkdir -p /etc/nvsnap

# Install binary
log_info "Installing nvsnap-agent binary..."
if [ -f "$PROJECT_ROOT/bin/nvsnap-agent" ]; then
    cp "$PROJECT_ROOT/bin/nvsnap-agent" /usr/local/bin/nvsnap-agent
elif [ -f "./nvsnap-agent" ]; then
    cp ./nvsnap-agent /usr/local/bin/nvsnap-agent
else
    log_error "nvsnap-agent binary not found. Build with 'make build' first."
fi
chmod +x /usr/local/bin/nvsnap-agent

# Create config
log_info "Creating configuration..."
cat > /etc/nvsnap/agent.yaml << EOF
# NVSNAP Agent Configuration
checkpoint_dir: /var/lib/nvsnap/checkpoints
criu_path: $CRIU_PATH
cuda_checkpoint_path: /usr/local/bin/cuda-checkpoint
listen: :8081
log_level: info
EOF

# Install systemd service
log_info "Installing systemd service..."
cat > /etc/systemd/system/nvsnap-agent.service << EOF
[Unit]
Description=NVSNAP Agent - GPU Checkpoint/Restore
Documentation=https://github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap
After=network.target containerd.service nvidia-persistenced.service
Wants=containerd.service

[Service]
Type=simple
ExecStart=/usr/local/bin/nvsnap-agent \\
    --checkpoint-dir=/var/lib/nvsnap/checkpoints \\
    --criu-path=$CRIU_PATH \\
    --cuda-checkpoint-path=/usr/local/bin/cuda-checkpoint \\
    --listen=:8081 \\
    --log-level=info

Restart=always
RestartSec=5

Environment="PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

StandardOutput=journal
StandardError=journal
SyslogIdentifier=nvsnap-agent

[Install]
WantedBy=multi-user.target
EOF

# Reload and start
log_info "Starting nvsnap-agent service..."
systemctl daemon-reload
systemctl enable nvsnap-agent
systemctl restart nvsnap-agent

# Verify
sleep 2
if systemctl is-active --quiet nvsnap-agent; then
    log_info "nvsnap-agent is running!"
else
    log_error "nvsnap-agent failed to start. Check: journalctl -u nvsnap-agent"
fi

# Test health endpoint
if curl -s http://localhost:8081/health &>/dev/null; then
    log_info "Agent health check passed!"
else
    log_warn "Agent health check failed - may still be starting"
fi

echo ""
echo "═══════════════════════════════════════════════════════════════"
echo "Installation complete!"
echo ""
echo "Commands:"
echo "  Status:  systemctl status nvsnap-agent"
echo "  Logs:    journalctl -u nvsnap-agent -f"
echo "  Test:    curl http://localhost:8081/health"
echo ""
echo "Agent API available at: http://$(hostname -I | awk '{print $1}'):8081"
echo "═══════════════════════════════════════════════════════════════"
