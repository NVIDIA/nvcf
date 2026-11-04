#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# NVSNAP Container Build and Push Script
#
# Usage:
#   ./scripts/build-container.sh [version]
#
# Examples:
#   ./scripts/build-container.sh          # Auto-increment patch version
#   ./scripts/build-container.sh 0.1.0    # Use specific version
#
# Environment variables:
#   NVSNAP_REGISTRY - Container registry (default: nvcr.io/0651155215864979/ncp-dev)
#   NVSNAP_SKIP_PUSH - Set to 1 to skip pushing to registry
#   DOCKER_CONFIG - Docker config directory (default: ~/.docker)

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"
DOCKER_DIR="$PROJECT_ROOT/docker/phase2"

# Configuration
REGISTRY="${NVSNAP_REGISTRY:-nvcr.io/0651155215864979/ncp-dev}"
IMAGE_NAME="nvsnap"
VERSION_FILE="$PROJECT_ROOT/.container-version"
CONTAINER_TOOL="${CONTAINER_TOOL:-}"

if [ -z "$CONTAINER_TOOL" ]; then
    if command -v nerdctl &>/dev/null; then
        CONTAINER_TOOL="nerdctl"
    elif command -v docker &>/dev/null; then
        CONTAINER_TOOL="docker"
    else
        log_error "No container tool found (install docker or nerdctl)"
        exit 1
    fi
fi

if [ "$CONTAINER_TOOL" = "docker" ] && [ -z "${DOCKER_HOST:-}" ]; then
    if [ -S /var/run/docker.sock ]; then
        export DOCKER_HOST="unix:///var/run/docker.sock"
    fi
fi

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

log_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# Get current version from file or default to 0.0.0
get_current_version() {
    if [ -f "$VERSION_FILE" ]; then
        cat "$VERSION_FILE"
    else
        echo "0.0.0"
    fi
}

# Increment patch version (0.0.1 -> 0.0.2)
increment_version() {
    local version=$1
    local major minor patch
    
    IFS='.' read -r major minor patch <<< "$version"
    patch=$((patch + 1))
    echo "${major}.${minor}.${patch}"
}

# Parse arguments
if [ -n "$1" ]; then
    VERSION="$1"
else
    CURRENT_VERSION=$(get_current_version)
    VERSION=$(increment_version "$CURRENT_VERSION")
    log_info "Auto-incrementing version: $CURRENT_VERSION -> $VERSION"
fi

# Validate version format
if ! [[ "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    log_error "Invalid version format: $VERSION (expected: X.Y.Z)"
    exit 1
fi

FULL_IMAGE="${REGISTRY}/${IMAGE_NAME}:${VERSION}"

log_info "================================================"
log_info "  NVSNAP Container Build"
log_info "================================================"
log_info "Registry: $REGISTRY"
log_info "Image:    $IMAGE_NAME"
log_info "Version:  $VERSION"
log_info "Full tag: $FULL_IMAGE"
log_info "================================================"

# Check prerequisites
log_info "Using container tool: $CONTAINER_TOOL"

# Copy forked CRIU source for in-container build. Sibling-discovered if
# unset; export CRIU_FORK_DIR to override.
if [ -z "${CRIU_FORK_DIR:-}" ]; then
    source "$(dirname "${BASH_SOURCE[0]}")/_deps.sh"
    nvsnap_resolve_sibling CRIU_FORK_DIR criu
fi
if [ -d "$CRIU_FORK_DIR" ]; then
    log_info "Copying forked CRIU source from: $CRIU_FORK_DIR"
    rm -rf "$DOCKER_DIR/criu-source"
    # Copy source files needed for build (exclude .git, object files, and dep files)
    mkdir -p "$DOCKER_DIR/criu-source"
    rsync -a \
        --exclude='.git' \
        --exclude='*.o' \
        --exclude='*.a' \
        --exclude='*.d' \
        --exclude='*.so' \
        --exclude='*.so.*' \
        --exclude='built-in.o' \
        --exclude='criu/criu' \
        --exclude='compel/compel' \
        "$CRIU_FORK_DIR/" "$DOCKER_DIR/criu-source/"
else
    log_error "Forked CRIU source not found at $CRIU_FORK_DIR"
    exit 1
fi

# Check for cuda-checkpoint binary.
# Prefer $CUDA_CHECKPOINT_BIN (explicit), then sibling cuda-checkpoint
# checkout at ../cuda-checkpoint/bin/x86_64_Linux/, then $PATH.
CUDA_CKPT_SRC=""
if [ -n "${CUDA_CHECKPOINT_BIN:-}" ] && [ -f "${CUDA_CHECKPOINT_BIN}" ]; then
    CUDA_CKPT_SRC="${CUDA_CHECKPOINT_BIN}"
elif [ -f "${PROJECT_ROOT}/../cuda-checkpoint/bin/x86_64_Linux/cuda-checkpoint" ]; then
    CUDA_CKPT_SRC="$(cd "${PROJECT_ROOT}/../cuda-checkpoint/bin/x86_64_Linux" && pwd)/cuda-checkpoint"
elif command -v cuda-checkpoint &>/dev/null; then
    CUDA_CKPT_SRC="$(command -v cuda-checkpoint)"
fi

if [ -n "$CUDA_CKPT_SRC" ]; then
    log_info "Copying cuda-checkpoint from: $CUDA_CKPT_SRC"
    cp "$CUDA_CKPT_SRC" "$DOCKER_DIR/cuda-checkpoint"
else
    log_warn "cuda-checkpoint binary not found - container will build without it"
    touch "$DOCKER_DIR/cuda-checkpoint"  # Create placeholder
fi

# Copy nvsnap-agent binary
AGENT_BIN="$PROJECT_ROOT/bin/nvsnap-agent"
if [ -f "$AGENT_BIN" ]; then
    log_info "Copying nvsnap-agent from: $AGENT_BIN"
    cp "$AGENT_BIN" "$DOCKER_DIR/nvsnap-agent"
else
    log_error "nvsnap-agent not found at $AGENT_BIN - run 'make agent' first"
    exit 1
fi

# Copy restore-entrypoint binary
RESTORE_BIN="$PROJECT_ROOT/bin/restore-entrypoint"
if [ -f "$RESTORE_BIN" ]; then
    log_info "Copying restore-entrypoint from: $RESTORE_BIN"
    cp "$RESTORE_BIN" "$DOCKER_DIR/restore-entrypoint"
fi

# Build the image
log_info "Building Docker image..."
cd "$DOCKER_DIR"

"${CONTAINER_TOOL}" build \
    --build-arg VERSION="$VERSION" \
    --build-arg BUILD_DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    --build-arg GIT_COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo 'unknown')" \
    -t "$FULL_IMAGE" \
    -t "${REGISTRY}/${IMAGE_NAME}:latest" \
    . 2>&1

if [ $? -ne 0 ]; then
    log_error "Docker build failed"
    exit 1
fi

log_info "Build successful: $FULL_IMAGE"

# Push to registry unless skipped
if [ "${NVSNAP_SKIP_PUSH:-0}" = "1" ]; then
    log_info "Skipping push (NVSNAP_SKIP_PUSH=1)"
else
    log_info "Pushing to registry..."
    
    # Check if we can authenticate
    if ! "${CONTAINER_TOOL}" push "$FULL_IMAGE" 2>&1; then
        log_warn "Push failed - you may need to authenticate:"
        log_warn "  docker login $REGISTRY"
        exit 1
    fi
    
    log_info "Push successful: $FULL_IMAGE"
fi

# Save version for next build
echo "$VERSION" > "$VERSION_FILE"
log_info "Version saved to $VERSION_FILE"

# Cleanup
rm -f "$DOCKER_DIR/cuda-checkpoint"
rm -f "$DOCKER_DIR/nvsnap-agent"
rm -f "$DOCKER_DIR/restore-entrypoint"
rm -rf "$DOCKER_DIR/criu-source"

log_info "================================================"
log_info "  Build Complete!"
log_info "================================================"
log_info ""
log_info "To run locally:"
log_info "  docker run --gpus all --privileged $FULL_IMAGE"
log_info ""
log_info "To pull on K8s nodes:"
log_info "  kubectl create secret docker-registry nvcr-secret \\"
log_info "    --docker-server=$REGISTRY \\"
log_info "    --docker-username='\$oauthtoken' \\"
log_info "    --docker-password='<NGC_API_KEY>'"
log_info ""
