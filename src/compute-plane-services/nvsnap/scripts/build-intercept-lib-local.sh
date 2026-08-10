#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT_DIR="${1:-/tmp/nvsnap-local/nvsnap-lib}"
DOCKER_HOST_DEFAULT="unix:///var/run/docker.sock"
export DOCKER_HOST="${NVSNAP_DOCKER_HOST:-$DOCKER_HOST_DEFAULT}"
BUILD_IMAGE="${NVSNAP_INTERCEPT_BUILD_IMAGE:-nvsnap-intercept-build:local}"
ENABLE_LIBUV_INTERCEPT="${NVSNAP_ENABLE_LIBUV_INTERCEPT:-0}"

mkdir -p "${OUT_DIR}"

if ! docker image inspect "${BUILD_IMAGE}" >/dev/null 2>&1; then
  echo "Building intercept toolchain image ${BUILD_IMAGE}..."
  docker build -f "${REPO_ROOT}/scripts/Dockerfile.intercept-build" -t "${BUILD_IMAGE}" "${REPO_ROOT}/scripts"
fi

echo "Building libnvsnap_intercept.so in ${BUILD_IMAGE}..."
docker run --rm \
  -v "${REPO_ROOT}/lib/nvsnap_intercept:/src" \
  -v "${OUT_DIR}:/out" \
  "${BUILD_IMAGE}" bash -lc \
  "make -C /src clean && make -C /src ENABLE_LIBUV_INTERCEPT=${ENABLE_LIBUV_INTERCEPT} && cp /src/libnvsnap_intercept.so /out/"

echo "Built: ${OUT_DIR}/libnvsnap_intercept.so"
