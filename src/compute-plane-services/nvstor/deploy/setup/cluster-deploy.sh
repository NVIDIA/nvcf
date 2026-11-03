#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Deploy nvstor to a 3-node cluster.
# Usage: bash cluster-deploy.sh <node1-ip> <node2-ip> <node3-ip>
#
# Prerequisites: node-setup.sh already run on each node, SSH key access.

set -euo pipefail

if [ $# -lt 3 ]; then
    echo "Usage: $0 <node1-ip> <node2-ip> <node3-ip>"
    exit 1
fi

NODE1=$1
NODE2=$2
NODE3=$3
NODES=("$NODE1" "$NODE2" "$NODE3")
SSH_USER=${SSH_USER:-$(whoami)}
NVSTOR_DIR=${NVSTOR_DIR:-/opt/nvstor}

echo "========================================"
echo "  nvstor Cluster Deployment"
echo "========================================"
echo "  Node 1: $NODE1"
echo "  Node 2: $NODE2"
echo "  Node 3: $NODE3"
echo "  User:   $SSH_USER"
echo ""

# ---------------------------------------------------------------------------
# 1. Build release binary locally
# ---------------------------------------------------------------------------
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

echo "[1/4] Building release binary..."
cd "$REPO_ROOT"
cargo build --release --bin nvstor-server 2>&1 | tail -3

BINARY="$REPO_ROOT/target/release/nvstor-server"
if [ ! -f "$BINARY" ]; then
    echo "ERROR: binary not found at $BINARY"
    exit 1
fi
echo "  Binary: $(ls -lh "$BINARY" | awk '{print $5}')"

# ---------------------------------------------------------------------------
# 2. Generate per-node configs
# ---------------------------------------------------------------------------
echo ""
echo "[2/4] Generating configs..."

generate_config() {
    local node_id=$1
    local node_ip=$2
    local all_peers=""
    local NFS_CLIENTS
    NFS_CLIENTS=$(printf '"%s", ' "${NODES[@]}"); NFS_CLIENTS=${NFS_CLIENTS%, }

    for i in "${!NODES[@]}"; do
        local peer_id=$((i + 1))
        if [ "$peer_id" != "$node_id" ]; then
            all_peers="${all_peers}
[[cluster.seeds]]
addr = \"${NODES[$i]}:9102\""
        fi
    done

    cat << EOF
[server]
node_id = ${node_id}
listen_addr = "0.0.0.0"
data_dir = "${NVSTOR_DIR}/data"

[server.ports]
s3 = 9000
wire = 9002
health = 9101
nbd = 10809
nfs = 2049
raft = 9102

[storage]
chunk_size = 4194304
direct_io = true

[cluster]
node_name = "nvstor-${node_id}"
bind_addr = "${node_ip}:9102"
${all_peers}

[replication]
default_replicas = 3

[nfs]
# The cluster nodes; add the hosts that will mount the export.
allow_clients = ["127.0.0.1", ${NFS_CLIENTS}]

[s3]
enabled = true
region = "us-east-1"
EOF
}

for i in 0 1 2; do
    node_id=$((i + 1))
    config_file="/tmp/nvstor-node${node_id}.toml"
    generate_config "$node_id" "${NODES[$i]}" > "$config_file"
    echo "  Generated: $config_file"
done

# ---------------------------------------------------------------------------
# 3. Deploy to each node
# ---------------------------------------------------------------------------
echo ""
echo "[3/4] Deploying to nodes..."

for i in 0 1 2; do
    node_id=$((i + 1))
    node_ip="${NODES[$i]}"
    config_file="/tmp/nvstor-node${node_id}.toml"

    echo "  Deploying to node ${node_id} (${node_ip})..."

    # Create directory
    ssh "$SSH_USER@$node_ip" "sudo mkdir -p ${NVSTOR_DIR}/{bin,data,config} && sudo chown -R $SSH_USER:$SSH_USER $NVSTOR_DIR"

    # Copy binary and config
    scp -q "$BINARY" "$SSH_USER@$node_ip:${NVSTOR_DIR}/bin/nvstor-server"
    scp -q "$config_file" "$SSH_USER@$node_ip:${NVSTOR_DIR}/config/nvstor.toml"

    # Create systemd service
    ssh "$SSH_USER@$node_ip" "sudo tee /etc/systemd/system/nvstor.service > /dev/null" << EOF
[Unit]
Description=nvstor Distributed Storage
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=$SSH_USER
ExecStart=${NVSTOR_DIR}/bin/nvstor-server --config ${NVSTOR_DIR}/config/nvstor.toml
Restart=always
RestartSec=5
LimitNOFILE=1048576
LimitMEMLOCK=infinity
Environment=RUST_LOG=info

[Install]
WantedBy=multi-user.target
EOF

    ssh "$SSH_USER@$node_ip" "sudo systemctl daemon-reload"
    echo "    Done."
done

# ---------------------------------------------------------------------------
# 4. Start cluster
# ---------------------------------------------------------------------------
echo ""
echo "[4/4] Starting cluster..."

for i in 0 1 2; do
    node_id=$((i + 1))
    node_ip="${NODES[$i]}"
    echo "  Starting node ${node_id} (${node_ip})..."
    ssh "$SSH_USER@$node_ip" "sudo systemctl restart nvstor"
done

# Wait for startup
sleep 3

echo ""
echo "Checking health..."
for i in 0 1 2; do
    node_id=$((i + 1))
    node_ip="${NODES[$i]}"
    status=$(curl -s -o /dev/null -w "%{http_code}" "http://${node_ip}:9101/health" 2>/dev/null || echo "unreachable")
    echo "  Node ${node_id} (${node_ip}): ${status}"
done

echo ""
echo "========================================"
echo "  Deployment complete!"
echo "========================================"
echo ""
echo "Test commands:"
echo "  # S3"
echo "  aws --endpoint-url http://${NODE1}:9000 s3 mb s3://test-bucket"
echo "  echo hello | aws --endpoint-url http://${NODE1}:9000 s3 cp - s3://test-bucket/hello.txt"
echo ""
echo "  # NFS"
echo "  # (from a node; other hosts must be added to [nfs] allow_clients)"
echo "  sudo mount -t nfs4 ${NODE1}:/ /mnt/nvstor"
echo ""
echo "  # Health"
echo "  curl http://${NODE1}:9101/health"
