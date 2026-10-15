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

root_dir="$(cd "$(dirname "$0")/.." && pwd)"
stack_env_file="${STACK_ENV_FILE:?STACK_ENV_FILE is required}"
output_file="${OUTPUT_FILE:?OUTPUT_FILE is required}"
release="${RELEASE:-nvca-operator}"
namespace="${NAMESPACE:-nvca-operator}"
env_nvca_operator_version="${NVCA_OPERATOR_VERSION:-}"
env_nvca_version="${NVCA_VERSION:-}"

if [ -f "${root_dir}/.env" ]; then
  # Keep stack-derived rendering aligned with the currently vendored chart version.
  # Explicit environment variables still win over values sourced from .env.
  # shellcheck disable=SC1091
  source "${root_dir}/.env"
fi

if [ -n "${env_nvca_operator_version}" ]; then
  NVCA_OPERATOR_VERSION="${env_nvca_operator_version}"
fi

if [ -n "${env_nvca_version}" ]; then
  NVCA_VERSION="${env_nvca_version}"
fi

for cmd in yq helm; do
  if ! command -v "${cmd}" >/dev/null 2>&1; then
    echo "Error: '${cmd}' command not found" >&2
    exit 1
  fi
done

if [ ! -f "${stack_env_file}" ]; then
  echo "Error: stack environment file not found: ${stack_env_file}" >&2
  exit 1
fi

stack_image_registry="$(yq -r '.global.image.registry // ""' "${stack_env_file}")"
stack_image_repository="$(yq -r '.global.image.repository // ""' "${stack_env_file}")"

if [ -z "${stack_image_registry}" ] || [ -z "${stack_image_repository}" ]; then
  echo "Error: ${stack_env_file} is missing global.image.registry or global.image.repository" >&2
  exit 1
fi

repo_prefix="${stack_image_registry}/${stack_image_repository}"
tmp_dir="$(mktemp -d)"
tmp_existing="${tmp_dir}/existing-values.yaml"

cleanup() {
  rm -rf "${tmp_dir}"
}
trap cleanup EXIT

cp "${root_dir}/values.local.yml" "${output_file}"

if helm status "${release}" --namespace "${namespace}" >/dev/null 2>&1; then
  echo "Found existing release ${release} in namespace ${namespace}, reusing its current values when present."
  helm get values "${release}" --namespace "${namespace}" -o yaml > "${tmp_existing}"
  yq eval-all -i '. as $item ireduce ({}; . *+ $item )' "${output_file}" "${tmp_existing}"
fi

export REPO_PREFIX="${repo_prefix}"
yq eval -i '
  .image.repository = strenv(REPO_PREFIX) + "/nvca-operator" |
  .nvcaImage.repositoryOverride = strenv(REPO_PREFIX) + "/nvca" |
  .ngcConfig.clusterSource = "self-managed" |
  .ngcConfig.serviceKey = "not-used" |
  .generateImagePullSecret = false |
  .selfManaged.featureGateValues = ["DynamicGPUDiscovery", "SelfHosted"] |
  .selfManaged.imageCredHelper.imageRepository = strenv(REPO_PREFIX) + "/nvcf-image-credential-helper" |
  .selfManaged.sharedStorage.imageRepository = strenv(REPO_PREFIX) + "/samba" |
  .nameOverride = "nvca-operator" |
  .fullnameOverride = "nvca-operator"
' "${output_file}"

if [ -n "${NVCA_OPERATOR_VERSION:-}" ]; then
  export NVCA_OPERATOR_VERSION
  yq eval -i '.image.tag = strenv(NVCA_OPERATOR_VERSION)' "${output_file}"
fi

if [ -n "${NVCA_VERSION:-}" ]; then
  export NVCA_VERSION
  yq eval -i '.selfManaged.nvcaVersion = strenv(NVCA_VERSION)' "${output_file}"
fi

if [ -n "${NCA_ID:-}" ]; then
  export NCA_ID
  yq eval -i '.ncaID = strenv(NCA_ID)' "${output_file}"
fi

if [ -n "${CLUSTER_NAME:-}" ]; then
  export CLUSTER_NAME
  yq eval -i '.clusterName = strenv(CLUSTER_NAME)' "${output_file}"
fi

if [ -n "${CLUSTER_ID:-}" ]; then
  export CLUSTER_ID
  yq eval -i '.clusterID = strenv(CLUSTER_ID)' "${output_file}"
fi

if [ -n "${IMAGE_PULL_SECRET_NAME:-}" ]; then
  export IMAGE_PULL_SECRET_NAME
  yq eval -i '.imagePullSecrets = [{"name": strenv(IMAGE_PULL_SECRET_NAME)}]' "${output_file}"
fi

echo "Generated ${output_file} from ${stack_env_file}"
echo "  image repository prefix: ${repo_prefix}"
echo "  release namespace: ${namespace}"
