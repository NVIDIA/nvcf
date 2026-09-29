#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
#
# Asserts the binary inside the image tarball is executable at
# /usr/bin/pylon-operator. Without this guard, rules_pkg defaults non-source
# srcs to 0644 and the container fails to start with "permission denied".
set -euo pipefail

image_tar="$1"
entrypoint_path="usr/bin/pylon-operator"
tmp_dir="${TEST_TMPDIR:-/tmp}/pylon-operator-mode-${RANDOM}-${RANDOM}"
outer_dir="${tmp_dir}/outer"
mkdir -p "${outer_dir}"
trap 'rm -rf "${tmp_dir}"' EXIT

tar -xf "${image_tar}" -C "${outer_dir}"

while IFS= read -r candidate; do
  if ! tar -tf "${candidate}" >/dev/null 2>&1; then
    continue
  fi
  if ! tar -tf "${candidate}" | grep -Eq "^(\./)?${entrypoint_path}\$"; then
    continue
  fi

  layer_dir="${tmp_dir}/layer"
  mkdir -p "${layer_dir}"
  tar -xf "${candidate}" -C "${layer_dir}"

  entrypoint="${layer_dir}/${entrypoint_path}"
  if [[ ! -x "${entrypoint}" ]]; then
    echo "/${entrypoint_path} is not executable" >&2
    ls -l "${entrypoint}" >&2
    exit 1
  fi

  exit 0
done < <(find "${outer_dir}" -type f)

echo "no image layer contains /${entrypoint_path}" >&2
find "${outer_dir}" -type f -print >&2
exit 1
