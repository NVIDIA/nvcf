#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -euo pipefail

COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BRANCH=$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo "unknown")
DIRTY=""
if [ "$COMMIT" != "unknown" ] && [ -n "$(git status --porcelain 2>/dev/null)" ]; then
    DIRTY="-dirty"
fi

# CI overrides for VERSION (from git tag or MR sha) and BUILD_USER.
# When unset, use only tags that belong to this service. `git describe
# --exact-match` is unsafe in the monorepo because a commit can also have
# stack/chart tags such as deploy/stacks/nvcf-compute-plane/v...
TAG_PREFIX="src/control-plane-services/function-autoscaler/v"
LEGACY_TAG_PREFIX="nvcf-function-autoscaler-v"

version_from_exact_tag() {
    local prefix tag
    for prefix in "$@"; do
        tag=$(git tag --points-at HEAD --list "${prefix}*" 2>/dev/null | sort -V | tail -n1 || true)
        if [ -n "${tag}" ]; then
            printf '%s\n' "${tag#"${prefix}"}"
            return 0
        fi
    done
    return 1
}

if [ -n "${NVCF_VERSION:-}" ]; then
    VERSION="${NVCF_VERSION}"
elif VERSION=$(version_from_exact_tag "${TAG_PREFIX}" "${LEGACY_TAG_PREFIX}"); then
    :
else
    VERSION="mr-${COMMIT}"
fi

BUILD_USER="${NVCF_BUILD_USER:-$(whoami 2>/dev/null || echo 'unknown')}"
BUILD_DATE=$(date -u +"%Y-%m-%dT%H:%M:%SZ")

echo "STABLE_VERSION ${VERSION}"
echo "STABLE_GIT_COMMIT ${COMMIT}${DIRTY}"
echo "STABLE_GIT_BRANCH ${BRANCH}"
echo "STABLE_BUILD_USER ${BUILD_USER}"
echo "STABLE_OCI_TAG ${VERSION}-${COMMIT}${DIRTY}"

# Volatile keys (no STABLE_ prefix). Bazel injects them into stamped
# binaries the same way as STABLE_* keys, but their value changes do
# NOT invalidate the action cache. BUILD_DATE moves on every invocation;
# keeping it volatile is what lets `--stamp` reuse the cached link of
# the server binary instead of forcing a relink on every CI run.
echo "BUILD_DATE ${BUILD_DATE}"
