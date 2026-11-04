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

# nvstor node setup — run on each cluster node.
# Usage: bash node-setup.sh
#
# Tested on: Ubuntu 24.04 LTS
# Requirements: sudo access, internet connectivity

set -euo pipefail

echo "========================================"
echo "  nvstor Node Setup — $(hostname)"
echo "========================================"

# ---------------------------------------------------------------------------
# 1. System packages
# ---------------------------------------------------------------------------
echo ""
echo "[1/6] Installing system packages..."
sudo apt-get update -qq
sudo apt-get install -y -qq \
    build-essential \
    pkg-config \
    git \
    curl \
    wget \
    htop \
    iotop \
    sysstat \
    numactl \
    hwloc \
    fio \
    nfs-common \
    nbd-client \
    awscli \
    jq

# ---------------------------------------------------------------------------
# 2. RDMA / ibverbs (if hardware present)
# ---------------------------------------------------------------------------
echo ""
echo "[2/6] Installing RDMA stack..."
sudo apt-get install -y -qq \
    rdma-core \
    libibverbs-dev \
    ibverbs-utils \
    ibverbs-providers \
    librdmacm-dev \
    rdmacm-utils \
    infiniband-diags \
    perftest

# Check RDMA hardware
if ls /sys/class/infiniband/ &>/dev/null; then
    echo "  RDMA devices found:"
    ls /sys/class/infiniband/
    ibv_devinfo 2>/dev/null | grep -E 'hca_id|state|link_layer' || true
else
    echo "  No RDMA hardware detected (will use TCP fallback)"
fi

# ---------------------------------------------------------------------------
# 3. RocksDB dependencies
# ---------------------------------------------------------------------------
echo ""
echo "[3/6] Installing RocksDB dependencies..."
sudo apt-get install -y -qq \
    libclang-dev \
    clang \
    librocksdb-dev \
    libsnappy-dev \
    liblz4-dev \
    libzstd-dev

# ---------------------------------------------------------------------------
# 4. Rust toolchain
# ---------------------------------------------------------------------------
echo ""
echo "[4/6] Installing Rust toolchain..."
if command -v rustc &>/dev/null; then
    echo "  Rust already installed: $(rustc --version)"
    rustup update stable 2>/dev/null || true
else
    curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh -s -- -y --default-toolchain stable
    source "$HOME/.cargo/env"
    echo "  Installed: $(rustc --version)"
fi

# ---------------------------------------------------------------------------
# 5. Kernel tuning for storage workloads
# ---------------------------------------------------------------------------
echo ""
echo "[5/6] Applying kernel tuning..."
sudo tee /etc/sysctl.d/99-nvstor.conf > /dev/null << 'SYSCTL'
# nvstor storage node tuning

# Network — high throughput
net.core.rmem_max = 16777216
net.core.wmem_max = 16777216
net.core.rmem_default = 1048576
net.core.wmem_default = 1048576
net.core.netdev_max_backlog = 30000
net.core.somaxconn = 65535
net.ipv4.tcp_rmem = 4096 1048576 16777216
net.ipv4.tcp_wmem = 4096 1048576 16777216
net.ipv4.tcp_max_syn_backlog = 65535
net.ipv4.tcp_tw_reuse = 1
net.ipv4.tcp_fin_timeout = 10

# VM — minimize swapping, favor page cache
vm.swappiness = 1
vm.dirty_ratio = 40
vm.dirty_background_ratio = 10
vm.vfs_cache_pressure = 50

# Huge pages (optional, for RDMA memory registration)
vm.nr_hugepages = 1024

# File descriptors
fs.file-max = 2097152
fs.nr_open = 2097152
SYSCTL

sudo sysctl --system -q

# Raise limits
sudo tee /etc/security/limits.d/99-nvstor.conf > /dev/null << 'LIMITS'
* soft nofile 1048576
* hard nofile 1048576
* soft memlock unlimited
* hard memlock unlimited
* soft nproc 65535
* hard nproc 65535
LIMITS

# ---------------------------------------------------------------------------
# 6. NVMe optimization (if present)
# ---------------------------------------------------------------------------
echo ""
echo "[6/6] Checking storage..."
for dev in /sys/block/nvme*; do
    if [ -d "$dev" ]; then
        devname=$(basename "$dev")
        echo "  NVMe device: $devname"
        # Set scheduler to none (NVMe doesn't need I/O scheduling)
        echo none | sudo tee /sys/block/$devname/queue/scheduler > /dev/null 2>&1 || true
        # Read-ahead for sequential workloads
        echo 256 | sudo tee /sys/block/$devname/queue/read_ahead_kb > /dev/null 2>&1 || true
    fi
done

lsblk -o NAME,SIZE,TYPE,FSTYPE,MOUNTPOINT | grep -E 'disk|nvme' || echo "  No block devices"

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------
echo ""
echo "========================================"
echo "  Setup complete!"
echo "========================================"
echo "  Hostname:   $(hostname)"
echo "  OS:         $(lsb_release -ds 2>/dev/null || cat /etc/os-release | grep PRETTY_NAME | cut -d= -f2)"
echo "  Kernel:     $(uname -r)"
echo "  CPU:        $(nproc) cores"
echo "  RAM:        $(free -h | awk '/Mem:/ {print $2}')"
echo "  Rust:       $(source "$HOME/.cargo/env" 2>/dev/null; rustc --version 2>/dev/null || echo 'not found')"
echo "  RDMA:       $(ls /sys/class/infiniband/ 2>/dev/null | tr '\n' ' ' || echo 'none')"
echo ""
echo "Next: clone nvstor repo and run 'cargo build --release'"
