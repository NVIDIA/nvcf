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

stack_env_file="${tmp_dir}/stack-env.yaml"
output_file="${tmp_dir}/rendered-values.yaml"
stub_bin_dir="${tmp_dir}/bin"
mkdir -p "${stub_bin_dir}"

cat > "${stack_env_file}" <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: <your-org>
EOF

cat > "${stub_bin_dir}/helm" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

if [[ "$1" == "status" ]]; then
  exit 0
fi

if [[ "$1" == "get" && "$2" == "values" ]]; then
  cat <<'YAML'
image:
  tag: 2.52.0-rc.1
selfManaged:
  nvcaVersion: 2.52.0-rc.1
YAML
  exit 0
fi

echo "unexpected helm invocation: $*" >&2
exit 1
EOF
chmod +x "${stub_bin_dir}/helm"

PATH="${stub_bin_dir}:$PATH" \
STACK_ENV_FILE="${stack_env_file}" \
OUTPUT_FILE="${output_file}" \
RELEASE="nvca-operator" \
NAMESPACE="nvca-operator" \
NVCA_OPERATOR_VERSION="2.52.0-rc.5" \
NVCA_VERSION="2.52.0-rc.5" \
"${repo_root}/scripts/render_values_from_stack_env.sh"

actual_image_tag="$(yq -r '.image.tag' "${output_file}")"
actual_nvca_version="$(yq -r '.selfManaged.nvcaVersion' "${output_file}")"

if [[ "${actual_image_tag}" != "2.52.0-rc.5" ]]; then
  echo "expected image.tag to stay aligned with vendored chart defaults, got ${actual_image_tag}" >&2
  exit 1
fi

if [[ "${actual_nvca_version}" != "2.52.0-rc.5" ]]; then
  echo "expected selfManaged.nvcaVersion to stay aligned with vendored chart defaults, got ${actual_nvca_version}" >&2
  exit 1
fi

echo "render_values_from_stack_env.sh keeps upgrade version fields aligned with vendored defaults"
