#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Stage /criu-bundle into a destination tree (function-pod hostPath
# mounts via the nvsnap-agent DaemonSet — see deploy/helm/nvsnap/templates/
# agent-daemonset.yaml — or direct emptyDir mounts for legacy/test
# workloads).
#
# Two destinations:
#   $NVSNAP_BUNDLE_TOOLS_DST  (default /nvsnap)      — criu bundle +
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

# Install a populated TMP as DST. DST keeps its directory: pods bind-mount
# it (/nvsnap in every GPU pod under gpushare), and replacing the
# directory left every such mount pointing at a deleted one. A CRIU
# capture of those pods then recorded the mount as deleted and could not
# be restored ("Can't stat : Bad file descriptor" re-creating it). So each
# file is renamed over its old copy inside DST: atomic per file, and a
# running process keeps the file it already mapped.
atomic_swap() {
  local dst="$1"
  local tmp="$2"
  if [[ ! -d "$dst" ]]; then
    mkdir -p "$(dirname "$dst")"
    mv "$tmp" "$dst"
    return
  fi
  (cd "$tmp" && find . -mindepth 1 -type d -printf '%P\n') | while IFS= read -r d; do
    mkdir -p "$dst/$d"
  done
  (cd "$tmp" && find . -mindepth 1 ! -type d -printf '%P\n') | while IFS= read -r f; do
    mv -f "$tmp/$f" "$dst/$f"
  done
  rm -rf "$tmp"
}

# ─── Phase 1: tools tree ──────────────────────────────────────────────
TOOLS_TMP="${NVSNAP_DST}.new"
rm -rf "$TOOLS_TMP"
mkdir -p "$TOOLS_TMP"
# cp -a preserves modes (the exec bit on criu, nvsnap-rootfs-restore,
# cuda-checkpoint). Trailing /. on the source copies contents-of, not
# the directory itself.
cp -a /criu-bundle/. "$TOOLS_TMP/"
if [[ -x /usr/local/bin/py-spy ]]; then cp /usr/local/bin/py-spy "$TOOLS_TMP/"; fi
if [[ -x /usr/bin/nsenter ]];     then cp /usr/bin/nsenter     "$TOOLS_TMP/"; fi

# ─── Phase 1b: versioned copy (docs/design/VERSIONED-NODE-BUNDLE.md) ──
# Pods mount <root>/versions/<id>, never changed once staged: a process
# keeps mapping the files it started with, and a checkpoint's restore mounts
# the version its capture ran. The id is the content hash of the bundle.
ROOT="$(dirname "$NVSNAP_DST")"
VERSIONS="$ROOT/versions"
BUNDLE_ID="$(cd "$TOOLS_TMP" && find . -type f -print0 | LC_ALL=C sort -z | xargs -0 sha256sum | sha256sum | cut -c1-16)"
mkdir -p "$VERSIONS"
if [[ ! -d "$VERSIONS/$BUNDLE_ID" ]]; then
  rm -rf "$VERSIONS/.$BUNDLE_ID.tmp"
  cp -a "$TOOLS_TMP" "$VERSIONS/.$BUNDLE_ID.tmp"
  mv "$VERSIONS/.$BUNDLE_ID.tmp" "$VERSIONS/$BUNDLE_ID"
  echo "restore-bundle-init: staged version $BUNDLE_ID"
fi
echo "$BUNDLE_ID" > "$ROOT/.current-bundle.tmp"
mv -f "$ROOT/.current-bundle.tmp" "$ROOT/current-bundle"

# Legacy tree, for pods admitted before versions.
atomic_swap "$NVSNAP_DST" "$TOOLS_TMP"

# ─── Sanity checks ────────────────────────────────────────────────────
if [[ ! -x "$NVSNAP_DST/criu" ]]; then
  echo "restore-bundle-init: $NVSNAP_DST/criu missing or not executable" >&2
  ls -la "$NVSNAP_DST" >&2
  exit 1
fi
if [[ ! -x "$VERSIONS/$BUNDLE_ID/criu" ]]; then
  echo "restore-bundle-init: $VERSIONS/$BUNDLE_ID/criu missing or not executable" >&2
  exit 1
fi
echo "restore-bundle-init: staged into $NVSNAP_DST and version $BUNDLE_ID"
