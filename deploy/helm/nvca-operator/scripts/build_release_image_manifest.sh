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
  echo "usage: $0 <rendered-manifest.yaml> <supplemental-manifest.yaml> <output.txt>" >&2
  exit 1
fi

rendered_manifest="$1"
supplemental_manifest="$2"
output_manifest="$3"

for input in "${rendered_manifest}" "${supplemental_manifest}"; do
  if [[ ! -f "${input}" ]]; then
    echo "manifest not found: ${input}" >&2
    exit 1
  fi
done

tmp_images="$(mktemp)"
cleanup() {
  rm -f "${tmp_images}"
}
trap cleanup EXIT

awk '
function valid_image(value) {
  if (value == "") {
    return 0
  }
  if (value ~ /:\/\//) {
    return 0
  }
  if (index(value, "{{") > 0 || index(value, "${") > 0) {
    return 0
  }
  if (value !~ /\//) {
    return 0
  }
  if (value !~ /[:@]/) {
    return 0
  }
  return 1
}
/^[[:space:]]*image:[[:space:]]*/ {
  image = $0
  sub(/^[[:space:]]*image:[[:space:]]*/, "", image)
  sub(/[[:space:]]*#.*/, "", image)
  gsub(/^[[:space:]]+|[[:space:]]+$/, "", image)
  gsub(/^["'\'']/, "", image)
  gsub(/["'\'']$/, "", image)
  if (valid_image(image)) {
    print image
  }
}
' "${rendered_manifest}" "${supplemental_manifest}" | sort -u > "${tmp_images}"

cp "${tmp_images}" "${output_manifest}"
