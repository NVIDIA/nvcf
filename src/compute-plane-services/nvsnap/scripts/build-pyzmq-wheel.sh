#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Build pyzmq wheel linked against our patched libzmq (CRIU restore support)
#
# Stock pyzmq wheels bundle their own libzmq inside pyzmq.libs/.
# This means our patched libzmq (with epoll reinit for CRIU restore)
# is completely bypassed — pyzmq calls the bundled stock copy.
#
# This script builds pyzmq from source with ZMQ_PREFIX pointing to our
# patched libzmq installation. The resulting wheel links against the
# system libzmq.so.5 (no bundled copy), so at runtime it uses whatever
# libzmq.so.5 is on LD_LIBRARY_PATH — our patched version.
#
# Same pattern as build-uvloop-wheel.sh.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"

# Logging
log_info() { echo "[INFO] $*"; }
log_error() { echo "[ERROR] $*" >&2; }

# Configuration
REGISTRY="${REGISTRY:-nvcr.io/0651155215864979/ncp-dev}"
IMAGE_NAME="pyzmq-builder"
VERSION="${VERSION:-v27.2.0-gpucr1}"
source "$(dirname "${BASH_SOURCE[0]}")/_deps.sh"
nvsnap_resolve_sibling PYZMQ_SRC pyzmq
nvsnap_resolve_sibling LIBZMQ_SRC libzmq
CONTAINER_TOOL="${CONTAINER_TOOL:-docker}"
# Build against vLLM base to match GLIBC version (2.35, Ubuntu 22.04)
BUILDER_BASE="${BUILDER_BASE:-vllm/vllm-openai:v0.11.2}"
PUSH="${PUSH:-true}"

# Validate sources
if [ ! -d "$PYZMQ_SRC" ]; then
    log_error "pyzmq source not found at $PYZMQ_SRC"
    exit 1
fi
if [ ! -d "$LIBZMQ_SRC" ]; then
    log_error "libzmq source not found at $LIBZMQ_SRC"
    exit 1
fi

log_info "Building pyzmq builder image"
log_info "Version: $VERSION"
log_info "pyzmq source: $PYZMQ_SRC"
log_info "libzmq source: $LIBZMQ_SRC"

# Create build context
BUILD_CONTEXT=$(mktemp -d)
trap "rm -rf $BUILD_CONTEXT" EXIT

log_info "Preparing build context..."

# Copy pyzmq source
rsync -a \
    --exclude='.git' \
    --exclude='build' \
    --exclude='dist' \
    --exclude='*.egg-info' \
    --exclude='__pycache__' \
    --exclude='*.pyc' \
    --exclude='*.so' \
    --exclude='buildutils/bundled' \
    "$PYZMQ_SRC/" "$BUILD_CONTEXT/pyzmq-src/"

# Copy libzmq source (for building from source inside Docker)
rsync -a \
    --exclude='.git' \
    --exclude='build' \
    "$LIBZMQ_SRC/" "$BUILD_CONTEXT/libzmq-src/"

# Create Dockerfile
cat > "$BUILD_CONTEXT/Dockerfile" <<DOCKERFILE
FROM ${BUILDER_BASE} AS builder

RUN apt-get update && apt-get install -y --no-install-recommends \\
    build-essential \\
    cmake \\
    git \\
    pkg-config \\
    libsodium-dev \\
    && rm -rf /var/lib/apt/lists/*

# Build and install our patched libzmq first
COPY libzmq-src/ /libzmq-src/
RUN cd /libzmq-src && mkdir -p build && cd build && \\
    cmake -DCMAKE_INSTALL_PREFIX=/usr/local \\
          -DCMAKE_BUILD_TYPE=Release \\
          -DBUILD_TESTS=OFF \\
          .. && \\
    make -j\$(nproc) && \\
    make install && \\
    ldconfig

# Build pyzmq against our installed libzmq (not bundled)
COPY pyzmq-src/ /pyzmq-src/
WORKDIR /pyzmq-src

# Install pyzmq build dependencies
RUN pip install 'cython>=3.0.0' 'packaging' 'scikit-build-core>=0.10'

# Build pyzmq with ZMQ_PREFIX=/usr/local (our patched libzmq)
# PYZMQ_NO_BUNDLE=1 prevents fallback to bundled libzmq
RUN ZMQ_PREFIX=/usr/local PYZMQ_NO_BUNDLE=1 \\
    pip wheel . -w /wheels/ --no-build-isolation -v

# Verify: the wheel should NOT contain bundled libzmq
RUN echo "=== Wheel contents ===" && \\
    unzip -l /wheels/pyzmq-*.whl | grep -i "libzmq" && \\
    echo "(should show NO bundled libzmq-*.so files)" || true

# Verify: pyzmq loads and uses our patched libzmq
# Run from /tmp to avoid importing source tree instead of installed wheel
RUN pip install --force-reinstall /wheels/pyzmq-*.whl && \\
    cd /tmp && python3 -c "import zmq; print('pyzmq version:', zmq.__version__); print('zmq.zmq_version():', zmq.zmq_version()); print('Using system libzmq (CRIU restore support)')"

# Minimal output image with just the wheel
FROM python:3.12-slim
COPY --from=builder /wheels/ /wheels/
CMD ["ls", "-la", "/wheels/"]
DOCKERFILE

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
if [ "$PUSH" = "true" ]; then
    log_info "Pushing to registry..."
    "${CONTAINER_TOOL}" push "$FULL_IMAGE"
    "${CONTAINER_TOOL}" push "${REGISTRY}/${IMAGE_NAME}:latest"
    log_info "Pushed: $FULL_IMAGE"
fi

echo ""
log_info "=========================================="
log_info "pyzmq builder image ready: $FULL_IMAGE"
log_info "=========================================="
echo ""
echo "Usage in K8s init container:"
echo "  image: $FULL_IMAGE"
echo "  command: cp /wheels/pyzmq-*.whl /nvsnap-lib/"
echo ""
echo "Then in vLLM startup:"
echo "  pip install --force-reinstall --no-deps /nvsnap-lib/pyzmq-*.whl"
echo "  (LD_LIBRARY_PATH must include /nvsnap-lib/ where libzmq.so.5 lives)"
