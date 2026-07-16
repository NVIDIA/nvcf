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

mkdir -p "${tmp_dir}/scripts" "${tmp_dir}/sbom-inputs/package" "${tmp_dir}/bin"

cp "${repo_root}/scripts/package_release_assets.sh" "${tmp_dir}/scripts/package_release_assets.sh"
chmod +x "${tmp_dir}/scripts/package_release_assets.sh"

cat > "${tmp_dir}/scripts/generate_release_sbom.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '{"spdxVersion":"SPDX-2.3"}\n' > "$3"
EOF
chmod +x "${tmp_dir}/scripts/generate_release_sbom.sh"

cat > "${tmp_dir}/bin/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

upload_file=""
url=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --upload-file)
      upload_file="$2"
      shift 2
      ;;
    --header)
      shift 2
      ;;
    --fail)
      shift
      ;;
    http*)
      url="$1"
      shift
      ;;
    *)
      shift
      ;;
  esac
done

printf '%s\t%s\n' "${upload_file}" "${url}" >> "${UPLOAD_LOG:?UPLOAD_LOG is required}"
EOF
chmod +x "${tmp_dir}/bin/curl"

printf 'image\n' > "${tmp_dir}/sbom-inputs/artifacts-9.9.9.txt"
printf 'chart-bytes\n' > "${tmp_dir}/sbom-inputs/package/helm-nvca-operator-9.9.9.tgz"

upload_log="${tmp_dir}/upload.log"

(
  cd "${tmp_dir}"
  PATH="${tmp_dir}/bin:${PATH}" \
  CI_COMMIT_TAG=9.9.9 \
  CI_API_V4_URL=https://gitlab.example.com/api/v4 \
  CI_PROJECT_ID=123 \
  CI_JOB_TOKEN=token \
  RELEASE_ASSET_PACKAGE_NAME=nvca-operator-release-assets \
  UPLOAD_LOG="${upload_log}" \
  ./scripts/package_release_assets.sh
)

for expected in \
  "${tmp_dir}/release-artifacts/artifacts-9.9.9.txt" \
  "${tmp_dir}/release-artifacts/sbom-9.9.9.spdx.json" \
  "${tmp_dir}/release-artifacts/helm-nvca-operator-9.9.9.tgz" \
  "${tmp_dir}/release-artifacts/checksums-9.9.9.sha256"
do
  if [[ ! -f "${expected}" ]]; then
    echo "expected release artifact missing: ${expected}" >&2
    exit 1
  fi
done

checksum_file="${tmp_dir}/release-artifacts/checksums-9.9.9.sha256"
for checksummed in \
  'artifacts-9.9.9.txt' \
  'sbom-9.9.9.spdx.json' \
  'helm-nvca-operator-9.9.9.tgz'
do
  if ! grep -Fq "  ${checksummed}" "${checksum_file}"; then
    echo "expected checksum file to include ${checksummed}" >&2
    cat "${checksum_file}" >&2
    exit 1
  fi
done

for uploaded in \
  "release-artifacts/artifacts-9.9.9.txt" \
  "release-artifacts/sbom-9.9.9.spdx.json" \
  "release-artifacts/helm-nvca-operator-9.9.9.tgz" \
  "release-artifacts/checksums-9.9.9.sha256"
do
  if ! grep -Fq "${uploaded}" "${upload_log}"; then
    echo "expected upload log to include ${uploaded}" >&2
    cat "${upload_log}" >&2
    exit 1
  fi
done

echo "package_release_assets.sh uploads the chart package and checksum alongside manifest assets"
