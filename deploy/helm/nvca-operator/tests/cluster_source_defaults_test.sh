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

manifest_ngc_managed="${tmp_dir}/manifest-ngc-managed.yaml"
manifest_self_managed="${tmp_dir}/manifest-self-managed.yaml"

# Render with default values (ngc-managed)
helm template nvca-operator "${repo_root}/nvca-operator" \
  --namespace nvca-operator \
  --values "${repo_root}/nvca-operator/values.yaml" \
  > "${manifest_ngc_managed}"

# Render with self-managed clusterSource
helm template nvca-operator "${repo_root}/nvca-operator" \
  --namespace nvca-operator \
  --values "${repo_root}/nvca-operator/values.yaml" \
  --set-string ngcConfig.clusterSource=self-managed \
  --set-string selfManaged.icmsServiceURL=http://icms.example.invalid:8080 \
  --set-string selfManaged.revalServiceURL=http://reval.example.invalid:8080 \
  --set-string selfManaged.natsURL=nats://nats.example.invalid:4222 \
  > "${manifest_self_managed}"

# --- ngc-managed assertions ---

cluster_source_ngc="$(
  yq -r 'select(.kind == "Deployment" and .metadata.name == "nvca-operator") |
    .spec.template.spec.containers[] | select(.name == "nvca-operator") |
    .env[] | select(.name == "NVCA_CLUSTER_SOURCE") | .value' \
    "${manifest_ngc_managed}"
)"
if [[ "${cluster_source_ngc}" != "ngc-managed" ]]; then
  echo "expected NVCA_CLUSTER_SOURCE=ngc-managed in default render, got '${cluster_source_ngc}'" >&2
  exit 1
fi

helm_managed_data_ngc="$(
  yq -r 'select(.kind == "ConfigMap" and .metadata.name == "nvcfbackend-helm-managed") | .data // {} | length' \
    "${manifest_ngc_managed}"
)"
if [[ "${helm_managed_data_ngc}" != "0" ]]; then
  echo "expected nvcfbackend-helm-managed to have empty data for ngc-managed, got ${helm_managed_data_ngc} keys" >&2
  exit 1
fi

self_managed_data_ngc="$(
  yq -r 'select(.kind == "ConfigMap" and .metadata.name == "nvcfbackend-self-managed") | .data // {} | length' \
    "${manifest_ngc_managed}"
)"
if [[ "${self_managed_data_ngc}" != "0" ]]; then
  echo "expected nvcfbackend-self-managed to have empty data for ngc-managed, got ${self_managed_data_ngc} keys" >&2
  exit 1
fi

# --- self-managed assertions ---

cluster_source_sm="$(
  yq -r 'select(.kind == "Deployment" and .metadata.name == "nvca-operator") |
    .spec.template.spec.containers[] | select(.name == "nvca-operator") |
    .env[] | select(.name == "NVCA_CLUSTER_SOURCE") | .value' \
    "${manifest_self_managed}"
)"
if [[ "${cluster_source_sm}" != "self-managed" ]]; then
  echo "expected NVCA_CLUSTER_SOURCE=self-managed in self-managed render, got '${cluster_source_sm}'" >&2
  exit 1
fi

self_managed_data_sm="$(
  yq -r 'select(.kind == "ConfigMap" and .metadata.name == "nvcfbackend-self-managed") | .data // {} | length' \
    "${manifest_self_managed}"
)"
if [[ "${self_managed_data_sm}" == "0" ]]; then
  echo "expected nvcfbackend-self-managed to have data for self-managed render, got empty" >&2
  exit 1
fi

helm_managed_data_sm="$(
  yq -r 'select(.kind == "ConfigMap" and .metadata.name == "nvcfbackend-helm-managed") | .data // {} | length' \
    "${manifest_self_managed}"
)"
if [[ "${helm_managed_data_sm}" != "0" ]]; then
  echo "expected nvcfbackend-helm-managed to have empty data for self-managed, got ${helm_managed_data_sm} keys" >&2
  exit 1
fi

echo "clusterSource defaults: ngc-managed renders NVCA_CLUSTER_SOURCE=ngc-managed with empty config maps; self-managed renders NVCA_CLUSTER_SOURCE=self-managed with populated nvcfbackend-self-managed"
