#! /bin/bash
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

# Exit if any command fails
set -e

EXAMPLE_USAGE="example usage: build_distroless.sh <VERSION>"
SCRIPT_DIR="$( cd -- "$( dirname -- "${BASH_SOURCE[0]}" )" &> /dev/null && pwd )"
REPO_ROOT_DIR=$(dirname "$(dirname "${SCRIPT_DIR}")")

if [[ -z "$1" ]]; then
  echo "need to provide version number as arg"
  echo "$EXAMPLE_USAGE"
  exit 1
fi

VERSION=$1

# Build binaries
echo "Building binaries..."
"${SCRIPT_DIR}"/build_zips.sh "v${VERSION}"

BUILD_DATE=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
echo "BUILD_DATE: ${BUILD_DATE}"

echo "Building distroless container images..."
docker buildx build --load --pull \
        -f "${REPO_ROOT_DIR}/nv_releases/docker/Dockerfile.distroless" \
        --platform linux/amd64,linux/arm64 \
        --label "version=${VERSION}" \
        --label "build-date=${BUILD_DATE}" \
        --tag ess-agent/distroless-go-multi-arch:"${VERSION}" "${REPO_ROOT_DIR}/nv_releases"

# Run multi-arch image
docker run --rm -it ess-agent/distroless-go-multi-arch:"${VERSION}" --version
