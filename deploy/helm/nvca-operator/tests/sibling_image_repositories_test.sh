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
#
# The credential helper and the OTel collector are published next to the
# operator image. The chart derives their repositories from
# image.repository, so a cluster that pulls the operator from another org
# pulls the sibling images from that org too, and an explicit
# imageRepository still wins.

set -euo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
tmp_dir="$(mktemp -d)"

cleanup() {
  rm -rf "${tmp_dir}"
}
trap cleanup EXIT

render() {
  local operator_repository="$1"
  shift
  helm template nvca-operator "${repo_root}/nvca-operator" \
    --namespace nvca-operator \
    --values "${repo_root}/nvca-operator/values.yaml" \
    --set-string "image.repository=${operator_repository}" \
    --set-string "ngcConfig.serviceKey=test-service-key" \
    --set-string "nameOverride=nvca-operator" \
    --set-string "fullnameOverride=nvca-operator" \
    --set-string "ngcConfig.clusterSource=helm-managed" \
    --set-string "clusterName=test-cluster" \
    --set otelCollector.enabled=true \
    --set helmManaged.otelCollector.enabled=true \
    "$@"
}

cred_helper_repository() {
  yq -r 'select(.kind == "ConfigMap" and .metadata.name == "nvcfbackend-helm-managed") |
    .data."cluster-dto.yaml" | from_yaml | .imageCredentialHelper.imageConfig.repository' "$1"
}

collector_repository() {
  yq -r 'select(.kind == "Deployment" and .metadata.name == "nvca-operator") |
    .spec.template.spec.containers[] | select(.name == "nvca-operator") |
    .env[] | select(.name == "OTEL_COLLECTOR_IMAGE_REPO") | .value' "$1"
}

expect() {
  local what="$1" got="$2" want="$3"
  if [[ "${got}" != "${want}" ]]; then
    echo "${what}: expected ${want}, got ${got}" >&2
    exit 1
  fi
}

# The public org: unchanged defaults.
manifest="${tmp_dir}/public.yaml"
render "nvcr.io/nvidia/nvcf-byoc/nvca-operator" > "${manifest}"
expect "public credential helper" "$(cred_helper_repository "${manifest}")" "nvcr.io/nvidia/nvcf-byoc/nvcf-image-credential-helper"
expect "public collector" "$(collector_repository "${manifest}")" "nvcr.io/nvidia/nvcf-byoc/nvcf-otel-collector"

# Staging: unchanged defaults.
manifest="${tmp_dir}/staging.yaml"
render "stg.nvcr.io/nvidia/nvcf-byoc/nvca-operator" > "${manifest}"
expect "staging credential helper" "$(cred_helper_repository "${manifest}")" "stg.nvcr.io/nvidia/nvcf-byoc/nvcf-image-credential-helper"
expect "staging collector" "$(collector_repository "${manifest}")" "stg.nvcr.io/nvidia/nvcf-byoc/nvcf-otel-collector"

# An operator image from another org (a managed cluster's release org): the
# siblings follow it instead of falling back to the public org.
manifest="${tmp_dir}/sbom-org.yaml"
render "nvcr.io/example-org/example-team/nvca-operator" > "${manifest}"
expect "other-org credential helper" "$(cred_helper_repository "${manifest}")" "nvcr.io/example-org/example-team/nvcf-image-credential-helper"
expect "other-org collector" "$(collector_repository "${manifest}")" "nvcr.io/example-org/example-team/nvcf-otel-collector"

# An explicit repository still wins.
manifest="${tmp_dir}/override.yaml"
render "nvcr.io/example-org/example-team/nvca-operator" \
  --set-string "helmManaged.imageCredHelper.imageRepository=registry.example.invalid/team/nvcf-image-credential-helper" \
  --set-string "otelCollector.imageRepository=registry.example.invalid/team/nvcf-otel-collector" > "${manifest}"
expect "overridden credential helper" "$(cred_helper_repository "${manifest}")" "registry.example.invalid/team/nvcf-image-credential-helper"
expect "overridden collector" "$(collector_repository "${manifest}")" "registry.example.invalid/team/nvcf-otel-collector"

echo "sibling image repositories follow image.repository"
