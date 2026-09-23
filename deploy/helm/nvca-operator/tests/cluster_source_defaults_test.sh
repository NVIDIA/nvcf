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

# --- BYOO OTel collector registry assertions ---
#
# BYOC (ngc-managed) clusters authenticate image pulls with the NGC Cluster
# Key issued at registration, which is scoped to nvcf-core, not
# nvidia/nvcf-byoc. Other cluster sources are unverified and keep today's
# nvidia/nvcf-byoc default.

decode_byoo_function_override() {
  local manifest_path="$1"
  local encoded_overrides

  encoded_overrides="$(
    awk '
      $0 ~ "-[[:space:]]+--function-env-overrides-b64$" {
        expect_value = 1
        next
      }
      expect_value && /^[[:space:]]*-[[:space:]]*/ {
        value = $0
        sub(/^[[:space:]]*-[[:space:]]*"?/, "", value)
        sub(/"[[:space:]]*$/, "", value)
        print value
        exit
      }
    ' "${manifest_path}"
  )"

  if [[ -z "${encoded_overrides}" ]]; then
    echo "missing --function-env-overrides-b64 in ${manifest_path}" >&2
    exit 1
  fi

  local decoded_overrides
  if decoded_overrides="$(printf '%s' "${encoded_overrides}" | base64 --decode 2>/dev/null)"; then
    :
  elif decoded_overrides="$(printf '%s' "${encoded_overrides}" | base64 -D 2>/dev/null)"; then
    :
  else
    echo "invalid --function-env-overrides-b64 base64 in ${manifest_path}" >&2
    exit 1
  fi

  printf '%s' "${decoded_overrides}" | yq -er '.BYOO_OTEL_COLLECTOR_CONTAINER'
}

byoo_otel_collector_tag="$(yq -r '.agent.byooOtelCollector.imageTag' "${repo_root}/nvca-operator/values.yaml")"
byoo_ngc_managed="$(decode_byoo_function_override "${manifest_ngc_managed}")"
byoo_self_managed="$(decode_byoo_function_override "${manifest_self_managed}")"

if [[ "${byoo_ngc_managed}" != "nvcr.io/qtfpt1h0bieu/nvcf-core/byoo-otel-collector:${byoo_otel_collector_tag}" ]]; then
  echo "expected ngc-managed (BYOC) BYOO collector default to use the nvcf-core repository, got ${byoo_ngc_managed}" >&2
  exit 1
fi

if [[ "${byoo_self_managed}" != "nvcr.io/nvidia/nvcf-byoc/byoo-otel-collector:${byoo_otel_collector_tag}" ]]; then
  echo "expected self-managed BYOO collector default to keep the nvidia/nvcf-byoc repository, got ${byoo_self_managed}" >&2
  exit 1
fi

echo "clusterSource defaults: ngc-managed renders NVCA_CLUSTER_SOURCE=ngc-managed with empty config maps and the nvcf-core BYOO collector repository; self-managed renders NVCA_CLUSTER_SOURCE=self-managed with populated nvcfbackend-self-managed and the nvidia/nvcf-byoc BYOO collector repository"
