#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

# Master deployment script - builds and deploys everything
#
# Required environment variables:
#   KUBECONFIG - Path to kubeconfig file
#   CRIU_FORK_SRC - Path to forked CRIU source directory
#   SSH_USER / SSH_PASS - Credentials (or in ~/.nvsnap/credentials)
#
# Optional environment variables:
#   CUDA_CHECKPOINT_PATH - Path to cuda-checkpoint binary
#
# Usage:
#   export KUBECONFIG=/path/to/kubeconfig
#   export CRIU_FORK_SRC=/path/to/criu-fork
#   ./scripts/deploy-all.sh

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/config.sh"

PROJECT_ROOT="$NVSNAP_PROJECT_ROOT"

log_info() { echo ""; echo "========================================"; echo "  $1"; echo "========================================"; }

cd "$PROJECT_ROOT"

# Validate all requirements upfront
echo "Validating configuration..."
if ! validate_criu_source; then
    exit 1
fi
if ! validate_kubeconfig; then
    exit 1
fi
if ! load_credentials; then
    exit 1
fi
echo "Configuration OK"

# Step 1: Build CRIU bundle
log_info "Step 1: Building CRIU Bundle"
"$SCRIPT_DIR/build-criu-bundle.sh"

# Step 2: Build agent
log_info "Step 2: Building Agent"
make agent

# Step 3: Deploy CRIU bundle to nodes
log_info "Step 3: Deploying CRIU Bundle to Cluster Nodes"
"$SCRIPT_DIR/install-criu.sh"

# Step 4: Deploy agent to nodes
log_info "Step 4: Deploying Agent to Cluster Nodes"
"$SCRIPT_DIR/deploy-agent.sh"

log_info "Deployment Complete!"
echo ""
echo "Tools deployed to all nodes:"
echo "  - CRIU (forked, with GPU support)"
echo "  - cuda-checkpoint"
echo "  - restore-entrypoint"
echo "  - nvsnap-agent (running as systemd service)"
echo ""
echo "Test with:"
echo "  kubectl apply -f deploy/tests/01-pytorch-test.yaml"
echo "  curl http://<node-ip>:8081/health"
