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
workspace_root="$(cd "${repo_root}/../../.." && pwd)"
tmp_dir="$(mktemp -d)"
fixture_root="${tmp_dir}/fixture"

cleanup() {
  rm -rf "${tmp_dir}"
}
trap cleanup EXIT

mkdir -p "${fixture_root}/deploy/helm"
cp -R "${repo_root}" "${fixture_root}/deploy/helm/nvca-operator"
mkdir -p "${fixture_root}/src/compute-plane-services/nvca/deployments"
cp -R "${workspace_root}/src/compute-plane-services/nvca/deployments/nvca-operator" \
  "${fixture_root}/src/compute-plane-services/nvca/deployments/nvca-operator"

chart_root="${fixture_root}/deploy/helm/nvca-operator"
NVCA_OPERATOR_VERSION="3.2.11" \
NVCA_VERSION="3.2.11" \
NVCA_SHARED_STORAGE_IMAGE_TAG="1.0.5" \
  "${chart_root}/scripts/ci_vendor_nvca_operator_chart" >/dev/null

actual_tag="$(yq -r '.image.tag' "${chart_root}/nvca-operator/values.yaml")"
if [[ -n "${actual_tag}" ]]; then
  echo "expected vendored image.tag to remain empty, got ${actual_tag}" >&2
  exit 1
fi

yq eval -i '.version = "3.2.11"' "${chart_root}/nvca-operator/Chart.yaml"
manifest="${tmp_dir}/manifest.yaml"
helm template nvca-operator "${chart_root}/nvca-operator" \
  --set-string image.repository=registry.example.test/nvca-operator \
  --set-string selfManaged.icmsServiceURL=http://icms.example.invalid:8080 \
  --set-string selfManaged.revalServiceURL=http://reval.example.invalid:8080 \
  --set-string selfManaged.natsURL=nats://nats.example.invalid:4222 \
  > "${manifest}"

if ! grep -Fq 'image: registry.example.test/nvca-operator:3.2.11' "${manifest}"; then
  echo "expected image.tag to fall back to the chart version" >&2
  exit 1
fi

echo "vendor_chart_image_tag_test.sh keeps image.tag empty for chart-version fallback"
