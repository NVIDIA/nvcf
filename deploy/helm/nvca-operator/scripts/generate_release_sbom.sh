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

if [[ $# -ne 3 ]]; then
  echo "usage: $0 <manifest-dir> <release-version> <output-file>" >&2
  exit 1
fi

manifest_dir="$1"
release_version="$2"
output_file="$3"

if [[ ! -d "${manifest_dir}" ]]; then
  echo "manifest directory not found: ${manifest_dir}" >&2
  exit 1
fi

if ! command -v generate-sbom >/dev/null 2>&1; then
  echo "generate-sbom binary is required on PATH" >&2
  exit 1
fi

tmp_dir="$(mktemp -d)"
cleanup() {
  rm -rf "${tmp_dir}"
}
trap cleanup EXIT

workspace_root="${tmp_dir}/workspace"
render_dir="${workspace_root}/out/00-release.yaml-deadbeef-helm-nvca-operator/templates"
artifact_dir="${tmp_dir}/artifacts"

mkdir -p "${workspace_root}/helmfile.d" "${render_dir}" "${artifact_dir}"

cat > "${workspace_root}/helmfile.d/00-release.yaml.gotmpl" <<EOF
releases:
  - name: helm-nvca-operator
    namespace: nvca-operator
    chart: oci://nvcr.io/nvidia/nvcf-byoc/helm-nvca-operator
    version: ${release_version}
EOF

cp "${manifest_dir}"/*.yaml "${render_dir}/"

(
  cd "${workspace_root}"
  generate-sbom --format=sbom -d out -o "${artifact_dir}" release
)

mkdir -p "$(dirname "${output_file}")"
mv "${artifact_dir}/sbom-release.spdx.json" "${output_file}"
