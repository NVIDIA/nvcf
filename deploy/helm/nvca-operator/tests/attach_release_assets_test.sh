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

mkdir -p "${tmp_dir}/scripts" "${tmp_dir}/release-artifacts" "${tmp_dir}/bin"

cp "${repo_root}/scripts/attach_release_assets.sh" "${tmp_dir}/scripts/attach_release_assets.sh"
chmod +x "${tmp_dir}/scripts/attach_release_assets.sh"

printf 'image\n' > "${tmp_dir}/release-artifacts/artifacts-9.9.9.txt"
printf '{"spdxVersion":"SPDX-2.3"}\n' > "${tmp_dir}/release-artifacts/sbom-9.9.9.spdx.json"
printf 'chart-bytes\n' > "${tmp_dir}/release-artifacts/helm-nvca-operator-9.9.9.tgz"
printf 'deadbeef  artifacts-9.9.9.txt\n' > "${tmp_dir}/release-artifacts/checksums-9.9.9.sha256"

cat > "${tmp_dir}/bin/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

method="GET"
url=""
payload=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --request)
      method="$2"
      shift 2
      ;;
    --data)
      payload="$2"
      shift 2
      ;;
    --header)
      shift 2
      ;;
    --silent|--fail)
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

case "${method} ${url}" in
  "GET https://gitlab.example.com/api/v4/projects/123/releases/9.9.9")
    printf '{"tag_name":"9.9.9"}\n'
    ;;
  "GET https://gitlab.example.com/api/v4/projects/123/releases/9.9.9/assets/links")
    printf '[]\n'
    ;;
  "POST https://gitlab.example.com/api/v4/projects/123/releases/9.9.9/assets/links")
    printf '%s\n' "${payload}" >> "${POST_LOG:?POST_LOG is required}"
    printf '{"id":1}\n'
    ;;
  *)
    echo "unexpected curl invocation: ${method} ${url}" >&2
    exit 1
    ;;
esac
EOF
chmod +x "${tmp_dir}/bin/curl"

post_log="${tmp_dir}/post.log"

(
  cd "${tmp_dir}"
  PATH="${tmp_dir}/bin:${PATH}" \
  CI_COMMIT_TAG=9.9.9 \
  CI_API_V4_URL=https://gitlab.example.com/api/v4 \
  CI_PROJECT_ID=123 \
  CI_JOB_TOKEN=token \
  RELEASE_ASSET_PACKAGE_NAME=nvca-operator-release-assets \
  POST_LOG="${post_log}" \
  ./scripts/attach_release_assets.sh
)

for expected in \
  'artifacts-9.9.9.txt' \
  'sbom-9.9.9.spdx.json' \
  'helm-nvca-operator-9.9.9.tgz' \
  'checksums-9.9.9.sha256'
do
  if ! grep -Fq "${expected}" "${post_log}"; then
    echo "expected release link creation for ${expected}" >&2
    cat "${post_log}" >&2
    exit 1
  fi
done

echo "attach_release_assets.sh links every packaged release artifact including checksums"
