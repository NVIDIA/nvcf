#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# cuda-checkpoint wrapper — sets up NVIDIA library paths and isolates from LD_PRELOAD.
#
# The CRIU CUDA plugin parses cuda-checkpoint's stdout/stderr to extract the
# restore thread ID. If LD_PRELOAD libraries (like libnvsnap_intercept.so) load
# in cuda-checkpoint, their log messages corrupt the output, causing the plugin
# to get tid=0 and GPU resume to fail.
#
# Fix: unset LD_PRELOAD and temporarily disable /etc/ld.so.preload before exec.
#
# LD_LIBRARY_PATH composition order (first match wins):
#   1. $NVSNAP_CUDA_LIB_DIR — runtime-discovered from the workload's
#      /proc/<pid>/maps by the nvsnap-agent. This is the universal-across-K8s
#      path: it picks whatever directory the running CUDA process actually
#      loaded libcuda from. Empty when the workload hasn't dlopen'd libcuda
#      yet (rare; readiness gates checkpoint behind serving).
#   2. Fallback list of well-known cluster layouts — used when (1) is empty.
#      Keep this list updated as new cluster flavors are encountered, but
#      the runtime-discovery layer (1) makes it less load-bearing.
#   3. Whatever LD_LIBRARY_PATH the parent already had.
#
# See docs/architecture/06-CUDA-CHECKPOINT-CALL-FLOW.md for the full flow.

unset LD_PRELOAD
if [ -f /etc/ld.so.preload ]; then
    mv /etc/ld.so.preload /etc/ld.so.preload.bak 2>/dev/null
    trap 'mv /etc/ld.so.preload.bak /etc/ld.so.preload 2>/dev/null' EXIT
fi

# Container view (this wrapper runs in the nvsnap-agent container's mntns).
# Each entry is a directory where libcuda.so.1 *might* live on the host,
# expressed as the agent's /host-prefixed view.
FALLBACK_PATHS="\
/host/run/nvidia/driver/usr/lib/x86_64-linux-gnu:\
/host/home/kubernetes/bin/nvidia/lib64:\
/host/usr/local/nvidia/lib64:\
/host/usr/lib/x86_64-linux-gnu"

# Host view (used when this wrapper runs inside nsenter into host mntns).
HOST_FALLBACK_PATHS="\
/run/nvidia/driver/usr/lib/x86_64-linux-gnu:\
/home/kubernetes/bin/nvidia/lib64:\
/usr/local/nvidia/lib64:\
/usr/lib/x86_64-linux-gnu"

# Resolve our own directory BEFORE touching LD_LIBRARY_PATH — readlink/dirname
# themselves die once the path is exported (host/driver dirs ship their own
# libc.so.6; mixing it with this container's ld-linux aborts with
# "undefined symbol __tunable_is_initialized").
DIR="$(dirname "$(readlink -f "$0")")"

# Container's own libdir FIRST so libc/libm resolve from this container; the
# host/driver dirs after it only supply libs missing here (libcuda.so.1).
LD_PATH="/usr/lib/x86_64-linux-gnu"
if [ -n "${NVSNAP_CUDA_LIB_DIR:-}" ]; then
    LD_PATH="${LD_PATH}:${NVSNAP_CUDA_LIB_DIR}"
fi
LD_PATH="${LD_PATH}:${FALLBACK_PATHS}:${HOST_FALLBACK_PATHS}"
if [ -n "${LD_LIBRARY_PATH:-}" ]; then
    LD_PATH="${LD_PATH}:${LD_LIBRARY_PATH}"
fi
export LD_LIBRARY_PATH="${LD_PATH}"

exec "${DIR}/cuda-checkpoint.real" "$@"
