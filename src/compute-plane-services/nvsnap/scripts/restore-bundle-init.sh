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
#   $NVSNAP_BUNDLE_LIB_DST    (default /nvsnap-lib)  — libnvsnap_intercept.so +
#                                                  sitecustomize + uvloop
#                                                  wheels + libuv.so +
#                                                  libzmq.so. CRIU restore
#                                                  re-mmaps libnvsnap from
#                                                  the path the captured
#                                                  process was using, so
#                                                  this MUST stay at the
#                                                  exact path on every
#                                                  node.
#
# Atomicity: each destination is written to a `.new` sibling, then
# rename'd into place. Existing kubelet hostPath mounts are pinned to
# the directory's inode at mount time, so renaming the dir on the host
# does NOT affect pods that are already using the old version — they
# keep their inode. Newly-scheduled pods see the new version. This
# matters during agent rolling-upgrade: function pods restoring on
# nodes that haven't yet upgraded keep using the old bundle.
#
# Idempotent: re-running on the same agent image is a no-op (cp -a
# rewrites identical content; rename produces the same end state).
# Caller doesn't need to track whether staging already ran.

set -euo pipefail

NVSNAP_DST="${NVSNAP_BUNDLE_TOOLS_DST:-/nvsnap}"
LIB_DST="${NVSNAP_BUNDLE_LIB_DST:-/nvsnap-lib}"

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

# ─── Phase 2: intercept-lib tree ──────────────────────────────────────
LIB_TMP="${LIB_DST}.new"
rm -rf "$LIB_TMP"
mkdir -p "$LIB_TMP"
for whl in /criu-bundle/payload/wheels/uvloop-*.whl; do
  if [[ ! -e "$whl" ]]; then
    echo "restore-bundle-init: no uvloop wheels at /criu-bundle/payload/wheels/" >&2
    exit 1
  fi
  tag=$(echo "$whl" | grep -oE 'cp3[0-9]+' | head -1)
  if [[ -z "$tag" ]]; then
    echo "restore-bundle-init: cannot extract ABI tag from $whl" >&2
    exit 1
  fi
  mkdir -p "$LIB_TMP/site-packages-${tag}"
  python3 -m zipfile -e "$whl" "$LIB_TMP/site-packages-${tag}/"
done
cp /criu-bundle/payload/lib/libuv.so*  "$LIB_TMP/"
cp /criu-bundle/payload/lib/libzmq.so* "$LIB_TMP/"
cp /criu-bundle/lib/libnvsnap_intercept.so "$LIB_TMP/"
cp -r /criu-bundle/sitecustomize "$LIB_TMP/"
atomic_swap "$LIB_DST" "$LIB_TMP"

# ─── Sanity checks ────────────────────────────────────────────────────
if [[ ! -x "$NVSNAP_DST/restore-entrypoint" ]]; then
  echo "restore-bundle-init: $NVSNAP_DST/restore-entrypoint missing or not executable" >&2
  ls -la "$NVSNAP_DST" >&2
  exit 1
fi
if [[ ! -e "$LIB_DST/libnvsnap_intercept.so" ]]; then
  echo "restore-bundle-init: $LIB_DST/libnvsnap_intercept.so missing — restore will fail at mmap" >&2
  ls -la "$LIB_DST" >&2
  exit 1
fi

echo "restore-bundle-init: staged into $NVSNAP_DST + $LIB_DST"
