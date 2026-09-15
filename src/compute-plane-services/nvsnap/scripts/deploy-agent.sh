#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

# Deploy nvsnap-agent to all cluster nodes
# This copies the binary and sets up a systemd service
#
# Required environment variables:
#   KUBECONFIG - Path to kubeconfig file
#   SSH_USER / SSH_PASS - Credentials (or in ~/.nvsnap/credentials)

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/config.sh"

PROJECT_ROOT="$NVSNAP_PROJECT_ROOT"
AGENT_BINARY="${PROJECT_ROOT}/bin/nvsnap-agent"

SSH_OPTS="-o PreferredAuthentications=password -o PubkeyAuthentication=no -o StrictHostKeyChecking=no"

log_info() { echo "=== $1 ==="; }
log_step() { echo "  $1"; }

# Validate requirements
if ! validate_kubeconfig; then
    exit 1
fi

if ! load_credentials; then
    exit 1
fi

# Build agent if not exists
if [ ! -f "$AGENT_BINARY" ]; then
    log_info "Building agent..."
    cd "$PROJECT_ROOT" && make agent
fi

# Get cluster nodes
if ! kubectl cluster-info &>/dev/null; then
    echo "ERROR: Cannot connect to cluster. Check KUBECONFIG."
    exit 1
fi

NODES=$(kubectl get nodes -o jsonpath='{range .items[*]}{.status.addresses[?(@.type=="InternalIP")].address}{"\n"}{end}')

if [ -z "$NODES" ]; then
    echo "ERROR: No nodes found in cluster"
    exit 1
fi

log_info "Deploying nvsnap-agent to cluster nodes"
echo "Binary: $AGENT_BINARY"
echo "Nodes: $(echo $NODES | tr '\n' ' ')"
echo ""

for NODE_IP in $NODES; do
    echo "--- Node: $NODE_IP ---"
    
    # Copy agent binary
    log_step "Copying agent binary..."
    sshpass -p "$SSH_PASS" scp $SSH_OPTS "$AGENT_BINARY" ${SSH_USER}@${NODE_IP}:/tmp/nvsnap-agent
    
    # Install and setup systemd service
    log_step "Installing agent..."
    sshpass -p "$SSH_PASS" ssh $SSH_OPTS ${SSH_USER}@${NODE_IP} "/bin/bash -s" << REMOTE_SCRIPT
set -e

# Stop existing agent if running
sudo systemctl stop nvsnap-agent 2>/dev/null || true
sleep 1  # Wait for process to fully stop

# Install binary
sudo rm -f /usr/local/bin/nvsnap-agent 2>/dev/null || true
sudo cp /tmp/nvsnap-agent /usr/local/bin/nvsnap-agent
sudo chmod +x /usr/local/bin/nvsnap-agent
rm /tmp/nvsnap-agent

# Create checkpoint directory
sudo mkdir -p /var/lib/nvsnap/checkpoints

# Install systemd service
cat << 'SVCEOF' | sudo tee /etc/systemd/system/nvsnap-agent.service > /dev/null
[Unit]
Description=NVSNAP Agent - GPU Checkpoint/Restore
After=network.target containerd.service
Wants=containerd.service

[Service]
Type=simple
ExecStart=/usr/local/bin/nvsnap-agent --listen :8081 --checkpoint-dir /var/lib/nvsnap/checkpoints --containerd-socket /run/containerd/containerd.sock --criu-path /usr/local/sbin/criu --cuda-checkpoint-path /usr/local/bin/cuda-checkpoint --log-level info
Restart=always
RestartSec=5
Environment=PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin

[Install]
WantedBy=multi-user.target
SVCEOF

# Reload and restart
sudo systemctl daemon-reload
sudo systemctl enable nvsnap-agent
sudo systemctl restart nvsnap-agent

# Wait for startup
sleep 2

# Check status
echo "  Status:"
sudo systemctl status nvsnap-agent --no-pager | head -5 || true

# Test health endpoint
echo "  Health check:"
curl -s http://localhost:8081/health | head -1 || echo "    Agent not responding yet"
REMOTE_SCRIPT
    
    echo ""
done

log_info "Agent deployment complete!"
echo ""
echo "Agent is running on all nodes at port 8081"
echo "Test with: curl http://<node-ip>:8081/health"
echo "View logs: ssh <node> journalctl -u nvsnap-agent -f"
