#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Build NVSNAP dependency images reproducibly
#
# Usage:
#   ./scripts/build-deps.sh libzmq     # Build libzmq builder image
#   ./scripts/build-deps.sh uvloop     # Build uvloop wheel image
#   ./scripts/build-deps.sh pyzmq      # Build pyzmq wheel image
#   ./scripts/build-deps.sh all        # Build all
#   ./scripts/build-deps.sh push       # Push all to registry
#
# Environment:
#   REGISTRY        - Image registry (default: from versions.sh)
#   PUSH=1          - Push after build
#   NO_CACHE=1      - Force rebuild without cache

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"

source "${SCRIPT_DIR}/versions.sh"

REGISTRY="${REGISTRY:-$NVSNAP_REGISTRY}"

# Source repo locations (auto-discovered from sibling layout — see CONTRIBUTING.md)
source "$(dirname "${BASH_SOURCE[0]}")/_deps.sh"
nvsnap_resolve_sibling LIBZMQ_SRC libzmq
nvsnap_resolve_sibling UVLOOP_SRC uvloop
nvsnap_resolve_sibling PYZMQ_SRC pyzmq
nvsnap_resolve_sibling LIBUV_SRC libuv

# Auto-detect DOCKER_HOST
if [ -z "${DOCKER_HOST:-}" ]; then
    if [ -S /var/run/docker.sock ]; then
        export DOCKER_HOST="unix:///var/run/docker.sock"
    fi
fi

CACHE_FLAG=""
if [ "${NO_CACHE:-0}" = "1" ]; then
    CACHE_FLAG="--no-cache"
fi

usage() {
    echo "Usage: $0 [libzmq|uvloop|pyzmq|libuv|all|push]"
    echo ""
    echo "Builds dependency images from version-controlled Dockerfiles."
    echo "Source repos must be cloned locally (see environment variables)."
    echo ""
    echo "Current versions:"
    echo "  libzmq: ${NVSNAP_LIBZMQ_VERSION}"
    echo "  uvloop: ${NVSNAP_UVLOOP_VERSION}"
    echo "  pyzmq:  ${NVSNAP_PYZMQ_VERSION}"
    echo "  libuv:  ${NVSNAP_LIBUV_VERSION}"
}

build_libzmq() {
    local IMAGE="${REGISTRY}/libzmq-builder:${NVSNAP_LIBZMQ_VERSION}"
    echo "=== Building libzmq: ${IMAGE} ==="

    if [ ! -d "$LIBZMQ_SRC/src" ]; then
        echo "ERROR: libzmq source not found at $LIBZMQ_SRC"
        echo "Clone: git clone <libzmq-fork> $LIBZMQ_SRC"
        exit 1
    fi

    # Create clean build context (exclude .git, build artifacts)
    local BUILD_CTX
    BUILD_CTX=$(mktemp -d)
    trap "rm -rf $BUILD_CTX" RETURN

    rsync -a \
        --exclude='.git' \
        --exclude='build/' \
        --exclude='*.o' \
        --exclude='*.so' \
        --exclude='*.so.*' \
        "$LIBZMQ_SRC/" "$BUILD_CTX/"

    docker build \
        $CACHE_FLAG \
        --platform linux/amd64 \
        -t "$IMAGE" \
        -f "${PROJECT_ROOT}/docker/libzmq/Dockerfile" \
        "$BUILD_CTX"

    echo ""
    echo "Built: $IMAGE"

    if [ "${PUSH:-0}" = "1" ]; then
        echo "Pushing $IMAGE..."
        docker push "$IMAGE"
    fi
}

build_uvloop() {
    local IMAGE="${REGISTRY}/uvloop-builder:${NVSNAP_UVLOOP_VERSION}"
    echo "=== Building uvloop: ${IMAGE} ==="

    if [ ! -f "$UVLOOP_SRC/pyproject.toml" ]; then
        echo "ERROR: uvloop source not found at $UVLOOP_SRC"
        echo "Clone: git clone <uvloop-fork> $UVLOOP_SRC"
        exit 1
    fi

    local BUILD_CTX
    BUILD_CTX=$(mktemp -d)
    trap "rm -rf $BUILD_CTX" RETURN

    rsync -a \
        --exclude='.git' \
        --exclude='build/' \
        --exclude='dist/' \
        --exclude='*.egg-info' \
        --exclude='.eggs/' \
        --exclude='*.o' \
        --exclude='*.so' \
        --exclude='uvloop/loop.c' \
        "$UVLOOP_SRC/" "$BUILD_CTX/"

    docker build \
        $CACHE_FLAG \
        --platform linux/amd64 \
        -t "$IMAGE" \
        -f "${PROJECT_ROOT}/docker/uvloop/Dockerfile" \
        "$BUILD_CTX"

    echo ""
    echo "Built: $IMAGE"

    if [ "${PUSH:-0}" = "1" ]; then
        echo "Pushing $IMAGE..."
        docker push "$IMAGE"
    fi
}

build_pyzmq() {
    local IMAGE="${REGISTRY}/pyzmq-builder:${NVSNAP_PYZMQ_VERSION}"
    echo "=== Building pyzmq: ${IMAGE} ==="

    if [ ! -f "$PYZMQ_SRC/pyproject.toml" ]; then
        echo "ERROR: pyzmq source not found at $PYZMQ_SRC"
        echo "Clone: git clone <pyzmq-fork> $PYZMQ_SRC"
        exit 1
    fi
    if [ ! -d "$LIBZMQ_SRC/src" ]; then
        echo "ERROR: libzmq source not found at $LIBZMQ_SRC (needed to build pyzmq)"
        exit 1
    fi

    # pyzmq needs both pyzmq-src and libzmq-src in the build context
    local BUILD_CTX
    BUILD_CTX=$(mktemp -d)
    trap "rm -rf $BUILD_CTX" RETURN

    rsync -a \
        --exclude='.git' \
        --exclude='build/' \
        --exclude='dist/' \
        --exclude='*.egg-info' \
        --exclude='*.o' \
        --exclude='*.so' \
        --exclude='*.so.*' \
        "$PYZMQ_SRC/" "$BUILD_CTX/pyzmq-src/"

    rsync -a \
        --exclude='.git' \
        --exclude='build/' \
        --exclude='*.o' \
        --exclude='*.so' \
        --exclude='*.so.*' \
        "$LIBZMQ_SRC/" "$BUILD_CTX/libzmq-src/"

    docker build \
        $CACHE_FLAG \
        --platform linux/amd64 \
        -t "$IMAGE" \
        -f "${PROJECT_ROOT}/docker/pyzmq/Dockerfile" \
        "$BUILD_CTX"

    echo ""
    echo "Built: $IMAGE"

    if [ "${PUSH:-0}" = "1" ]; then
        echo "Pushing $IMAGE..."
        docker push "$IMAGE"
    fi
}

build_libuv() {
    local IMAGE="${REGISTRY}/libuv-builder:${NVSNAP_LIBUV_VERSION}"
    echo "=== Building libuv: ${IMAGE} ==="

    if [ ! -f "$LIBUV_SRC/CMakeLists.txt" ]; then
        echo "ERROR: libuv source not found at $LIBUV_SRC"
        echo "Clone: git clone <libuv-fork> $LIBUV_SRC"
        exit 1
    fi

    local BUILD_CTX
    BUILD_CTX=$(mktemp -d)
    trap "rm -rf $BUILD_CTX" RETURN

    git -C "$LIBUV_SRC" archive HEAD | tar -x -C "$BUILD_CTX"

    docker build \
        $CACHE_FLAG \
        --platform linux/amd64 \
        -t "$IMAGE" \
        -f "${PROJECT_ROOT}/docker/libuv/Dockerfile" \
        "$BUILD_CTX"

    echo ""
    echo "Built: $IMAGE"

    if [ "${PUSH:-0}" = "1" ]; then
        echo "Pushing $IMAGE..."
        docker push "$IMAGE"
    fi
}

push_all() {
    echo "=== Pushing all dependency images ==="
    docker push "${REGISTRY}/libzmq-builder:${NVSNAP_LIBZMQ_VERSION}"
    docker push "${REGISTRY}/uvloop-builder:${NVSNAP_UVLOOP_VERSION}"
    docker push "${REGISTRY}/pyzmq-builder:${NVSNAP_PYZMQ_VERSION}"
    docker push "${REGISTRY}/libuv-builder:${NVSNAP_LIBUV_VERSION}"
    echo "Done."
}

case "${1:-help}" in
    libzmq)
        build_libzmq
        ;;
    uvloop)
        build_uvloop
        ;;
    pyzmq)
        build_pyzmq
        ;;
    libuv)
        build_libuv
        ;;
    all)
        build_libzmq
        build_uvloop
        build_pyzmq
        build_libuv
        ;;
    push)
        push_all
        ;;
    help|--help|-h)
        usage
        ;;
    *)
        echo "Unknown command: $1"
        usage
        exit 1
        ;;
esac
