#!/bin/sh
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Combined init script for NvSnap restore pods.
# Copies all dependencies to shared volumes in one step.
set -e

echo "=== NvSnap init: copying dependencies ==="

# Copy Python packages (uvloop, pyzmq) to nvsnap-lib volume
mkdir -p /nvsnap-lib/site-packages
cp -r /staging/site-packages/* /nvsnap-lib/site-packages/

# Copy shared libraries (libuv, libzmq, libnvsnap_intercept) to nvsnap-lib volume
cp /staging/lib/libuv.so* /nvsnap-lib/ 2>/dev/null || true
cp /staging/lib/libzmq.so* /nvsnap-lib/
cp /staging/criu-bundle/lib/libnvsnap_intercept.so /nvsnap-lib/

# Copy CRIU bundle (criu, restore-entrypoint, agent, cuda-checkpoint) to nvsnap-tools volume
cp -r /staging/criu-bundle/. /nvsnap/
# Copy optional debug tools
if [ -f /staging/criu-bundle/py-spy ]; then
    cp /staging/criu-bundle/py-spy /nvsnap/
fi

echo "=== NvSnap init complete ==="
