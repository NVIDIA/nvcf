#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Build + push one nvsnap image, idempotently.
#
#   ci/build-image.sh <component>
#
# <component> is one of:
#   base server blobstore l2-wait        # self-contained / CRIU
#   uvloop libzmq libuv pyzmq            # dependency builders (clone forks)
#   agent init                           # layer on the base + dep images
#
# Tags come from scripts/versions.sh (the single source of truth). The
# build is idempotent: if the target tag already exists in the registry
# the build is skipped, so re-running a pipeline is cheap and bumping a
# version in versions.sh is what triggers a real (re)build — matching
# CLAUDE.md rule 19 (never reuse a tag on rebuild).
#
# Env knobs:
#   BUILDER   build tool — "buildah" (CI default) or "docker" (local).
#   PUSH      push after build (default 1; set 0 to build only, e.g. local test).
#   NO_SKIP   set 1 to force a rebuild even if the tag exists.
set -euo pipefail

COMPONENT="${1:?usage: build-image.sh <component>}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
# shellcheck disable=SC1091
. scripts/versions.sh

BUILDER="${BUILDER:-buildah}"
PUSH="${PUSH:-1}"
REG="$NVSNAP_REGISTRY"

# bud: thin alias over the chosen builder's build verb.
bud() { if [ "$BUILDER" = "buildah" ]; then "$BUILDER" bud "$@"; else "$BUILDER" build "$@"; fi; }

# exists: does this tag already live in the registry?
exists() { "$BUILDER" pull "$1" >/dev/null 2>&1; }

# push_img: push unless PUSH=0.
push_img() { [ "$PUSH" = "1" ] && "$BUILDER" push "$1" || echo "[build-image] PUSH=0, not pushing $1"; }

# guard_ref: fail loud if a fork ref isn't pinned (don't guess a branch).
guard_ref() { [ -n "$2" ] || { echo "[build-image] $1 is unset — pin the fork ref in scripts/versions.sh" >&2; exit 2; }; }

# clone_fork: shallow-clone a fork at a ref into $1.
clone_fork() { git clone --depth 1 --branch "$3" "$2" "$1"; }

build() {
    local img="$1"; shift
    if [ "${NO_SKIP:-0}" != "1" ] && exists "$img"; then
        echo "[build-image] $img already exists; skipping."
        return 0
    fi
    echo "[build-image] building $img"
    bud "$@" -t "$img" .
    push_img "$img"
}

case "$COMPONENT" in

  # ── self-contained Go images (context = repo root) ──────────────────
  server)
    build "$REG/nvsnap-server:$NVSNAP_SERVER_VERSION" \
        -f docker/Dockerfile.server --build-arg VERSION="$NVSNAP_SERVER_VERSION"
    ;;
  blobstore)
    build "$REG/nvsnap-blobstore:$NVSNAP_BLOBSTORE_VERSION" \
        -f docker/nvsnap-blobstore/Dockerfile --build-arg VERSION="$NVSNAP_BLOBSTORE_VERSION"
    ;;
  l2-wait)
    build "$REG/nvsnap-l2-wait:$NVSNAP_L2WAIT_VERSION" \
        -f docker/nvsnap-l2-wait/Dockerfile
    ;;

  # ── base: CRIU bundle (context = docker/agent + cloned criu-src) ────
  base)
    base_img="$REG/nvsnap-agent-base:$NVSNAP_BASE_VERSION"
    if [ "${NO_SKIP:-0}" != "1" ] && exists "$base_img"; then
        echo "[build-image] $base_img already exists; skipping."; exit 0
    fi
    guard_ref NVSNAP_CRIU_REF "$NVSNAP_CRIU_REF"
    ctx="$ROOT/docker/agent"
    trap 'rm -rf "$ctx/criu-src"' EXIT
    rm -rf "$ctx/criu-src"
    clone_fork "$ctx/criu-src" "$NVSNAP_CRIU_REPO" "$NVSNAP_CRIU_REF"
    ( cd "$ctx" && bud -f Dockerfile.base -t "$base_img" . )
    push_img "$base_img"
    ;;

  # ── dependency builders (context = the cloned fork) ─────────────────
  uvloop)
    img="$REG/uvloop-builder:$NVSNAP_UVLOOP_VERSION"
    if [ "${NO_SKIP:-0}" != "1" ] && exists "$img"; then echo "[build-image] $img exists; skipping."; exit 0; fi
    guard_ref NVSNAP_UVLOOP_REF "$NVSNAP_UVLOOP_REF"
    tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
    clone_fork "$tmp/src" "$NVSNAP_UVLOOP_REPO" "$NVSNAP_UVLOOP_REF"
    bud -f "$ROOT/docker/uvloop/Dockerfile" -t "$img" "$tmp/src"
    push_img "$img"
    ;;
  libzmq)
    img="$REG/libzmq-builder:$NVSNAP_LIBZMQ_VERSION"
    if [ "${NO_SKIP:-0}" != "1" ] && exists "$img"; then echo "[build-image] $img exists; skipping."; exit 0; fi
    guard_ref NVSNAP_LIBZMQ_REF "$NVSNAP_LIBZMQ_REF"
    tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
    clone_fork "$tmp/src" "$NVSNAP_LIBZMQ_REPO" "$NVSNAP_LIBZMQ_REF"
    bud -f "$ROOT/docker/libzmq/Dockerfile" -t "$img" "$tmp/src"
    push_img "$img"
    ;;
  libuv)
    img="$REG/libuv-builder:$NVSNAP_LIBUV_VERSION"
    if [ "${NO_SKIP:-0}" != "1" ] && exists "$img"; then echo "[build-image] $img exists; skipping."; exit 0; fi
    guard_ref NVSNAP_LIBUV_REF "$NVSNAP_LIBUV_REF"
    tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
    clone_fork "$tmp/src" "$NVSNAP_LIBUV_REPO" "$NVSNAP_LIBUV_REF"
    bud -f "$ROOT/docker/libuv/Dockerfile" -t "$img" "$tmp/src"
    push_img "$img"
    ;;
  pyzmq)
    # pyzmq builds against the libzmq source: context holds both
    # libzmq-src/ and pyzmq-src/ (see docker/pyzmq/Dockerfile).
    img="$REG/pyzmq-builder:$NVSNAP_PYZMQ_VERSION"
    if [ "${NO_SKIP:-0}" != "1" ] && exists "$img"; then echo "[build-image] $img exists; skipping."; exit 0; fi
    guard_ref NVSNAP_PYZMQ_REF "$NVSNAP_PYZMQ_REF"
    guard_ref NVSNAP_LIBZMQ_REF "$NVSNAP_LIBZMQ_REF"
    tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
    clone_fork "$tmp/pyzmq-src" "$NVSNAP_PYZMQ_REPO" "$NVSNAP_PYZMQ_REF"
    clone_fork "$tmp/libzmq-src" "$NVSNAP_LIBZMQ_REPO" "$NVSNAP_LIBZMQ_REF"
    bud -f "$ROOT/docker/pyzmq/Dockerfile" -t "$img" "$tmp"
    push_img "$img"
    ;;

  # ── agent: app layer over base + dep images (context = repo root) ───
  agent)
    img="$REG/nvsnap-agent:$NVSNAP_APP_VERSION"
    if [ "${NO_SKIP:-0}" != "1" ] && exists "$img"; then echo "[build-image] $img exists; skipping."; exit 0; fi
    # Dockerfile.app's final stage COPYs cuda-checkpoint-wrapper.sh from
    # the context root; stage it there for the build, then clean up.
    cp docker/agent/cuda-checkpoint-wrapper.sh ./cuda-checkpoint-wrapper.sh
    trap 'rm -f "$ROOT/cuda-checkpoint-wrapper.sh"' EXIT
    bud -f docker/agent/Dockerfile.app \
        --build-arg BASE_IMAGE="$REG/nvsnap-agent-base:$NVSNAP_BASE_VERSION" \
        --build-arg UVLOOP_IMAGE="$REG/uvloop-builder:$NVSNAP_UVLOOP_VERSION" \
        --build-arg LIBUV_IMAGE="$REG/libuv-builder:$NVSNAP_LIBUV_VERSION" \
        --build-arg LIBZMQ_IMAGE="$REG/libzmq-builder:$NVSNAP_LIBZMQ_VERSION" \
        -t "$img" .
    push_img "$img"
    ;;

  # ── init: assembles dep + agent images (context = repo root) ────────
  init)
    build "$REG/nvsnap-init:$NVSNAP_INIT_VERSION" \
        -f docker/init/Dockerfile \
        --build-arg UVLOOP_IMAGE="$REG/uvloop-builder:$NVSNAP_UVLOOP_VERSION" \
        --build-arg LIBUV_IMAGE="$REG/libuv-builder:$NVSNAP_LIBUV_VERSION" \
        --build-arg LIBZMQ_IMAGE="$REG/libzmq-builder:$NVSNAP_LIBZMQ_VERSION" \
        --build-arg PYZMQ_IMAGE="$REG/pyzmq-builder:$NVSNAP_PYZMQ_VERSION" \
        --build-arg AGENT_IMAGE="$REG/nvsnap-agent:$NVSNAP_APP_VERSION"
    ;;

  *)
    echo "unknown component: $COMPONENT" >&2
    exit 2
    ;;
esac
