#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Stage /criu-bundle into a destination tree (function-pod hostPath
# mounts via the nvsnap-agent DaemonSet — see deploy/helm/nvsnap/templates/
# agent-daemonset.yaml — or direct emptyDir mounts for legacy/test
# workloads).
#
# Two destinations:
#   $NVSNAP_BUNDLE_TOOLS_DST  (default /nvsnap)      — restore-entrypoint +
#                                                  criu + cuda-checkpoint
#                                                  + plugins + lib/. The
#                                                  webhook rewrites the
#                                                  workload's main
#                                                  container command to
#                                                  $TOOLS/restore-entrypoint.
# The intercept payload (libnvsnap_intercept.so, patched uvloop/libuv/
# libzmq, sitecustomize) is no longer staged: criu-v2 dumps and restores
# in-namespace, so no userspace interception is injected into workloads.
# lib/nvsnap_intercept/ stays in-tree for future multi-GPU work.

set -euo pipefail

NVSNAP_DST="${NVSNAP_BUNDLE_TOOLS_DST:-/nvsnap}"

if [[ ! -d /criu-bundle ]]; then
  echo "restore-bundle-init: /criu-bundle missing in agent image" >&2
  exit 1
fi

# Atomic rename of a populated TMP into DST. Caller MUST have already
# populated TMP. Stale `.new` / `.old` siblings from a prior crashed
# run are cleaned first; the post-mv cleanup of `.old` may race a
# very recently-scheduled pod still holding the old inode, which is
# fine — the kernel only frees the inode once that pod's mount goes
# away.
atomic_swap() {
  local dst="$1"
  local tmp="$2"
  if [[ -d "$dst" ]]; then
    local old="${dst}.old"
    rm -rf "$old"
    mv "$dst" "$old"
    mv "$tmp" "$dst"
    rm -rf "$old" || true
  else
    mkdir -p "$(dirname "$dst")"
    mv "$tmp" "$dst"
  fi
}

# ─── Phase 1: tools tree ──────────────────────────────────────────────
TOOLS_TMP="${NVSNAP_DST}.new"
rm -rf "$TOOLS_TMP"
mkdir -p "$TOOLS_TMP"
# cp -a preserves modes (the exec bit on criu, restore-entrypoint,
# cuda-checkpoint). Trailing /. on the source copies contents-of, not
# the directory itself.
cp -a /criu-bundle/. "$TOOLS_TMP/"
if [[ -x /usr/local/bin/py-spy ]]; then cp /usr/local/bin/py-spy "$TOOLS_TMP/"; fi
if [[ -x /usr/bin/nsenter ]];     then cp /usr/bin/nsenter     "$TOOLS_TMP/"; fi
atomic_swap "$NVSNAP_DST" "$TOOLS_TMP"

# ─── Sanity checks ────────────────────────────────────────────────────
if [[ ! -x "$NVSNAP_DST/restore-entrypoint" ]]; then
  echo "restore-bundle-init: $NVSNAP_DST/restore-entrypoint missing or not executable" >&2
  ls -la "$NVSNAP_DST" >&2
  exit 1
fi
echo "restore-bundle-init: staged into $NVSNAP_DST"
