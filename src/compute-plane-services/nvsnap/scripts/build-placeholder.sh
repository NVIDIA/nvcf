#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Build a placeholder image for a given base image
# Usage: ./build-placeholder.sh <base-image> [version]
# Example: ./build-placeholder.sh pytorch/pytorch:2.7.0-cuda12.8-cudnn9-runtime 0.2.0

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"
source "$(dirname "${BASH_SOURCE[0]}")/_deps.sh"
nvsnap_resolve_sibling CRIU_SOURCE criu
REGISTRY="nvcr.io/0651155215864979/ncp-dev"

BASE_IMAGE="${1:?Usage: $0 <base-image> [version]}"
VERSION="${2:-latest}"

# Generate tag from base image name
TAG_NAME=$(echo "$BASE_IMAGE" | sed 's/[^a-zA-Z0-9]/-/g' | tr '[:upper:]' '[:lower:]')
FULL_TAG="${REGISTRY}/nvsnap-placeholder:${TAG_NAME}-${VERSION}"

echo "=== Building Placeholder Image ==="
echo "Base image: ${BASE_IMAGE}"
echo "Output:     ${FULL_TAG}"
echo ""

# Setup build context
BUILD_DIR=$(mktemp -d)
trap "rm -rf ${BUILD_DIR}" EXIT

# Copy CRIU source
echo "Copying CRIU source..."
rsync -a --exclude='.git' --exclude='*.o' --exclude='*.d' --exclude='criu/criu' \
    "${CRIU_SOURCE}/" "${BUILD_DIR}/criu-source/"

# Copy restore-entrypoint
cp "${PROJECT_ROOT}/bin/restore-entrypoint" "${BUILD_DIR}/"

# Copy Dockerfile
cp "${PROJECT_ROOT}/placeholder/Dockerfile" "${BUILD_DIR}/"

# Update Dockerfile to use provided base image
sed -i "s|^ARG BASE_IMAGE=.*|ARG BASE_IMAGE=${BASE_IMAGE}|" "${BUILD_DIR}/Dockerfile"

# Build
echo ""
echo "Building image..."
docker build \
    --build-arg BASE_IMAGE="${BASE_IMAGE}" \
    -t "${FULL_TAG}" \
    "${BUILD_DIR}" 2>&1 | tail -15

# Push
echo ""
echo "Pushing to registry..."
docker push "${FULL_TAG}" 2>&1 | tail -5

echo ""
echo "=== Done ==="
echo "Image: ${FULL_TAG}"
echo ""
echo "Use in restore pod:"
echo "  image: ${FULL_TAG}"
