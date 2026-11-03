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

#
# deploy-cluster-smoketest.sh — runs FROM a dev workstation.
#
# Builds the release binary locally, scp's it + the cluster-smoketest
# script to a remote node, then executes the smoke test via ssh.
#
# Requires:
#   - This repo checked out locally with cargo available.
#   - SSH access to a node with: nfs-common, sudo, ss, sha256sum.
#
# Usage:
#   ./deploy-cluster-smoketest.sh <user@host>
#   ./deploy-cluster-smoketest.sh user@10.0.0.1
#   SSH_KEY=~/.ssh/id_ed25519_foo ./deploy-cluster-smoketest.sh user@host
#
# Exit 0 on a successful full smoke test.

set -euo pipefail

TARGET="${1:-}"
SSH_KEY="${SSH_KEY:-}"
[ -n "$TARGET" ] || { echo "usage: $0 <user@host>" >&2; exit 1; }

SSH_OPTS=(-o StrictHostKeyChecking=accept-new -o ConnectTimeout=10)
[ -n "$SSH_KEY" ] && SSH_OPTS+=(-i "$SSH_KEY")

REPO_ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
BIN="$REPO_ROOT/target/release/nvstor-nfs-export"
CLUSTER_SMOKETEST="$REPO_ROOT/crates/nvstor-nfs/scripts/cluster-smoketest.sh"

echo "==> Building nvstor-nfs-export (release)"
(cd "$REPO_ROOT" && cargo build --release -p nvstor-nfs --bin nvstor-nfs-export >/dev/null 2>&1)
[ -x "$BIN" ] || { echo "error: missing $BIN" >&2; exit 2; }

echo "==> Copying binary + script to $TARGET"
scp "${SSH_OPTS[@]}" "$BIN" "${TARGET}:/tmp/nvstor-nfs-export"
scp "${SSH_OPTS[@]}" "$CLUSTER_SMOKETEST" "${TARGET}:/tmp/cluster-smoketest.sh"

echo "==> Running cluster-smoketest.sh on $TARGET"
# Default shell on these nodes is csh, so wrap in bash -c explicitly.
ssh "${SSH_OPTS[@]}" "$TARGET" \
    "bash -c 'chmod +x /tmp/nvstor-nfs-export /tmp/cluster-smoketest.sh && BIN=/tmp/nvstor-nfs-export bash /tmp/cluster-smoketest.sh'"
