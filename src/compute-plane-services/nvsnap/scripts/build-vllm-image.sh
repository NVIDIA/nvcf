#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Build custom vLLM image with patched uvloop + libzmq baked in
#
# This eliminates the need for:
# - get-libzmq init container (libzmq baked into image)
# - C-level uvloop hacks in libnvsnap_intercept.so (uvloop handles it natively)
#
# The resulting image is a drop-in replacement for vllm/vllm-openai:VERSION

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"

# Logging
log_info() { echo "[INFO] $*"; }
log_error() { echo "[ERROR] $*" >&2; }

# Configuration
REGISTRY="${REGISTRY:-nvcr.io/0651155215864979/ncp-dev}"
IMAGE_NAME="vllm-openai"
VLLM_BASE="${VLLM_BASE:-vllm/vllm-openai:v0.11.2}"
UVLOOP_BUILDER="${UVLOOP_BUILDER:-${REGISTRY}/uvloop-builder:v0.22.1-gpucr2}"
LIBZMQ_BUILDER="${LIBZMQ_BUILDER:-${REGISTRY}/libzmq-builder:v0.8.0-zmq}"
VERSION="${VERSION:-v0.11.2-gpucr1}"
CONTAINER_TOOL="${CONTAINER_TOOL:-docker}"

log_info "Building custom vLLM image"
log_info "Base: $VLLM_BASE"
log_info "uvloop builder: $UVLOOP_BUILDER"
log_info "libzmq builder: $LIBZMQ_BUILDER"
log_info "Version: $VERSION"

# Create build context
BUILD_CONTEXT=$(mktemp -d)
trap "rm -rf $BUILD_CONTEXT" EXIT

# Create Dockerfile
cat > "$BUILD_CONTEXT/Dockerfile" <<DOCKERFILE
# Stage 1: Get patched uvloop wheel
FROM ${UVLOOP_BUILDER} AS uvloop

# Stage 2: Get patched libzmq
FROM ${LIBZMQ_BUILDER} AS libzmq

# Stage 3: Build final vLLM image
FROM ${VLLM_BASE}

# Install patched uvloop (replaces stock version)
COPY --from=uvloop /wheels/ /tmp/wheels/
RUN pip install --force-reinstall --no-deps /tmp/wheels/uvloop-*.whl && rm -rf /tmp/wheels/

# Install patched libzmq (replaces stock version)
COPY --from=libzmq /usr/local/lib/libzmq.so* /usr/local/lib/
RUN ldconfig

# Verify patches
RUN python3 -c "import uvloop; print('uvloop:', uvloop.__version__)" && \\
    python3 -c "import ctypes; zmq = ctypes.CDLL('libzmq.so.5'); print('libzmq loaded')" && \\
    echo "All patches verified"
DOCKERFILE

# Build image
log_info "Building Docker image..."
FULL_IMAGE="${REGISTRY}/${IMAGE_NAME}:${VERSION}"

cd "$BUILD_CONTEXT"

"${CONTAINER_TOOL}" build \
    -t "$FULL_IMAGE" \
    .

log_info "Built: $FULL_IMAGE"

# Push to registry
if [ "${PUSH:-true}" = "true" ]; then
    log_info "Pushing to registry..."
    "${CONTAINER_TOOL}" push "$FULL_IMAGE"
    log_info "Pushed: $FULL_IMAGE"
fi

echo ""
log_info "=========================================="
log_info "Custom vLLM image ready: $FULL_IMAGE"
log_info "=========================================="
echo ""
echo "This image includes:"
echo "  - vLLM from $VLLM_BASE"
echo "  - Patched uvloop with CRIU restore support (uv_loop_fork)"
echo "  - Patched libzmq with checkpoint/restore APIs"
echo ""
echo "Usage in K8s manifests:"
echo "  image: $FULL_IMAGE"
echo "  (No get-libzmq init container needed)"
echo ""
