#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Build + push the non-agent nvsnap images (nvsnap-server, nvsnap-blobstore).
#
# These are pure-Go binaries (UI for server, none for blobstore) and
# build in seconds — no CRIU base layer needed. Versions read from
# scripts/versions.sh.
#
# Usage:
#   ./scripts/build-nvsnap-images.sh build       # build both, no push
#   ./scripts/build-nvsnap-images.sh push        # build + push both
#   ./scripts/build-nvsnap-images.sh server      # build nvsnap-server only
#   ./scripts/build-nvsnap-images.sh blobstore   # build nvsnap-blobstore only

set -euo pipefail
unset DOCKER_HOST 2>/dev/null || true

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"
source "${SCRIPT_DIR}/versions.sh"

REGISTRY="${REGISTRY:-$NVSNAP_REGISTRY}"
SERVER_VERSION="${NVSNAP_SERVER_VERSION:-v0.5.0-phase5d}"
BLOBSTORE_VERSION="${NVSNAP_BLOBSTORE_VERSION:-v0.1.0}"

SERVER_IMAGE="${REGISTRY}/nvsnap-server:${SERVER_VERSION}"
BLOBSTORE_IMAGE="${REGISTRY}/nvsnap-blobstore:${BLOBSTORE_VERSION}"

build_server() {
    echo "==> Building ${SERVER_IMAGE}"
    cd "$PROJECT_ROOT"
    docker build \
        -f docker/Dockerfile.server \
        --build-arg VERSION="${SERVER_VERSION}" \
        -t "${SERVER_IMAGE}" \
        .
    echo "    OK: ${SERVER_IMAGE}"
}

push_server() {
    echo "==> Pushing ${SERVER_IMAGE}"
    docker push "${SERVER_IMAGE}"
}

build_blobstore() {
    echo "==> Building ${BLOBSTORE_IMAGE}"
    cd "$PROJECT_ROOT"
    docker build \
        -f docker/nvsnap-blobstore/Dockerfile \
        --build-arg VERSION="${BLOBSTORE_VERSION}" \
        -t "${BLOBSTORE_IMAGE}" \
        .
    echo "    OK: ${BLOBSTORE_IMAGE}"
}

push_blobstore() {
    echo "==> Pushing ${BLOBSTORE_IMAGE}"
    docker push "${BLOBSTORE_IMAGE}"
}

case "${1:-build}" in
    build)
        build_server
        build_blobstore
        ;;
    push)
        build_server
        build_blobstore
        push_server
        push_blobstore
        ;;
    server)
        build_server
        ;;
    blobstore)
        build_blobstore
        ;;
    push-server)
        build_server
        push_server
        ;;
    push-blobstore)
        build_blobstore
        push_blobstore
        ;;
    *)
        echo "Usage: $0 [build|push|server|blobstore|push-server|push-blobstore]" >&2
        exit 1
        ;;
esac
