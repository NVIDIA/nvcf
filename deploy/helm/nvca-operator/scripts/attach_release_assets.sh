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

package_url="${ci_api_v4_url}/projects/${ci_project_id}/packages/generic/${package_name}/${version}"
release_api="${ci_api_v4_url}/projects/${ci_project_id}/releases/${version}"

echo "Waiting for release ${version} to be created..."
for i in $(seq 1 30); do
  if curl --silent --fail --header "JOB-TOKEN: ${ci_job_token}" "${release_api}" > /dev/null 2>&1; then
    echo "Release found"
    break
  fi
  if [ "${i}" -eq 30 ]; then
    echo "ERROR: Release not found after 30 attempts" >&2
    exit 1
  fi
  sleep 10
done

existing_links="$(curl --silent --header "JOB-TOKEN: ${ci_job_token}" "${release_api}/assets/links")"
echo "Existing release links:"
echo "${existing_links}" | jq -r '.[]?.name'

for path in release-artifacts/*; do
  asset="$(basename "${path}")"
  link_id="$(echo "${existing_links}" | jq -r ".[] | select(.name == \"${asset}\") | .id")"
  if [ -n "${link_id}" ] && [ "${link_id}" != "null" ]; then
    echo "Updating existing link for ${asset} (id: ${link_id})..."
    curl --fail --request PUT \
      --header "JOB-TOKEN: ${ci_job_token}" \
      --header "Content-Type: application/json" \
      --data "{\"name\":\"${asset}\",\"url\":\"${package_url}/${asset}\",\"link_type\":\"package\"}" \
      "${release_api}/assets/links/${link_id}"
  else
    echo "Creating release link for ${asset}..."
    curl --fail --request POST \
      --header "JOB-TOKEN: ${ci_job_token}" \
      --header "Content-Type: application/json" \
      --data "{\"name\":\"${asset}\",\"url\":\"${package_url}/${asset}\",\"link_type\":\"package\"}" \
      "${release_api}/assets/links"
  fi
  echo
done
