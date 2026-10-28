#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Build NVSNAP agent with LOCAL criu-src (for io_uring fix testing)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"

# Configuration
REGISTRY="${REGISTRY:-nvcr.io/0651155215864979/ncp-dev}"
IMAGE_NAME="nvsnap-agent"
VERSION="${VERSION:-v0.4.18}"

echo "=== Building NVSNAP agent with LOCAL CRIU (io_uring fixes) ==="
echo "Version: ${VERSION}"
echo "Registry: ${REGISTRY}"

# Create build context
BUILD_CONTEXT=$(mktemp -d)
trap "rm -rf $BUILD_CONTEXT" EXIT

echo "Build context: $BUILD_CONTEXT"

# Copy Dockerfile.local
cp "${PROJECT_ROOT}/docker/agent/Dockerfile.local" "${BUILD_CONTEXT}/Dockerfile"

# Copy LOCAL criu-src with io_uring fixes
echo "Copying local criu-src..."
cp -r "${PROJECT_ROOT}/docker/phase2/criu-src" "${BUILD_CONTEXT}/criu-src"

# Copy Go project
echo "Copying Go project..."
rsync -a \
    --exclude='.git' \
    --exclude='vendor' \
    --exclude='bin' \
    --exclude='docker' \
    --exclude='placeholder' \
    --exclude='*.tar.gz' \
    "${PROJECT_ROOT}/" "${BUILD_CONTEXT}/"

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
echo "Building Docker image..."
docker build \
    --platform linux/amd64 \
    -t "${REGISTRY}/${IMAGE_NAME}:${VERSION}" \
    "${BUILD_CONTEXT}"

echo ""
echo "=== Build complete ==="
echo "Image: ${REGISTRY}/${IMAGE_NAME}:${VERSION}"

# Push
read -p "Push to registry? (y/n) " -n 1 -r
echo
if [[ $REPLY =~ ^[Yy]$ ]]; then
    echo "Pushing..."
    docker push "${REGISTRY}/${IMAGE_NAME}:${VERSION}"
    echo "Pushed: ${REGISTRY}/${IMAGE_NAME}:${VERSION}"
fi
