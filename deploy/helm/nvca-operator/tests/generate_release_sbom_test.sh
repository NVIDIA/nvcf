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

manifest_dir="${tmp_dir}/manifests"
output_file="${tmp_dir}/sbom-1.4.7.spdx.json"
stub_bin_dir="${tmp_dir}/bin"
mkdir -p "${manifest_dir}" "${stub_bin_dir}"

cat > "${manifest_dir}/rendered.yaml" <<'EOF'
apiVersion: v1
kind: Pod
metadata:
  name: nvca-operator
spec:
  containers:
    - name: operator
      image: nvcr.io/nvidia/nvcf-byoc/nvca-operator:2.52.0-rc.5
EOF

cat > "${manifest_dir}/supplemental-images.yaml" <<'EOF'
apiVersion: v1
kind: Pod
metadata:
  name: nvca-release-supplemental-images
spec:
  containers:
    - name: image-00
      image: nvcr.io/nvidia/nvcf-byoc/nvca:2.52.0-rc.5
EOF

cat > "${stub_bin_dir}/generate-sbom" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

if [[ ! -f "helmfile.d/00-release.yaml.gotmpl" ]]; then
  echo "expected synthetic helmfile workspace" >&2
  exit 1
fi

if ! grep -Fq "version: 1.4.7" "helmfile.d/00-release.yaml.gotmpl"; then
  echo "expected release version in synthetic helmfile" >&2
  exit 1
fi

if [[ ! -f "out/00-release.yaml-deadbeef-helm-nvca-operator/templates/rendered.yaml" ]]; then
  echo "expected rendered manifest in synthetic out dir" >&2
  exit 1
fi

if [[ ! -f "out/00-release.yaml-deadbeef-helm-nvca-operator/templates/supplemental-images.yaml" ]]; then
  echo "expected supplemental manifest in synthetic out dir" >&2
  exit 1
fi

output_dir=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    -o|--output-dir)
      output_dir="$2"
      shift 2
      ;;
    *)
      shift
      ;;
  esac
done

mkdir -p "${output_dir}"
printf '{"spdxVersion":"SPDX-2.3"}\n' > "${output_dir}/sbom-release.spdx.json"
EOF
chmod +x "${stub_bin_dir}/generate-sbom"

PATH="${stub_bin_dir}:$PATH" \
  "${repo_root}/scripts/generate_release_sbom.sh" \
  "${manifest_dir}" \
  "1.4.7" \
  "${output_file}"

if [[ ! -f "${output_file}" ]]; then
  echo "expected output file to be created" >&2
  exit 1
fi

if ! grep -Fq '"spdxVersion":"SPDX-2.3"' "${output_file}"; then
  echo "expected wrapper to preserve generated sbom output" >&2
  exit 1
fi

echo "generate_release_sbom.sh prepares a synthetic workspace for generate-sbom"
