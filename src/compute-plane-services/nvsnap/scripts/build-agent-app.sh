#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Build NVSNAP agent APP image (Go binaries + intercept library)
# This builds on top of the base image and is FAST (~30 seconds)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"

# Configuration
REGISTRY="${REGISTRY:-nvcr.io/0651155215864979/ncp-dev}"
BASE_IMAGE="${BASE_IMAGE:-${REGISTRY}/nvsnap-agent-base:v45}"
IMAGE_NAME="nvsnap-agent"
VERSION="${VERSION:-v0.7.119}"
CONTAINER_TOOL="${CONTAINER_TOOL:-}"
ENABLE_LIBUV_INTERCEPT="${ENABLE_LIBUV_INTERCEPT:-0}"

if [ -z "$CONTAINER_TOOL" ]; then
    if command -v nerdctl >/dev/null 2>&1; then
        CONTAINER_TOOL="nerdctl"
    elif command -v docker >/dev/null 2>&1; then
        CONTAINER_TOOL="docker"
    else
        echo "No container tool found (install docker or nerdctl)"
        exit 1
    fi
fi

if [ "$CONTAINER_TOOL" = "docker" ] && [ -z "${DOCKER_HOST:-}" ]; then
    if [ -S /var/run/docker.sock ]; then
        export DOCKER_HOST="unix:///var/run/docker.sock"
    fi
fi

echo "=== Building NVSNAP Agent APP Image ==="
echo "Base: ${BASE_IMAGE}"
echo "Version: ${VERSION}"
echo "Enable libuv intercept: ${ENABLE_LIBUV_INTERCEPT}"
echo ""

# Create build context from project root
BUILD_CONTEXT=$(mktemp -d)
trap "rm -rf $BUILD_CONTEXT" EXIT

echo "Build context: $BUILD_CONTEXT"

# Copy Dockerfile
cp "${PROJECT_ROOT}/docker/agent/Dockerfile.app" "${BUILD_CONTEXT}/Dockerfile"

# Copy Go project (exclude large/unnecessary directories)
echo "Copying Go project..."
rsync -a \
    --exclude='.git' \
    --exclude='vendor' \
    --exclude='bin' \
    --exclude='docker' \
    --exclude='placeholder' \
    --exclude='*.tar.gz' \
    --exclude='node_modules' \
    "${PROJECT_ROOT}/" "${BUILD_CONTEXT}/"

# Copy intercept library source
mkdir -p "${BUILD_CONTEXT}/lib"
cp -r "${PROJECT_ROOT}/lib/nvsnap_intercept" "${BUILD_CONTEXT}/lib/"

# Build image
echo ""
echo "Building Docker image..."
"${CONTAINER_TOOL}" build \
    --platform linux/amd64 \
    --build-arg BASE_IMAGE="${BASE_IMAGE}" \
    --build-arg ENABLE_LIBUV_INTERCEPT="${ENABLE_LIBUV_INTERCEPT}" \
    -t "${REGISTRY}/${IMAGE_NAME}:${VERSION}" \
    "${BUILD_CONTEXT}"

echo ""
echo "=== App image build complete ==="
echo "Image: ${REGISTRY}/${IMAGE_NAME}:${VERSION}"
echo ""

# Auto-push option
if [ "${PUSH:-}" = "1" ] || [ "${PUSH:-}" = "true" ]; then
    echo "Pushing..."
    "${CONTAINER_TOOL}" push "${REGISTRY}/${IMAGE_NAME}:${VERSION}"
    echo "Pushed: ${REGISTRY}/${IMAGE_NAME}:${VERSION}"
fi
