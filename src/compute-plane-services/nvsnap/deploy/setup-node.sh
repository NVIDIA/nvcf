#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

#
# NVSNAP Node Setup Script
# Installs all required components on GPU nodes:
#   - Forked CRIU (with NVIDIA fixes)
#   - cuda-checkpoint
#   - nvsnap-agent (systemd service)
#
# Usage:
#   ./setup-node.sh                    # Run locally on the node
#   ./setup-node.sh --remote <node-ip> # Install on remote node
#
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

log_info() { echo -e "${GREEN}[INFO]${NC} $1"; }
log_warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }
log_error() { echo -e "${RED}[ERROR]${NC} $1"; }
log_step() { echo -e "${BLUE}[STEP $1]${NC} $2"; }

# Load credentials if available
load_credentials() {
    if [[ -f "$HOME/.nvsnap/credentials" ]]; then
        source "$HOME/.nvsnap/credentials"
    fi
}

# Remote installation
install_remote() {
    local NODE_IP="$1"
    load_credentials
    
    if [[ -z "$SSH_USER" || -z "$SSH_PASS" ]]; then
        log_error "SSH_USER and SSH_PASS required. Set in ~/.nvsnap/credentials"
        exit 1
    fi
    
    log_info "Installing on remote node: $NODE_IP"
    
    # Create temp directory for files
    local TEMP_DIR=$(mktemp -d)
    trap "rm -rf $TEMP_DIR" EXIT
    
    # Gather all files
    log_step 1 "Gathering installation files..."
    
    # CRIU fork
    if [[ -f "$PROJECT_ROOT/../criu-orig/criu/criu/criu" ]]; then
        cp "$PROJECT_ROOT/../criu-orig/criu/criu/criu" "$TEMP_DIR/criu"
        log_info "  Found forked CRIU"
    else
        log_error "Forked CRIU not found at $PROJECT_ROOT/../criu-orig/criu/criu/criu"
        exit 1
    fi
    
    # cuda-checkpoint (should already be on nodes, but include if available)
    if [[ -f "/usr/local/bin/cuda-checkpoint" ]]; then
        cp /usr/local/bin/cuda-checkpoint "$TEMP_DIR/"
        log_info "  Found cuda-checkpoint"
    fi
    
    # nvsnap-agent
    if [[ -f "$PROJECT_ROOT/bin/nvsnap-agent" ]]; then
        cp "$PROJECT_ROOT/bin/nvsnap-agent" "$TEMP_DIR/"
        log_info "  Found nvsnap-agent"
    else
        log_warn "nvsnap-agent not found. Build with 'make agent' first."
    fi
    
    # Create install script
    cat > "$TEMP_DIR/install.sh" << 'INSTALL_SCRIPT'
#!/bin/bash
set -e

echo "=== NVSNAP Node Setup ==="

# Check if running as root
if [ "$EUID" -ne 0 ]; then
    echo "Please run as root"
    exit 1
fi

# Create directories
mkdir -p /usr/local/bin /usr/local/sbin /var/lib/nvsnap/checkpoints /etc/nvsnap

# Install CRIU
if [ -f /tmp/nvsnap-install/criu ]; then
    echo "Installing forked CRIU..."
    [ -f /usr/local/sbin/criu ] && mv /usr/local/sbin/criu /usr/local/sbin/criu.bak
    cp /tmp/nvsnap-install/criu /usr/local/sbin/criu
    chmod +x /usr/local/sbin/criu
    echo "  CRIU: $(/usr/local/sbin/criu --version 2>&1 | head -1)"
fi

# Install cuda-checkpoint if provided
if [ -f /tmp/nvsnap-install/cuda-checkpoint ]; then
    echo "Installing cuda-checkpoint..."
    cp /tmp/nvsnap-install/cuda-checkpoint /usr/local/bin/cuda-checkpoint
    chmod +x /usr/local/bin/cuda-checkpoint
fi

# Install nvsnap-agent
if [ -f /tmp/nvsnap-install/nvsnap-agent ]; then
    echo "Installing nvsnap-agent..."
    
    # Stop existing service if running
    systemctl stop nvsnap-agent 2>/dev/null || true
    
    cp /tmp/nvsnap-install/nvsnap-agent /usr/local/bin/nvsnap-agent
    chmod +x /usr/local/bin/nvsnap-agent
    
    # Create systemd service
    cat > /etc/systemd/system/nvsnap-agent.service << 'EOF'
[Unit]
Description=NVSNAP Agent - GPU Checkpoint/Restore
Documentation=https://github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap
After=network.target containerd.service nvidia-persistenced.service
Wants=containerd.service

[Service]
Type=simple
ExecStart=/usr/local/bin/nvsnap-agent \
    --checkpoint-dir=/var/lib/nvsnap/checkpoints \
    --criu-path=/usr/local/sbin/criu \
    --cuda-checkpoint-path=/usr/local/bin/cuda-checkpoint \
    --listen=:8081 \
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
    
    systemctl daemon-reload
    systemctl enable nvsnap-agent
    systemctl start nvsnap-agent
    
    sleep 2
    if systemctl is-active --quiet nvsnap-agent; then
        echo "  nvsnap-agent: running"
    else
        echo "  nvsnap-agent: FAILED TO START"
        journalctl -u nvsnap-agent --no-pager -n 10
    fi
fi

# Verify installation
echo ""
echo "=== Verification ==="
echo "CRIU:            $(which criu 2>/dev/null || echo 'not found')"
echo "cuda-checkpoint: $(which cuda-checkpoint 2>/dev/null || echo 'not found')"
echo "nvsnap-agent:     $(which nvsnap-agent 2>/dev/null || echo 'not found')"

if command -v nvidia-smi &>/dev/null; then
    echo "NVIDIA Driver:   $(nvidia-smi --query-gpu=driver_version --format=csv,noheader | head -1)"
fi

# Test agent health
if curl -s http://localhost:8081/health &>/dev/null; then
    echo "Agent Health:    OK"
else
    echo "Agent Health:    FAILED"
fi

echo ""
echo "=== Setup Complete ==="
INSTALL_SCRIPT
    chmod +x "$TEMP_DIR/install.sh"
    
    # Copy files to remote
    log_step 2 "Copying files to $NODE_IP..."
    sshpass -p "$SSH_PASS" ssh -o StrictHostKeyChecking=no -o PubkeyAuthentication=no \
        "$SSH_USER@$NODE_IP" "rm -rf /tmp/nvsnap-install && mkdir -p /tmp/nvsnap-install"
    
    sshpass -p "$SSH_PASS" scp -o StrictHostKeyChecking=no -o PubkeyAuthentication=no \
        "$TEMP_DIR"/* "$SSH_USER@$NODE_IP:/tmp/nvsnap-install/"
    
    # Run installation
    log_step 3 "Running installation on $NODE_IP..."
    sshpass -p "$SSH_PASS" ssh -o StrictHostKeyChecking=no -o PubkeyAuthentication=no \
        "$SSH_USER@$NODE_IP" "sudo bash /tmp/nvsnap-install/install.sh"
    
    log_info "Installation complete on $NODE_IP"
}

# Main
main() {
    echo "╔═══════════════════════════════════════════════════════════════╗"
    echo "║              NVSNAP Node Setup                                 ║"
    echo "╚═══════════════════════════════════════════════════════════════╝"
    echo ""
    
    if [[ "$1" == "--remote" && -n "$2" ]]; then
        install_remote "$2"
    elif [[ "$1" == "--all" ]]; then
        # Install on all nodes from config
        load_credentials
        for NODE in ${GPU_NODES:-}; do
            log_info "Installing on $NODE..."
            install_remote "$NODE"
            echo ""
        done
    else
        # Local installation
        if [ "$EUID" -ne 0 ]; then
            log_error "Please run as root (sudo) for local installation"
            exit 1
        fi
        # Run local install
        exec bash -c "$(cat "$SCRIPT_DIR/setup-node.sh" | sed -n '/^INSTALL_SCRIPT$/,/^INSTALL_SCRIPT$/p' | tail -n +2 | head -n -1)"
    fi
}

main "$@"
