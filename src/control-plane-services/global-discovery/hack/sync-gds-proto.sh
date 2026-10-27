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
#
# Copies the canonical proto contract under api/proto into the vendored copy
# of the Rust gds-client crate, so a plain Cargo build of the crate needs no
# file outside the stargate workspace. The Bazel diff_test targets in
# src/libraries/rust/stargate/crates/gds-client fail until the copies match.
#
# Usage:
#   hack/sync-gds-proto.sh          copy api/proto into the crate
#   hack/sync-gds-proto.sh --check  fail when the vendored copy differs
set -euo pipefail

module_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
repo_root="$(cd "${module_dir}/../../.." && pwd)"

readonly canonical="${module_dir}/api/proto"
readonly vendored="${repo_root}/src/libraries/rust/stargate/crates/gds-client/proto"
readonly files=(
    google/api/annotations.proto
    google/api/http.proto
    nvidia/gds/v1/cluster.proto
    nvidia/gds/v1/common.proto
    nvidia/gds/v1/mcs.proto
    nvidia/gds/v1/pool.proto
    nvidia/gds/v1/server.proto
)

# Every canonical file must be in the list above; a new proto file needs a new
# entry here and a new diff_test in the crate's BUILD.bazel.
check_complete() {
    local f listed missing=0
    while IFS= read -r f; do
        listed=0
        for entry in "${files[@]}"; do
            if [[ "${entry}" == "${f}" ]]; then
                listed=1
                break
            fi
        done
        if [[ "${listed}" -eq 0 ]]; then
            echo "error: ${f} is not vendored; add it to $0 and to the gds-client BUILD.bazel" >&2
            missing=1
        fi
    done < <(cd "${canonical}" && find . -name '*.proto' | sed 's|^\./||' | sort)
    return "${missing}"
}

check_complete

case "${1:-}" in
    "")
        for f in "${files[@]}"; do
            mkdir -p "$(dirname "${vendored}/${f}")"
            cp "${canonical}/${f}" "${vendored}/${f}"
        done
        echo "sync-gds-proto: copied ${#files[@]} files to ${vendored}"
        ;;
    --check)
        status=0
        for f in "${files[@]}"; do
            if ! cmp -s "${canonical}/${f}" "${vendored}/${f}"; then
                echo "error: ${vendored}/${f} differs from ${canonical}/${f}" >&2
                status=1
            fi
        done
        if [[ "${status}" -ne 0 ]]; then
            echo "error: run $0 and commit the result" >&2
            exit 1
        fi
        echo "sync-gds-proto: vendored protos are up to date"
        ;;
    *)
        echo "usage: $0 [--check]" >&2
        exit 2
        ;;
esac
