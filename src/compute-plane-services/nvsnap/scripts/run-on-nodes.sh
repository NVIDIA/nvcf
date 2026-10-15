#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# NVSNAP Run Command on All Nodes
#
# Executes a command on all or specified cluster nodes.
#
# Usage:
#   ./run-on-nodes.sh <command>
#   ./run-on-nodes.sh --nodes "10.34.5.64,10.86.2.83" <command>
#   ./run-on-nodes.sh --script <script-file>

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Default nodes for nvsnap-test-cluster
DEFAULT_NODES="10.34.5.64 10.86.2.83 10.86.6.104"

# Credentials from environment or config file
CONFIG_FILE="${HOME}/.nvsnap/credentials"
SSH_OPTS="-o StrictHostKeyChecking=no -o PreferredAuthentications=password -o PubkeyAuthentication=no -o ConnectTimeout=10"

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

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

log() { echo -e "${GREEN}[INFO]${NC} $1"; }
warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }

ssh_cmd() {
    local host=$1
    shift
    sshpass -p "$SSH_PASS" ssh $SSH_OPTS "$SSH_USER@$host" "$@" 2>&1 | grep -v "limit:\|addtopath\|Command not found" || true
}

scp_cmd() {
    local src=$1
    local host=$2
    local dst=$3
    sshpass -p "$SSH_PASS" scp $SSH_OPTS "$src" "$SSH_USER@$host:$dst" 2>/dev/null
}

main() {
    local nodes="$DEFAULT_NODES"
    local script_file=""
    local sudo_cmd=""
    
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --nodes)
                nodes="${2//,/ }"
                shift 2
                ;;
            --script)
                script_file="$2"
                shift 2
                ;;
            --sudo)
                sudo_cmd="echo $SSH_PASS | sudo -S"
                shift
                ;;
            *)
                break
                ;;
        esac
    done
    
    local cmd="${*:-}"
    
    load_credentials
    
    if [[ -z "$cmd" && -z "$script_file" ]]; then
        echo "Usage: $0 [--nodes \"ip1,ip2\"] [--sudo] [--script <file>] <command>"
        echo ""
        echo "Default nodes: $DEFAULT_NODES"
        exit 1
    fi
    
    for node in $nodes; do
        echo -e "\n${BLUE}=== $node ===${NC}"
        
        if [[ -n "$script_file" ]]; then
            # Copy and run script
            local remote_script="/tmp/$(basename "$script_file")"
            scp_cmd "$script_file" "$node" "$remote_script"
            if [[ -n "$sudo_cmd" ]]; then
                ssh_cmd "$node" "echo $SSH_PASS | sudo -S bash $remote_script"
            else
                ssh_cmd "$node" "bash $remote_script"
            fi
        else
            # Run command
            if [[ -n "$sudo_cmd" ]]; then
                ssh_cmd "$node" "echo $SSH_PASS | sudo -S bash -c '$cmd'"
            else
                ssh_cmd "$node" "bash -c '$cmd'"
            fi
        fi
    done
    
    echo ""
}

main "$@"
