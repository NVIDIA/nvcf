#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Build uvloop wheels (cp310/311/312/313) with CRIU checkpoint/restore
# support. See docs/GENERIC-PYTHON-INJECTION-DESIGN.md for the design.
#
# The patch adds ~15 lines of Cython to uvloop/loop.pyx:
#   - detects CRIU restore via /run/criu-restored marker
#   - calls uv_loop_fork() on first _run() after restore
#   - reinitializes libuv kernel state (epoll, signal pipes, io_uring)
#
# Builder image: docker/uvloop/Dockerfile (manylinux_2_28_x86_64 base).
# Output image carries 4 wheels under /wheels/, one per Python ABI tag.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"

log_info()  { echo "[INFO] $*"; }
log_error() { echo "[ERROR] $*" >&2; }

REGISTRY="${REGISTRY:-nvcr.io/0651155215864979/ncp-dev}"
IMAGE_NAME="uvloop-builder"
VERSION="${VERSION:-v0.22.1-multipy1}"
source "$(dirname "${BASH_SOURCE[0]}")/_deps.sh"
nvsnap_resolve_sibling UVLOOP_SRC uvloop
UVLOOP_BRANCH="${UVLOOP_BRANCH:-checkpoint-restore-v1}"
CONTAINER_TOOL="${CONTAINER_TOOL:-docker}"
PUSH="${PUSH:-true}"

if [ ! -d "$UVLOOP_SRC" ]; then
    log_error "uvloop source not found at $UVLOOP_SRC"
    exit 1
fi

log_info "uvloop builder"
log_info "  version: $VERSION"
log_info "  source : $UVLOOP_SRC (branch $UVLOOP_BRANCH)"

# Prepare build context: uvloop source + checked-in Dockerfile.
# We rsync the source rather than mounting so the Docker daemon can use it
# even when the daemon runs remotely.
BUILD_CONTEXT="$(mktemp -d)"
trap 'rm -rf "$BUILD_CONTEXT"' EXIT

log_info "Preparing build context at $BUILD_CONTEXT"
rsync -a \
    --exclude='.git' \
    --exclude='build' \
    --exclude='dist' \
    --exclude='*.egg-info' \
    --exclude='__pycache__' \
    --exclude='*.pyc' \
    --exclude='*.so' \
    "$UVLOOP_SRC/" "$BUILD_CONTEXT/"

# Use the checked-in Dockerfile (no inline heredoc — see CLAUDE.md rule #13).
cp "$PROJECT_ROOT/docker/uvloop/Dockerfile" "$BUILD_CONTEXT/Dockerfile"

FULL_IMAGE="${REGISTRY}/${IMAGE_NAME}:${VERSION}"
log_info "Building $FULL_IMAGE"

"${CONTAINER_TOOL}" build \
    -t "$FULL_IMAGE" \
    -t "${REGISTRY}/${IMAGE_NAME}:latest" \
    "$BUILD_CONTEXT"

log_info "Built $FULL_IMAGE"

if [ "$PUSH" = "true" ]; then
    log_info "Pushing..."
    "${CONTAINER_TOOL}" push "$FULL_IMAGE"
    "${CONTAINER_TOOL}" push "${REGISTRY}/${IMAGE_NAME}:latest"
    log_info "Pushed $FULL_IMAGE"
fi

echo
log_info "=================================================="
log_info "uvloop multi-Python builder ready: $FULL_IMAGE"
log_info "  /wheels/uvloop-*-cp310-cp310-*.whl"
log_info "  /wheels/uvloop-*-cp311-cp311-*.whl"
log_info "  /wheels/uvloop-*-cp312-cp312-*.whl"
log_info "  /wheels/uvloop-*-cp313-cp313-*.whl"
log_info "=================================================="
echo
echo "Init container snippet (per docs/GENERIC-PYTHON-INJECTION-DESIGN.md):"
cat <<'EOF'
  - name: get-uvloop
    image: nvcr.io/0651155215864979/ncp-dev/uvloop-builder:VERSION
    command: ["/bin/sh", "-c"]
    args:
      - |
        for whl in /wheels/uvloop-*.whl; do
            tag=$(echo "$whl" | grep -oE 'cp3[0-9]+' | head -1)
            mkdir -p "/nvsnap-lib/site-packages-${tag}"
            python3 -m zipfile -e "$whl" "/nvsnap-lib/site-packages-${tag}/"
        done
    volumeMounts:
      - { name: nvsnap-lib, mountPath: /nvsnap-lib }
EOF
