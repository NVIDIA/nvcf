#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Build portable CRIU binary that works in most containers
# Uses Debian Bullseye (glibc 2.31) for maximum compatibility

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"
source "$(dirname "${BASH_SOURCE[0]}")/_deps.sh"
nvsnap_resolve_sibling CRIU_SOURCE criu
OUTPUT_DIR="${PROJECT_ROOT}/bin"

echo "=== Building Portable CRIU ==="
echo "Source: ${CRIU_SOURCE}"
echo "Output: ${OUTPUT_DIR}"

# Create Dockerfile inline
DOCKER_CONTEXT=$(mktemp -d)
trap "rm -rf ${DOCKER_CONTEXT}" EXIT

# Copy CRIU source (excluding build artifacts)
echo "Copying CRIU source..."
rsync -a --exclude='.git' --exclude='*.o' --exclude='*.d' --exclude='criu/criu' \
    "${CRIU_SOURCE}/" "${DOCKER_CONTEXT}/criu/"

# Create Dockerfile
cat > "${DOCKER_CONTEXT}/Dockerfile" << 'DOCKERFILE'
# Ubuntu 22.04 = glibc 2.35 (works on most modern containers)
FROM ubuntu:22.04

ENV DEBIAN_FRONTEND=noninteractive

# Install build dependencies
RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential \
    pkg-config \
    libbsd-dev \
    libcap-dev \
    libnet1-dev \
    libnl-3-dev \
    libnl-route-3-dev \
    libprotobuf-dev \
    libprotobuf-c-dev \
    protobuf-c-compiler \
    protobuf-compiler \
    python3-protobuf \
    libgnutls28-dev \
    libnftables-dev \
    libaio-dev \
    uuid-dev \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /build
COPY criu/ /build/criu/

# Patch out unsupported compiler flags for older GCC
RUN cd criu && \
    sed -i 's/-Wno-unknown-warning-option//g' Makefile.config 2>/dev/null || true && \
    sed -i 's/-Wno-dangling-pointer//g' Makefile.config 2>/dev/null || true && \
    sed -i 's/-Wno-unknown-warning-option//g' criu/Makefile 2>/dev/null || true && \
    sed -i 's/-Wno-dangling-pointer//g' criu/Makefile 2>/dev/null || true && \
    find . -name "*.mk" -exec sed -i 's/-Wno-unknown-warning-option//g' {} \; 2>/dev/null || true && \
    find . -name "*.mk" -exec sed -i 's/-Wno-dangling-pointer//g' {} \; 2>/dev/null || true

# Build
RUN cd criu && \
    make clean 2>/dev/null || true && \
    make WERROR=0 -j$(nproc) && \
    strip criu/criu

# Verify
RUN /build/criu/criu/criu --version

# Collect all required libraries
RUN mkdir -p /criu-bundle/lib && \
    cp /build/criu/criu/criu /criu-bundle/criu && \
    for lib in $(ldd /build/criu/criu/criu | grep "=> /" | awk '{print $3}'); do \
        cp -L "$lib" /criu-bundle/lib/ 2>/dev/null || true; \
    done && \
    cp -L /lib64/ld-linux-x86-64.so.2 /criu-bundle/lib/ 2>/dev/null || true

# Create wrapper script
RUN echo '#!/bin/sh' > /criu-bundle/criu-wrapper && \
    echo 'SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"' >> /criu-bundle/criu-wrapper && \
    echo 'export LD_LIBRARY_PATH="$SCRIPT_DIR/lib:$LD_LIBRARY_PATH"' >> /criu-bundle/criu-wrapper && \
    echo 'exec "$SCRIPT_DIR/criu" "$@"' >> /criu-bundle/criu-wrapper && \
    chmod +x /criu-bundle/criu-wrapper

RUN ls -la /criu-bundle/ /criu-bundle/lib/
DOCKERFILE

# Build
echo ""
echo "Building CRIU in Docker..."
docker build -t criu-portable-builder:latest "${DOCKER_CONTEXT}"

# Extract bundle
echo ""
echo "Extracting CRIU bundle..."
mkdir -p "${OUTPUT_DIR}"
rm -rf "${OUTPUT_DIR}/criu-bundle"

# Create container and copy bundle
CONTAINER_ID=$(docker create criu-portable-builder:latest)
docker cp "${CONTAINER_ID}:/criu-bundle" "${OUTPUT_DIR}/criu-bundle"
docker rm "${CONTAINER_ID}"

# Create tarball for easy deployment
cd "${OUTPUT_DIR}"
tar czf criu-bundle.tar.gz criu-bundle/

# Verify
echo ""
echo "=== Build Complete ==="
ls -lh "${OUTPUT_DIR}/criu-bundle/"
ls -lh "${OUTPUT_DIR}/criu-bundle/lib/" | head -10
ls -lh "${OUTPUT_DIR}/criu-bundle.tar.gz"

echo ""
echo "Bundle: ${OUTPUT_DIR}/criu-bundle.tar.gz"
echo ""
echo "Deploy to nodes:"
echo "  scp ${OUTPUT_DIR}/criu-bundle.tar.gz <node>:/tmp/"
echo "  ssh <node> 'cd /usr/local && sudo tar xzf /tmp/criu-bundle.tar.gz'"
echo "  # Then use /usr/local/criu-bundle/criu-wrapper as CRIU"
