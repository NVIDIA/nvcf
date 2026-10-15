#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Build NVSNAP agent BASE image (CRIU + system deps)
# This should be run rarely - only when CRIU or system deps change
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"

# Configuration
REGISTRY="${REGISTRY:-nvcr.io/0651155215864979/ncp-dev}"
IMAGE_NAME="nvsnap-agent-base"
VERSION="${VERSION:-v45}"

echo "=== Building NVSNAP Agent BASE Image ==="
echo "This builds CRIU and system dependencies (takes ~5 minutes)"
echo "Version: ${VERSION}"
echo "Registry: ${REGISTRY}"
echo ""

# Create build context
BUILD_CONTEXT=$(mktemp -d)
trap "rm -rf $BUILD_CONTEXT" EXIT

echo "Build context: $BUILD_CONTEXT"

# Copy Dockerfile
cp "${PROJECT_ROOT}/docker/agent/Dockerfile.base" "${BUILD_CONTEXT}/Dockerfile"

# Copy CRIU source with io_uring fixes
echo "Copying criu-src (with io_uring fixes)..."
# Clean any stale build artifacts first
if [ -d "${PROJECT_ROOT}/docker/phase2/criu-src/criu" ]; then
    (cd "${PROJECT_ROOT}/docker/phase2/criu-src/criu" && make clean 2>/dev/null || true)
fi
cp -r "${PROJECT_ROOT}/docker/phase2/criu-src" "${BUILD_CONTEXT}/criu-src"

# Copy cuda-checkpoint
if [ -f "${PROJECT_ROOT}/docker/agent/cuda-checkpoint" ]; then
    cp "${PROJECT_ROOT}/docker/agent/cuda-checkpoint" "${BUILD_CONTEXT}/"
elif [ -f "${PROJECT_ROOT}/bin/criu-bundle/cuda-checkpoint" ]; then
    cp "${PROJECT_ROOT}/bin/criu-bundle/cuda-checkpoint" "${BUILD_CONTEXT}/"
else
    echo "ERROR: cuda-checkpoint not found!"
    exit 1
fi

# Build image
echo ""
echo "Building Docker image (this takes ~5 minutes for CRIU)..."
docker build \
    --no-cache \
    --platform linux/amd64 \
    -t "${REGISTRY}/${IMAGE_NAME}:${VERSION}" \
    "${BUILD_CONTEXT}"

echo ""
echo "=== Base image build complete ==="
echo "Image: ${REGISTRY}/${IMAGE_NAME}:${VERSION}"
echo ""
echo "To push: docker push ${REGISTRY}/${IMAGE_NAME}:${VERSION}"
