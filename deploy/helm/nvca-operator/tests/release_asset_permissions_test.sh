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

artifact_dir="${tmp_dir}/sbom-inputs"
mkdir -p "${artifact_dir}/nested"

umask 077
printf 'image\n' > "${artifact_dir}/artifacts-test.txt"
printf 'kind: List\n' > "${artifact_dir}/nested/rendered.yaml"
umask 022

"${repo_root}/scripts/normalize_release_artifact_permissions.sh" "${artifact_dir}"

mode_of() {
  if stat -c '%a' "$1" >/dev/null 2>&1; then
    stat -c '%a' "$1"
  else
    stat -f '%Lp' "$1"
  fi
}

file_mode="$(mode_of "${artifact_dir}/artifacts-test.txt")"
dir_mode="$(mode_of "${artifact_dir}")"
nested_dir_mode="$(mode_of "${artifact_dir}/nested")"

if [[ "${file_mode}" != "644" ]]; then
  echo "expected artifact file mode 644, got ${file_mode}" >&2
  exit 1
fi

if [[ "${dir_mode}" != "755" ]]; then
  echo "expected artifact directory mode 755, got ${dir_mode}" >&2
  exit 1
fi

if [[ "${nested_dir_mode}" != "755" ]]; then
  echo "expected nested artifact directory mode 755, got ${nested_dir_mode}" >&2
  exit 1
fi

echo "release artifact permissions are normalized for cross-job access"
