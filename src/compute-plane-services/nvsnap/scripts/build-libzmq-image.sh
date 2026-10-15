#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Build libzmq builder image with checkpoint/restore support
# This image contains libzmq.so with checkpoint APIs for injection into workloads

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"

# Logging
log_info() { echo "[INFO] $*"; }
log_error() { echo "[ERROR] $*" >&2; }

# Configuration
REGISTRY="${REGISTRY:-nvcr.io/0651155215864979/ncp-dev}"
IMAGE_NAME="libzmq-builder"
VERSION="${VERSION:-v4.3.6-checkpoint-v1}"
source "$(dirname "${BASH_SOURCE[0]}")/_deps.sh"
nvsnap_resolve_sibling LIBZMQ_SRC libzmq
LIBZMQ_BRANCH="${LIBZMQ_BRANCH:-checkpoint-restore-v1}"
CONTAINER_TOOL="${CONTAINER_TOOL:-docker}"

# Validate source
if [ ! -d "$LIBZMQ_SRC" ]; then
    log_error "libzmq source not found at $LIBZMQ_SRC"
    exit 1
fi

log_info "Building libzmq builder image"
log_info "Version: $VERSION"
log_info "Source: $LIBZMQ_SRC"
log_info "Branch: $LIBZMQ_BRANCH"

# Create build context
BUILD_CONTEXT=$(mktemp -d)
trap "rm -rf $BUILD_CONTEXT" EXIT

log_info "Preparing build context..."

# Copy libzmq source (include builds/ directory for CMake helpers)
rsync -a --exclude='.git' --exclude='build' \
    "$LIBZMQ_SRC/" "$BUILD_CONTEXT/libzmq-src/"

# Create Dockerfile
cat > "$BUILD_CONTEXT/Dockerfile" <<'EOF'
FROM ubuntu:22.04

RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential \
    cmake \
    git \
    pkg-config \
    libsodium-dev \
    && rm -rf /var/lib/apt/lists/*

# Copy libzmq source with checkpoint support
COPY libzmq-src/ /libzmq-src/
WORKDIR /libzmq-src

# Build and install
RUN mkdir -p build && cd build && \
    cmake -DCMAKE_INSTALL_PREFIX=/usr/local \
          -DCMAKE_BUILD_TYPE=Release \
          -DBUILD_TESTS=OFF \
          .. && \
    make -j$(nproc) && \
    make install && \
    ldconfig

# Verify checkpoint API is present
RUN nm -D /usr/local/lib/libzmq.so | grep -E "zmq_ctx_checkpoint|zmq_ctx_restore|zmq_get_all_contexts" || \
    (echo "ERROR: Checkpoint API not found in libzmq.so"; exit 1)

# Show version info
RUN echo "libzmq version:" && \
    strings /usr/local/lib/libzmq.so | grep -E "^4\.[0-9]\.[0-9]" | head -1 && \
    echo "Checkpoint API symbols:" && \
    nm -D /usr/local/lib/libzmq.so | grep checkpoint

# Default command for testing
CMD ["/bin/bash"]
EOF

# Build image
log_info "Building Docker image..."
FULL_IMAGE="${REGISTRY}/${IMAGE_NAME}:${VERSION}"

cd "$BUILD_CONTEXT"

"${CONTAINER_TOOL}" build \
    -t "$FULL_IMAGE" \
    -t "${REGISTRY}/${IMAGE_NAME}:latest" \
    .

log_info "Built: $FULL_IMAGE"

# Push to registry
if [ "${PUSH:-true}" = "true" ]; then
    log_info "Pushing to registry..."
    "${CONTAINER_TOOL}" push "$FULL_IMAGE"
    "${CONTAINER_TOOL}" push "${REGISTRY}/${IMAGE_NAME}:latest"
    log_info "Pushed: $FULL_IMAGE"
fi

echo ""
log_info "=========================================="
log_info "libzmq builder image ready: $FULL_IMAGE"
log_info "=========================================="
echo ""
echo "Usage in vLLM pod:"
echo "  Copy from image: COPY --from=$FULL_IMAGE /usr/local/lib/libzmq.so* /usr/local/lib/"
echo "  Or init container to inject at runtime"
echo ""
