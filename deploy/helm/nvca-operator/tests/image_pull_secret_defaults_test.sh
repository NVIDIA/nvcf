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

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
tmp_dir="$(mktemp -d)"

cleanup() {
  rm -rf "${tmp_dir}"
}
trap cleanup EXIT

manifest_default="${tmp_dir}/manifest-default.yaml"
manifest_disabled="${tmp_dir}/manifest-disabled.yaml"

image_repository="stg.nvcr.io/nvidia/nvcf-byoc/nvca-operator"

# Render with vendored defaults. An ngc-managed install derives its registry
# credentials from ngcConfig.serviceKey, so the chart must generate the pull
# secret without the operator passing an extra flag.
helm template nvca-operator "${repo_root}/nvca-operator" \
  --namespace nvca-operator \
  --values "${repo_root}/nvca-operator/values.yaml" \
  --set-string "image.repository=${image_repository}" \
  > "${manifest_default}"

# Render with the generated secret turned off, as self-hosted installs do when
# they supply their own imagePullSecrets.
helm template nvca-operator "${repo_root}/nvca-operator" \
  --namespace nvca-operator \
  --values "${repo_root}/nvca-operator/values.yaml" \
  --set-string "image.repository=${image_repository}" \
  --set generateImagePullSecret=false \
  > "${manifest_disabled}"

# --- default render assertions ---

secret_type="$(
  yq -r 'select(.kind == "Secret" and .metadata.name == "nvca-operator-image-pull") | .type' \
    "${manifest_default}"
)"
if [[ "${secret_type}" != "kubernetes.io/dockerconfigjson" ]]; then
  echo "expected generated nvca-operator-image-pull Secret of type kubernetes.io/dockerconfigjson by default, got '${secret_type}'" >&2
  exit 1
fi

registry_host="$(
  yq -r 'select(.kind == "Secret" and .metadata.name == "nvca-operator-image-pull") | .data.".dockerconfigjson"' \
    "${manifest_default}" | base64 -d | yq -r '.auths | keys | .[0]'
)"
if [[ "${registry_host}" != "stg.nvcr.io" ]]; then
  echo "expected generated dockerconfigjson to carry an auth entry for stg.nvcr.io, got '${registry_host}'" >&2
  exit 1
fi

pull_secret_ref="$(
  yq -r 'select(.kind == "Deployment" and .metadata.name == "nvca-operator") |
    .spec.template.spec.imagePullSecrets[] | .name' \
    "${manifest_default}"
)"
if [[ "${pull_secret_ref}" != "nvca-operator-image-pull" ]]; then
  echo "expected nvca-operator Deployment to reference the generated pull secret, got '${pull_secret_ref}'" >&2
  exit 1
fi

# --- disabled render assertions ---

secret_disabled="$(
  yq -r 'select(.kind == "Secret" and .metadata.name == "nvca-operator-image-pull") | .metadata.name' \
    "${manifest_disabled}" | grep -c . || true
)"
if [[ "${secret_disabled}" != "0" ]]; then
  echo "expected no generated pull secret when generateImagePullSecret=false, got ${secret_disabled}" >&2
  exit 1
fi

echo "image pull secret defaults: chart generates nvca-operator-image-pull from ngcConfig.serviceKey by default and omits it when generateImagePullSecret=false"
