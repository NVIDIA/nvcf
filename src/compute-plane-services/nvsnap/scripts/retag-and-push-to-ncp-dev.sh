#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# One-time bootstrap of the new NGC registry. Retags each of the 9 nvsnap
# images from stg.nvcr.io/zq9tgrjzrfpo/<name>:<oldver> to
# nvcr.io/0651155215864979/ncp-dev/<name>:v0.0.1 and pushes them.
#
# Run after the registry migration in commit history (versions.sh,
# Dockerfiles, manifests). Binary contents are unchanged from the
# source images — this is purely a tag+push.
#
# Re-runnable: docker tag is idempotent; docker push skips layers that
# already exist on the destination registry.

set -euo pipefail
unset DOCKER_HOST 2>/dev/null || true

OLD_REG="stg.nvcr.io/zq9tgrjzrfpo"
NEW_REG="nvcr.io/0651155215864979/ncp-dev"
NEW_TAG="v0.0.1"

# <name>:<old-tag> pairs
declare -a IMAGES=(
    "nvsnap-agent-base:v1.58-criu-self-loader"
    "nvsnap-agent:v0.24.16-ensure-capture-endpoint"
    "nvsnap-server:v0.9.0-cross-node-restore"
    "nvsnap-blobstore:v0.2.0-stats-captures"
    "nvsnap-init:v0.24.16-ensure-capture-endpoint"
    "uvloop-builder:v0.22.1-multipy1"
    "libuv-builder:v1.48.0-criu-v3"
    "libzmq-builder:v4.3.6-criu-epoll-v12"
    "pyzmq-builder:v27.2.0-gpucr3"
)

for entry in "${IMAGES[@]}"; do
    name="${entry%%:*}"
    src="${OLD_REG}/${entry}"
    dst="${NEW_REG}/${name}:${NEW_TAG}"
    echo "=== ${name} ==="
    echo "  src: ${src}"
    echo "  dst: ${dst}"
    docker tag "${src}" "${dst}"
    docker push "${dst}"
    echo ""
done

echo "All 9 images pushed to ${NEW_REG}/*:${NEW_TAG}"
