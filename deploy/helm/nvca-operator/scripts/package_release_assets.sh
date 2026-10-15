#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     https://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -euo pipefail

version="${CI_COMMIT_TAG:?CI_COMMIT_TAG is required}"
ci_api_v4_url="${CI_API_V4_URL:?CI_API_V4_URL is required}"
ci_project_id="${CI_PROJECT_ID:?CI_PROJECT_ID is required}"
ci_job_token="${CI_JOB_TOKEN:?CI_JOB_TOKEN is required}"
package_name="${RELEASE_ASSET_PACKAGE_NAME:?RELEASE_ASSET_PACKAGE_NAME is required}"

mkdir -p release-artifacts

./scripts/generate_release_sbom.sh \
  ./sbom-inputs \
  "${version}" \
  "release-artifacts/sbom-${version}.spdx.json"

cp "sbom-inputs/artifacts-${version}.txt" "release-artifacts/artifacts-${version}.txt"
cp ./sbom-inputs/package/*.tgz release-artifacts/

artifact_paths=(release-artifacts/*)
if [[ ${#artifact_paths[@]} -eq 0 || ! -e "${artifact_paths[0]}" ]]; then
  echo "release artifacts were not created" >&2
  exit 1
fi

artifact_files=()
for path in "${artifact_paths[@]}"; do
  artifact_files+=("$(basename "${path}")")
done

sorted_artifact_files=()
while IFS= read -r file; do
  sorted_artifact_files+=("${file}")
done < <(printf '%s\n' "${artifact_files[@]}" | sort)
artifact_files=("${sorted_artifact_files[@]}")

(
  cd release-artifacts
  sha256sum "${artifact_files[@]}" > "checksums-${version}.sha256"
)

for path in release-artifacts/*; do
  file="$(basename "${path}")"
  echo "Uploading ${file} to package registry..."
  curl --fail --header "JOB-TOKEN: ${ci_job_token}" \
    --upload-file "${path}" \
    "${ci_api_v4_url}/projects/${ci_project_id}/packages/generic/${package_name}/${version}/${file}"
done
