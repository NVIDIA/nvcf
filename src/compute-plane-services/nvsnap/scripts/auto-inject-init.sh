#!/bin/sh
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Auto-inject init payload — replaces the 4-init-container fan-out
# (get-uvloop, get-libuv, get-libzmq, get-nvsnap). This script runs as
# a single init container using the nvsnap-agent image; it copies all
# four payloads into the workload pod's /nvsnap-lib emptyDir volume.
#
# The agent image must have everything it needs already bundled:
#   /criu-bundle/payload/wheels/uvloop-*.whl     (multi-python wheels)
#   /criu-bundle/payload/lib/libuv.so*
#   /criu-bundle/payload/lib/libzmq.so*
#   /criu-bundle/lib/libnvsnap_intercept.so
#   /criu-bundle/sitecustomize/                  (sitecustomize.py)
#
# Caller stamps PYTHONPATH=/nvsnap-lib/sitecustomize, LD_LIBRARY_PATH
# starts with /nvsnap-lib, and LD_PRELOAD points at the intercept lib —
# all from the webhook auto-inject patch in internal/webhook/auto_inject.go.

set -eu

DST=/nvsnap-lib

# 1. uvloop wheels per Python ABI tag. sitecustomize picks the right
# one at runtime based on sys.version_info.
for whl in /criu-bundle/payload/wheels/uvloop-*.whl; do
    if [ ! -e "$whl" ]; then
        echo "auto-inject: no uvloop wheels found at /criu-bundle/payload/wheels/" >&2
        exit 1
    fi
    tag=$(echo "$whl" | grep -oE 'cp3[0-9]+' | head -1)
    if [ -z "$tag" ]; then
        echo "auto-inject: cannot extract ABI tag from $whl" >&2
        exit 1
    fi
    mkdir -p "$DST/site-packages-${tag}"
    python3 -m zipfile -e "$whl" "$DST/site-packages-${tag}/"
done

# 2. Patched native libs (libuv, libzmq). LD_LIBRARY_PATH picks them
# up before the system versions.
cp /criu-bundle/payload/lib/libuv.so*  "$DST/"
cp /criu-bundle/payload/lib/libzmq.so* "$DST/"

# 3. Intercept lib + sitecustomize. LD_PRELOAD'd into every workload
# process at startup; PYTHONPATH'd so the interpreter imports our
# sitecustomize.py before user code runs.
cp /criu-bundle/lib/libnvsnap_intercept.so "$DST/"
cp -r /criu-bundle/sitecustomize "$DST/"

echo "auto-inject: payload installed under $DST"
ls -la "$DST"
