#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Build self-contained NVSNAP agent image for managed K8s (GKE, EKS, AKS, etc.)
#
# Multi-stage build that compiles everything from source:
# - CRIU: Cloned from GitHub fork (github.com/balajinvda/criu) at specific tag
# - Go binaries: nvsnap-agent, restore-entrypoint
#
# Only external dependency: cuda-checkpoint (NVIDIA proprietary binary)
#
# CRIU version is controlled by CRIU_TAG build arg in Dockerfile.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"

# Load config (if exists)
if [ -f "${SCRIPT_DIR}/config.sh" ]; then
    source "${SCRIPT_DIR}/config.sh"
fi

# Logging helpers
log_info() { echo "[INFO] $*"; }
log_warn() { echo "[WARN] $*" >&2; }
log_error() { echo "[ERROR] $*" >&2; }

# Configuration
REGISTRY="${REGISTRY:-nvcr.io/0651155215864979/ncp-dev}"
IMAGE_NAME="nvsnap-agent"
VERSION="${VERSION:-v0.4.5}"
DOCKER_DIR="${PROJECT_ROOT}/docker/agent"
CRIU_TAG="${CRIU_TAG:-nvsnap-v0.3.0}"
source "$(dirname "${BASH_SOURCE[0]}")/_deps.sh"
nvsnap_resolve_sibling CRIU_LOCAL_PATH criu
CRIU_LOCAL_DIR="criu-src-local"
CONTAINER_TOOL="${CONTAINER_TOOL:-}"
ENABLE_LIBUV_INTERCEPT="${ENABLE_LIBUV_INTERCEPT:-0}"

if [ -z "$CONTAINER_TOOL" ]; then
    if command -v nerdctl >/dev/null 2>&1; then
        CONTAINER_TOOL="nerdctl"
    elif command -v docker >/dev/null 2>&1; then
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

log_info "Building NVSNAP agent image"
log_info "Version: ${VERSION}"
log_info "Registry: ${REGISTRY}"
log_info "CRIU tag: ${CRIU_TAG}"
log_info "Enable libuv intercept: ${ENABLE_LIBUV_INTERCEPT}"

# Step 1: Prepare build context
log_info "Step 1: Preparing build context..."

BUILD_CONTEXT=$(mktemp -d)
trap "rm -rf $BUILD_CONTEXT" EXIT

# Copy Dockerfile
cp "${DOCKER_DIR}/Dockerfile" "${BUILD_CONTEXT}/"

# Copy local CRIU fork (preferred) into build context
mkdir -p "${BUILD_CONTEXT}/${CRIU_LOCAL_DIR}"
if [ -d "${CRIU_LOCAL_PATH}" ]; then
    log_info "  Using local CRIU from ${CRIU_LOCAL_PATH}"
    rsync -a --exclude='.git' "${CRIU_LOCAL_PATH}/" "${BUILD_CONTEXT}/${CRIU_LOCAL_DIR}/"
else
    log_warn "  Local CRIU not found at ${CRIU_LOCAL_PATH} (will clone from repo)"
fi

# Copy Go project (for go-builder stage)
log_info "  Copying Go project..."
rsync -a \
    --exclude='.git' \
    --exclude='docker' \
    --exclude='bin' \
    --exclude='placeholder' \
    --exclude='vendor' \
    --exclude='*.tar.gz' \
    "$PROJECT_ROOT/" "${BUILD_CONTEXT}/"

# Step 2: Find cuda-checkpoint
log_info "Step 2: Locating cuda-checkpoint..."
CUDA_CHECKPOINT=""
for path in \
    "${PROJECT_ROOT}/bin/cuda-checkpoint" \
    "${PROJECT_ROOT}/bin/criu-bundle/cuda-checkpoint" \
    "${PROJECT_ROOT}/../cuda-checkpoint/bin/x86_64_Linux/cuda-checkpoint" \
    "/usr/local/bin/cuda-checkpoint"; do
    if [ -f "$path" ]; then
        CUDA_CHECKPOINT="$path"
        break
    fi
done

if [ -n "$CUDA_CHECKPOINT" ]; then
    log_info "  Found: $CUDA_CHECKPOINT"
    cp "$CUDA_CHECKPOINT" "${BUILD_CONTEXT}/cuda-checkpoint"
else
    log_warn "  cuda-checkpoint not found, creating stub (GPU checkpoint will not work)"
    echo '#!/bin/sh' > "${BUILD_CONTEXT}/cuda-checkpoint"
    echo 'echo "cuda-checkpoint stub - please install real binary"; exit 1' >> "${BUILD_CONTEXT}/cuda-checkpoint"
fi
chmod +x "${BUILD_CONTEXT}/cuda-checkpoint"

# Wrapper that consumes $NVSNAP_CUDA_LIB_DIR (runtime-discovered by the agent)
# and a fallback list of known driver layouts. Dockerfile.local COPYs this
# into /criu-bundle/cuda-checkpoint — the path CRIU resolves via PATH.
cp "${DOCKER_DIR}/cuda-checkpoint-wrapper.sh" "${BUILD_CONTEXT}/cuda-checkpoint-wrapper.sh"

# Step 3: Build Docker image
log_info "Step 3: Building Docker image..."
log_info "  CRIU will be cloned from GitHub at tag: ${CRIU_TAG}"
FULL_IMAGE="${REGISTRY}/${IMAGE_NAME}:${VERSION}"

cd "$BUILD_CONTEXT"

"${CONTAINER_TOOL}" build \
    --build-arg CRIU_LOCAL_DIR="${CRIU_LOCAL_DIR}" \
    --build-arg CRIU_TAG="${CRIU_TAG}" \
    --build-arg ENABLE_LIBUV_INTERCEPT="${ENABLE_LIBUV_INTERCEPT}" \
    --build-arg VERSION="${VERSION}" \
    --build-arg BUILD_DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    -t "$FULL_IMAGE" \
    -t "${REGISTRY}/${IMAGE_NAME}:latest" \
    .

log_info "Built: $FULL_IMAGE"

# Step 4: Push to registry
if [ "${PUSH:-true}" = "true" ]; then
    log_info "Step 4: Pushing to registry..."
    "${CONTAINER_TOOL}" push "$FULL_IMAGE"
    "${CONTAINER_TOOL}" push "${REGISTRY}/${IMAGE_NAME}:latest"
    log_info "Pushed: $FULL_IMAGE"
fi

echo ""
log_info "=========================================="
log_info "Agent image ready: $FULL_IMAGE"
log_info "CRIU version: ${CRIU_TAG}"
log_info "=========================================="
echo ""
echo "Deploy with:"
echo "  kubectl set image daemonset/nvsnap-agent agent=$FULL_IMAGE"
echo ""
echo "Or update deploy/k8s/agent-daemonset.yaml and apply"
