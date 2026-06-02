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

# K8s GPU DRA driver version.
DRA_DRIVER_GPU_VERSION="v25.8.0"

normalize_version() {
    local version="$1"
    printf '%s\n' "${version#v}"
}

# Use CI environment variables when running in CI. Tolerate GitLab
# vs GitHub Actions: GitLab sets CI_COMMIT_SHORT_SHA / CI_COMMIT_TAG
# / CI_MERGE_REQUEST_IID; GHA sets CI=true too but has none of those.
# Fall back to git for the commit on the GHA path so the script does
# not die with "CI_COMMIT_SHORT_SHA: unbound variable" when the
# umbrella's public mirror runs `bazel build //...` from GHA.
#
# Version format is preserved by provider so downstream consumers
# that pattern-match on `mr.` (OCI tag filters, artifact retention
# rules, Helm chart version validators) keep working under GitLab:
#   GitLab tag-pipeline:   <semver>             (unchanged)
#   GitLab MR pipeline:    mr.<iid>-<sha>       (unchanged)
#   GitLab branch-push:    mr.0-<sha>           (unchanged)
#   GHA:                   ci-<sha>             (new -- previously crashed)
if [ "${CI:-}" = "true" ]; then
    COMMIT="${CI_COMMIT_SHORT_SHA:-$(git rev-parse --short HEAD 2>/dev/null || echo unknown)}"
    if [ -n "${CI_COMMIT_TAG:-}" ]; then
        VERSION="$(normalize_version "${CI_COMMIT_TAG}")"
    elif [ -n "${GITHUB_ACTIONS:-}" ]; then
        # GHA path: no MR IID, no semver tag, GitLab-style "mr."
        # prefix would be misleading. ci-<sha> is enough to
        # disambiguate cached artifacts; stamped binaries fall back
        # cleanly on subsequent `bazel build`.
        VERSION="ci-${COMMIT}"
    else
        # GitLab MR or branch-push. Historical format preserved.
        VERSION="mr.${CI_MERGE_REQUEST_IID:-"0"}-${COMMIT}"
    fi
    BUILD_USER="ci"
else
    COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")

    if [ -n "${NVCF_VERSION:-}" ]; then
        VERSION="$(normalize_version "${NVCF_VERSION}")"
    elif [ -n "${EGX_VERSION:-}" ]; then
        VERSION="$(normalize_version "${EGX_VERSION}")"
    else
        VERSION="dev-${COMMIT}"
    fi

    BUILD_USER="${NVCF_BUILD_USER:-$(whoami 2>/dev/null || echo 'unknown')}"
fi

DIRTY=""
if [ "$COMMIT" != "unknown" ] && [ -n "$(git status --porcelain 2>/dev/null)" ]; then
    DIRTY="-dirty"
fi

if command -v go >/dev/null 2>&1; then
    GO_VERSION="$(go list -m -f '{{.GoVersion}}' || echo "bazel-rules_go")"
else
    GO_VERSION="bazel-rules_go"
fi

BUILD_DATE=$(date -u +"%Y-%m-%dT%H:%M:%SZ")

echo "STABLE_VERSION ${VERSION}${DIRTY}"
echo "STABLE_GIT_COMMIT ${COMMIT}${DIRTY}"
echo "STABLE_BUILD_USER ${BUILD_USER}"
echo "STABLE_GO_VERSION ${GO_VERSION}"
echo "STABLE_OCI_TAG ${VERSION}${DIRTY}"
echo "STABLE_DRA_DRIVER_GPU_VERSION ${DRA_DRIVER_GPU_VERSION}"

# Volatile keys (no STABLE_ prefix). Bazel injects them into stamped
# binaries the same way as STABLE_* keys, but their value changes do
# NOT invalidate the action cache. BUILD_DATE moves on every invocation;
# keeping it volatile is what lets `--stamp` reuse the cached link of
# nvcf-cli_lib instead of forcing a relink on every CI run.
echo "BUILD_DATE ${BUILD_DATE}"
