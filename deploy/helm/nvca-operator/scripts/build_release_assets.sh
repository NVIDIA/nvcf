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
chart_file="nvca-operator/Chart.yaml"
chart_name="$(sed -n 's/^name:[[:space:]]*//p' "${chart_file}" | head -n1)"
chart_version="${version}"
image_manifest="sbom-inputs/artifacts-${version}.txt"
chart_package_dir="sbom-inputs/package"

mkdir -p sbom-inputs
mkdir -p "${chart_package_dir}"

helm template nvca-operator ./nvca-operator \
  --namespace nvca-operator \
  --values values.local.yml \
  --values values.release-sbom.yaml \
  > sbom-inputs/rendered.yaml

helm package ./nvca-operator \
  -d "${chart_package_dir}" \
  --version "${chart_version}" \
  >/dev/null

chart_package="$(find "${chart_package_dir}" -maxdepth 1 -type f -name '*.tgz' | sort | head -n1)"
if [[ -z "${chart_package}" ]]; then
  echo "failed to package helm chart" >&2
  exit 1
fi

./scripts/render_release_supplemental_images.sh \
  sbom-inputs/rendered.yaml \
  sbom-inputs/supplemental-images.yaml

./scripts/build_release_image_manifest.sh \
  sbom-inputs/rendered.yaml \
  sbom-inputs/supplemental-images.yaml \
  "${image_manifest}"

./scripts/normalize_release_artifact_permissions.sh sbom-inputs

test -s "${image_manifest}"

echo "Release chart: ${chart_name}"
echo "Chart version: ${chart_version}"
echo "Release tag: ${version}"
echo "Release chart package: ${chart_package}"
echo "Image manifest: ${image_manifest}"
cat "${image_manifest}"
