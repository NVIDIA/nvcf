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

# Cluster definition for scripts/cluster-smoketest.sh and friends
# (bare-metal nodes reached over SSH). Source this file; do not run it.
#
# Node addresses and SSH details come from the environment, optionally
# loaded from a settings file named by NVSTOR_CLUSTER_ENV (a shell file of
# VAR=value lines, e.g. NODE1=192.0.2.11). A variable already set in the
# environment wins over the file. There are no built-in addresses: NODE1
# and NODE2 must come from one of the two.
#
#   NODE1, NODE2     required: SSH-reachable addresses of the server nodes
#   NODE3            optional third node, used when INCLUDE_NODE3=1
#   SSH_USER         default: $USER
#   SSH_KEY          default: ~/.ssh/id_ed25519
if [ -n "${NVSTOR_CLUSTER_ENV:-}" ]; then
    if [ ! -f "$NVSTOR_CLUSTER_ENV" ]; then
        echo "cluster-config.sh: NVSTOR_CLUSTER_ENV=$NVSTOR_CLUSTER_ENV is not a file" >&2
        exit 1
    fi
    _cluster_cfg_saved=$(declare -p NODE1 NODE2 NODE3 INCLUDE_NODE3 SSH_USER SSH_KEY 2>/dev/null)
    set -a
    # shellcheck disable=SC1090
    . "$NVSTOR_CLUSTER_ENV" || { set +a; echo "cluster-config.sh: failed to load $NVSTOR_CLUSTER_ENV" >&2; exit 1; }
    set +a
    eval "$_cluster_cfg_saved"
    unset _cluster_cfg_saved
fi
if [ -z "${NODE1:-}" ] || [ -z "${NODE2:-}" ]; then
    echo "cluster-config.sh: NODE1 and NODE2 are not set; export them or set NVSTOR_CLUSTER_ENV=<file>" >&2
    exit 1
fi
: "${NODE3:=}"
: "${INCLUDE_NODE3:=0}"
if [ "$INCLUDE_NODE3" = 1 ] && [ -z "$NODE3" ]; then
    echo "cluster-config.sh: INCLUDE_NODE3=1 but NODE3 is not set" >&2
    exit 1
fi

# SSH access.
: "${SSH_USER:=$USER}"
: "${SSH_KEY:=$HOME/.ssh/id_ed25519}"

# nvstor-server ports (must match the [network] block in the per-node
# TOML the harness generates).
: "${NVSTOR_HEALTH_PORT:=9101}"
: "${NVSTOR_S3_PORT:=9000}"
: "${NVSTOR_NBD_PORT:=10809}"
: "${NVSTOR_NFS_PORT:=12049}"
: "${NVSTOR_RAFT_PORT:=9102}"
: "${NVSTOR_RDMA_RPC_PORT:=9878}"    # rdma_port + 1

# s3-rdma / s3-tcp / node-to-node RPC authenticate every request. These
# are the credentials the harness writes into each node's [auth] block and
# hands to the bench clients. Override in the environment for a real
# deployment; the defaults exist so the smoketest is runnable out of the box.
: "${NVSTOR_ACCESS_KEY:=nvstor-smoketest}"
: "${NVSTOR_SECRET_KEY:=nvstor-smoketest-secret}"

export NODE1 NODE2 NODE3 INCLUDE_NODE3
export SSH_USER SSH_KEY
export NVSTOR_HEALTH_PORT NVSTOR_S3_PORT NVSTOR_NBD_PORT
export NVSTOR_NFS_PORT NVSTOR_RAFT_PORT NVSTOR_RDMA_RPC_PORT
export NVSTOR_ACCESS_KEY NVSTOR_SECRET_KEY
