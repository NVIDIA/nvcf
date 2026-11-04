#!/usr/bin/env bash
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

# Deploy a 3-node nvstor cluster to bare metal.
#
# Configure via environment variables rather than edits to this file:
#
#   NVSTOR_NODES    comma-separated list of "user@host" entries (required)
#   NVSTOR_CONFIGS  comma-separated list of config files, one per node
#                   (default: node1.toml,node2.toml,node3.toml in the same
#                   directory as this script)
#   SSH_KEY         path to private key for ssh/scp (optional)
#   REMOTE_DIR      remote data directory (default /var/lib/nvstor)
#   REMOTE_BIN      path on remote nodes (default /usr/local/bin/nvstor-server)
#
# Example:
#   NVSTOR_NODES="user@10.0.0.1,user@10.0.0.2,user@10.0.0.3" \
#       bash deploy.sh
#
# Assumes passwordless sudo for the deploy user on every target node.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

IFS=',' read -r -a NODES <<< "${NVSTOR_NODES:-}"
if [ "${#NODES[@]}" -lt 3 ]; then
    echo "ERROR: set NVSTOR_NODES to a comma-separated list of at least 3 user@host entries" >&2
    exit 1
fi

IFS=',' read -r -a CONFIGS <<< "${NVSTOR_CONFIGS:-${SCRIPT_DIR}/node1.toml,${SCRIPT_DIR}/node2.toml,${SCRIPT_DIR}/node3.toml}"
if [ "${#CONFIGS[@]}" -ne "${#NODES[@]}" ]; then
    echo "ERROR: NVSTOR_CONFIGS must supply one config per node (${#NODES[@]} nodes given)" >&2
    exit 1
fi

BINARY="${REPO_ROOT}/target/release/nvstor-server"
REMOTE_DIR="${REMOTE_DIR:-/var/lib/nvstor}"
REMOTE_BIN="${REMOTE_BIN:-/usr/local/bin/nvstor-server}"

SSH_OPTS=(-o StrictHostKeyChecking=accept-new -o ConnectTimeout=15)
[ -n "${SSH_KEY:-}" ] && SSH_OPTS+=(-i "$SSH_KEY")

if [ ! -f "$BINARY" ]; then
    echo "ERROR: release binary not found at $BINARY" >&2
    echo "       Run: cargo build --release --bin nvstor-server" >&2
    exit 1
fi

echo "Binary size: $(du -h "$BINARY" | cut -f1)"
echo

for i in "${!NODES[@]}"; do
    NODE="${NODES[$i]}"
    CONFIG="${CONFIGS[$i]}"

    [ -f "$CONFIG" ] || { echo "ERROR: missing config $CONFIG" >&2; exit 1; }

    echo "=== Deploying to node $((i+1)) ($NODE) ==="

    echo "  Stopping existing nvstor-server..."
    ssh "${SSH_OPTS[@]}" "$NODE" "sudo pkill -f nvstor-server || true" || true

    echo "  Ensuring data directory..."
    ssh "${SSH_OPTS[@]}" "$NODE" \
        "sudo mkdir -p $REMOTE_DIR && sudo chown \$USER:\$USER $REMOTE_DIR"

    echo "  Copying binary..."
    scp "${SSH_OPTS[@]}" "$BINARY" "$NODE:/tmp/nvstor-server"
    ssh "${SSH_OPTS[@]}" "$NODE" \
        "sudo mv /tmp/nvstor-server $REMOTE_BIN && sudo chmod +x $REMOTE_BIN"

    echo "  Copying config..."
    scp "${SSH_OPTS[@]}" "$CONFIG" "$NODE:$REMOTE_DIR/nvstor.toml"

    echo "  Starting nvstor-server..."
    ssh "${SSH_OPTS[@]}" "$NODE" \
        "nohup $REMOTE_BIN --config $REMOTE_DIR/nvstor.toml > $REMOTE_DIR/nvstor.log 2>&1 &"

    echo "  Done."
    echo
done

echo "Waiting 5 s for cluster to form..."
sleep 5

echo
echo "=== Health checks ==="
for NODE in "${NODES[@]}"; do
    HOST="${NODE#*@}"
    echo -n "  $HOST: "
    curl -sf "http://$HOST:9101/healthz" || echo "FAILED"
done

echo
echo "=== S3 smoke test (via first node) ==="
FIRST_HOST="${NODES[0]#*@}"
echo -n "  Create bucket: "
curl -sf -o /dev/null -w "%{http_code}" -X PUT "http://$FIRST_HOST:9000/deploy-test" || true
echo
echo -n "  Put object: "
curl -sf -o /dev/null -w "%{http_code}" -X PUT -d "Hello from 3-node cluster" \
    "http://$FIRST_HOST:9000/deploy-test/hello.txt" || true
echo
echo -n "  Get object: "
curl -sf "http://$FIRST_HOST:9000/deploy-test/hello.txt" || true
echo
echo
echo "=== Deployment complete ==="
echo "Nodes:  ${NODES[*]}"
echo "S3:     http://$FIRST_HOST:9000"
echo "NFS:    $FIRST_HOST:2049"
echo "Health: http://<node>:9101/healthz"
