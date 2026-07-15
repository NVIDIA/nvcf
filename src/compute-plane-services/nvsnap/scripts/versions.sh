#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# NVSNAP Version Registry — Single Source of Truth
#
# Source this file from build/deploy scripts:
#   source "$(dirname "${BASH_SOURCE[0]}")/versions.sh"
#
# All variables support env var overrides:
#   NVSNAP_APP_VERSION=v0.9.99 ./scripts/build-agent.sh app

# Registry (NGC, ncp-dev sub-path, migrated from stg.nvcr.io/zq9tgrjzrfpo
# on 2026-05-21 — all images reset to v0.0.1).
NVSNAP_REGISTRY="${NVSNAP_REGISTRY:-nvcr.io/0651155215864979/ncp-dev}"

# CRIU source directory.
#
# NvSnap depends on a forked CRIU with NVIDIA-specific patches. The fork
# lives in its own repository (see CONTRIBUTING.md for the current URL —
# it's expected to move, so the build is decoupled from any specific
# remote and looks for a local checkout instead).
#
# Lookup order:
#   1. $NVSNAP_CRIU_SRC if set explicitly
#   2. ../criu sibling to this repo (default OSS contributor layout)
#   3. ../nvsnap-criu sibling (alternate naming if the fork lives under
#      a project name rather than the upstream "criu" name)
#
# Errors fast in build scripts if none resolve.
if [ -z "${NVSNAP_CRIU_SRC:-}" ]; then
    _gpucr_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
    for _candidate in "$_gpucr_root/../criu" "$_gpucr_root/../nvsnap-criu"; do
        if [ -d "$_candidate/.git" ]; then
            NVSNAP_CRIU_SRC="$(cd "$_candidate" && pwd)"
            break
        fi
    done
    unset _gpucr_root _candidate
fi
NVSNAP_CRIU_SRC="${NVSNAP_CRIU_SRC:-}"

# Core images — all on a fresh v0.0.1 baseline. Bump per CLAUDE.md
# rule 19 (never reuse a tag on rebuild).
NVSNAP_BASE_VERSION="${NVSNAP_BASE_VERSION:-v0.0.7}"
NVSNAP_APP_VERSION="${NVSNAP_APP_VERSION:-v0.2.17}"
NVSNAP_SERVER_VERSION="${NVSNAP_SERVER_VERSION:-v0.0.31}"
NVSNAP_BLOBSTORE_VERSION="${NVSNAP_BLOBSTORE_VERSION:-v0.0.1}"
NVSNAP_L2WAIT_VERSION="${NVSNAP_L2WAIT_VERSION:-v0.0.1}"

# Dependency builder images — also reset to v0.0.1. They retain their
# CRIU/NCCL patches; the version is just a new tag on the new registry.
NVSNAP_UVLOOP_VERSION="${NVSNAP_UVLOOP_VERSION:-v0.0.1}"
NVSNAP_LIBZMQ_VERSION="${NVSNAP_LIBZMQ_VERSION:-v0.0.1}"
NVSNAP_PYZMQ_VERSION="${NVSNAP_PYZMQ_VERSION:-v0.0.1}"
NVSNAP_LIBUV_VERSION="${NVSNAP_LIBUV_VERSION:-v0.0.1}"

# Dependency / CRIU fork repos + refs. All forks now live under
# github.com/balajinvda. These are consumed by ci/build-image.sh when a
# dep-builder or base image actually has to be (re)built; day to day the
# images are already in the registry and the idempotent build is a no-op.
# Leave a ref blank if it is not yet pinned — ci/build-image.sh errors
# with the var name rather than guessing a branch.
NVSNAP_CRIU_REPO="${NVSNAP_CRIU_REPO:-https://github.com/balajinvda/criu.git}"
# Pinned SHA on the io-uring-cr branch (rebased on upstream criu-dev + LZ4
# compression #2895) for reproducible OSS builds. Bump when the fork advances.
# Consumed by build-agent.sh (clean-checkout auto-clone) and ci/build-image.sh.
NVSNAP_CRIU_REF="${NVSNAP_CRIU_REF:-a25eb7b8b139c52b3ebcbe5e88e2015592db54a8}"
NVSNAP_LIBZMQ_REPO="${NVSNAP_LIBZMQ_REPO:-https://github.com/balajinvda/libzmq.git}"
NVSNAP_LIBZMQ_REF="${NVSNAP_LIBZMQ_REF:-checkpoint-restore-v1}"
NVSNAP_LIBUV_REPO="${NVSNAP_LIBUV_REPO:-https://github.com/balajinvda/libuv.git}"
NVSNAP_LIBUV_REF="${NVSNAP_LIBUV_REF:-fix/issue-41-no-sqarray}"
NVSNAP_UVLOOP_REPO="${NVSNAP_UVLOOP_REPO:-https://github.com/balajinvda/uvloop.git}"
NVSNAP_UVLOOP_REF="${NVSNAP_UVLOOP_REF:-checkpoint-restore-v1}"
NVSNAP_PYZMQ_REPO="${NVSNAP_PYZMQ_REPO:-https://github.com/balajinvda/pyzmq.git}"
NVSNAP_PYZMQ_REF="${NVSNAP_PYZMQ_REF:-main}"

# Combined init container — always matches agent version to prevent build-ID mismatch
NVSNAP_INIT_VERSION="${NVSNAP_INIT_VERSION:-${NVSNAP_APP_VERSION}}"

# vLLM base image
NVSNAP_VLLM_VERSION="${NVSNAP_VLLM_VERSION:-v0.20.0}"

# Host path for checkpoint storage (must match agent's --checkpoint-dir default)
NVSNAP_CHECKPOINT_HOST_PATH="${NVSNAP_CHECKPOINT_HOST_PATH:-/var/lib/containerd/nvsnap-checkpoints}"
