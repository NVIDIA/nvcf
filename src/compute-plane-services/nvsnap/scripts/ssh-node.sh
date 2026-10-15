#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# NVSNAP SSH Node Helper
#
# SSH into cluster nodes with saved credentials.
# Useful for debugging and manual testing.
#
# Usage:
#   ./ssh-node.sh <node-ip|node-name> [command]
#
# Environment:
#   SSH_USER - SSH username (default: from config)
#   SSH_PASS - SSH password (default: from config)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CONFIG_FILE="$HOME/.nvsnap/node-credentials"

# Credentials from environment or config file
# Set SSH_USER and SSH_PASS environment variables before running
CONFIG_FILE="${HOME}/.nvsnap/credentials"

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

# Node name to IP mapping for nvsnap-test-cluster
declare -A NODE_IPS=(
    ["4u2g-0226"]="10.34.5.64"
    ["4u2g-0061"]="10.86.2.83"
    ["2u1g-x570-2961"]="10.86.6.104"
)

get_node_ip() {
    local node="$1"
    
    # Check if it's already an IP
    if [[ "$node" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
        echo "$node"
        return
    fi
    
    # Check node map
    if [[ -n "${NODE_IPS[$node]:-}" ]]; then
        echo "${NODE_IPS[$node]}"
        return
    fi
    
    # Try kubectl
    if command -v kubectl &>/dev/null; then
        kubectl get node "$node" -o jsonpath='{.status.addresses[?(@.type=="InternalIP")].address}' 2>/dev/null || echo "$node"
    else
        echo "$node"
    fi
}

main() {
    if [[ $# -lt 1 ]]; then
        echo "Usage: $0 <node-ip|node-name> [command]"
        echo ""
        echo "Known nodes:"
        for name in "${!NODE_IPS[@]}"; do
            echo "  $name -> ${NODE_IPS[$name]}"
        done
        exit 1
    fi
    
    local node="$1"
    shift
    local cmd="${*:-}"
    
    load_credentials
    
    local node_ip=$(get_node_ip "$node")
    
    if [[ -n "$cmd" ]]; then
        # Run command
        sshpass -p "$SSH_PASS" ssh \
            -o StrictHostKeyChecking=no \
            -o PreferredAuthentications=password \
            -o PubkeyAuthentication=no \
            -o ConnectTimeout=10 \
            "$SSH_USER@$node_ip" "bash -c '$cmd'"
    else
        # Interactive shell
        sshpass -p "$SSH_PASS" ssh \
            -o StrictHostKeyChecking=no \
            -o PreferredAuthentications=password \
            -o PubkeyAuthentication=no \
            -o ConnectTimeout=10 \
            "$SSH_USER@$node_ip"
    fi
}

main "$@"
