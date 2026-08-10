#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

# Install the complete CRIU bundle to all cluster nodes
# This includes: CRIU + libs, CUDA plugin, cuda-checkpoint, restore-entrypoint
#
# Required environment variables:
#   KUBECONFIG - Path to kubeconfig file
#   SSH_USER / SSH_PASS - Credentials (or in ~/.nvsnap/credentials)

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/config.sh"

PROJECT_ROOT="$NVSNAP_PROJECT_ROOT"
BUNDLE_DIR="${PROJECT_ROOT}/bin/criu-bundle"
BUNDLE_TARBALL="${PROJECT_ROOT}/bin/criu-bundle.tar.gz"

# Remote installation path
REMOTE_INSTALL_DIR="/usr/local/nvsnap"

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

# Check if bundle exists
if [ ! -f "$BUNDLE_TARBALL" ]; then
    log_info "Bundle not found, building it first..."
    "$SCRIPT_DIR/build-criu-bundle.sh"
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

log_info "Installing CRIU bundle to cluster nodes"
echo "Bundle: $BUNDLE_TARBALL ($(du -h "$BUNDLE_TARBALL" | cut -f1))"
echo "Target: $REMOTE_INSTALL_DIR"
echo "Nodes: $(echo $NODES | tr '\n' ' ')"
echo ""

for NODE_IP in $NODES; do
    echo "--- Node: $NODE_IP ---"
    
    # Copy bundle tarball
    log_step "Copying bundle..."
    sshpass -p "$SSH_PASS" scp $SSH_OPTS "$BUNDLE_TARBALL" ${SSH_USER}@${NODE_IP}:/tmp/criu-bundle.tar.gz
    
    # Install bundle and create symlinks
    log_step "Installing..."
    sshpass -p "$SSH_PASS" ssh $SSH_OPTS ${SSH_USER}@${NODE_IP} "/bin/bash -s" << 'REMOTE_SCRIPT'
set -e

# Create installation directory
sudo rm -rf /usr/local/nvsnap
sudo mkdir -p /usr/local/nvsnap

# Extract bundle
sudo tar -xzf /tmp/criu-bundle.tar.gz -C /usr/local/nvsnap
rm /tmp/criu-bundle.tar.gz

# Create symlinks for easy access
sudo ln -sf /usr/local/nvsnap/criu-wrapper /usr/local/sbin/criu

# cuda-checkpoint symlink (if it exists in bundle)
if [ -f /usr/local/nvsnap/cuda-checkpoint ]; then
    sudo ln -sf /usr/local/nvsnap/cuda-checkpoint /usr/local/bin/cuda-checkpoint
fi

# restore-entrypoint symlink
if [ -f /usr/local/nvsnap/restore-entrypoint ]; then
    sudo ln -sf /usr/local/nvsnap/restore-entrypoint /usr/local/bin/restore-entrypoint
fi

# Verify installation
echo "  Verifying..."
echo -n "    CRIU: "
/usr/local/sbin/criu --version 2>&1 | head -1

if [ -f /usr/local/bin/cuda-checkpoint ]; then
    echo -n "    cuda-checkpoint: "
    /usr/local/bin/cuda-checkpoint --version 2>&1 | head -1 || echo "installed"
else
    echo "    cuda-checkpoint: NOT FOUND"
fi

if [ -f /usr/local/bin/restore-entrypoint ]; then
    echo "    restore-entrypoint: installed"
fi

# Show bundle contents
echo "    Bundle contents:"
ls /usr/local/nvsnap/ | sed 's/^/      /'
REMOTE_SCRIPT
    
    echo ""
done

log_info "Installation complete!"
echo ""
echo "Tools available on all nodes:"
echo "  - /usr/local/sbin/criu (wrapper using bundled libs)"
echo "  - /usr/local/bin/cuda-checkpoint"
echo "  - /usr/local/bin/restore-entrypoint"
echo "  - /usr/local/nvsnap/ (full bundle directory)"
