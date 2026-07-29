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

# Tag prefixes that identify a release of this service, most current first.
# A monorepo release commit normally carries tags for several services and
# Helm stacks at once, so an unfiltered `git describe --exact-match` can
# return an unrelated tag and stamp it as this service's version. Match only
# tags this service owns, and strip the prefix so the stamped value is a bare
# semver rather than a full tag path.
SERVICE_TAG_PREFIXES="src/invocation-plane-services/http-invocation/v"

# Echoes the highest release version tagged on HEAD for this service, or
# returns non-zero when HEAD carries no tag belonging to it.
service_version_from_tags() {
    local prefix candidate
    for prefix in ${SERVICE_TAG_PREFIXES}; do
        candidate=$(git tag --points-at HEAD 2>/dev/null \
            | grep "^${prefix}" \
            | sed "s|^${prefix}||" \
            | sort -V \
            | tail -n 1) || true
        if [ -n "${candidate}" ]; then
            printf '%s' "${candidate}"
            return 0
        fi
    done
    return 1
}

# CI sets NVCF_VERSION explicitly. Otherwise derive the version from a release
# tag on HEAD, falling back to an MR-style identifier for ordinary dev builds.
if [ -n "${NVCF_VERSION:-}" ]; then
    VERSION="${NVCF_VERSION}"
elif TAG_VERSION=$(service_version_from_tags); then
    VERSION="${TAG_VERSION}"
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
