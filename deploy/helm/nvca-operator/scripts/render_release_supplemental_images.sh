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

if [[ $# -ne 2 ]]; then
  echo "usage: $0 <rendered-manifest.yaml> <supplemental-manifest.yaml>" >&2
  exit 1
fi

input_manifest="$1"
output_manifest="$2"

if [[ ! -f "${input_manifest}" ]]; then
  echo "rendered manifest not found: ${input_manifest}" >&2
  exit 1
fi

tmp_images="$(mktemp)"
cleanup() {
  rm -f "${tmp_images}"
}
trap cleanup EXIT

awk '
function emit(repo, tag, image) {
  repo = trim(repo)
  tag = trim(tag)
  if (repo == "" || tag == "") {
    return
  }
  if (repo ~ /^</ || tag ~ /^</) {
    return
  }
  image = repo ":" tag
  if (index(image, "{{") > 0 || index(image, "${") > 0) {
    return
  }
  seen[image] = 1
}
function trim(value) {
  gsub(/^[[:space:]]+|[[:space:]]+$/, "", value)
  sub(/[[:space:]]+#.*/, "", value)
  gsub(/^"/, "", value)
  gsub(/"$/, "", value)
  gsub(/^'\''/, "", value)
  gsub(/'\''$/, "", value)
  return value
}
/name:[[:space:]]+NVCA_IMAGE_REPO$/ {
  expect_nvca_repo = 1
  next
}
expect_nvca_repo && /^[[:space:]]*value:[[:space:]]*/ {
  nvca_repo = $0
  sub(/^[[:space:]]*value:[[:space:]]*/, "", nvca_repo)
  nvca_repo = trim(nvca_repo)
  expect_nvca_repo = 0
  if (nvca_version != "") {
    emit(nvca_repo, nvca_version)
  }
  next
}
/name:[[:space:]]+OTEL_COLLECTOR_IMAGE_REPO$/ {
  expect_otel_repo = 1
  next
}
expect_otel_repo && /^[[:space:]]*value:[[:space:]]*/ {
  otel_repo = $0
  sub(/^[[:space:]]*value:[[:space:]]*/, "", otel_repo)
  otel_repo = trim(otel_repo)
  expect_otel_repo = 0
  if (otel_tag != "") {
    emit(otel_repo, otel_tag)
  }
  next
}
/name:[[:space:]]+OTEL_COLLECTOR_IMAGE_TAG$/ {
  expect_otel_tag = 1
  next
}
expect_otel_tag && /^[[:space:]]*value:[[:space:]]*/ {
  otel_tag = $0
  sub(/^[[:space:]]*value:[[:space:]]*/, "", otel_tag)
  otel_tag = trim(otel_tag)
  expect_otel_tag = 0
  if (otel_repo != "") {
    emit(otel_repo, otel_tag)
  }
  next
}
/nvcaVersion:[[:space:]]*/ {
  nvca_version = $0
  sub(/.*nvcaVersion:[[:space:]]*/, "", nvca_version)
  nvca_version = trim(nvca_version)
  if (nvca_repo != "") {
    emit(nvca_repo, nvca_version)
  }
  next
}
/repository:[[:space:]]*/ || /imageRepository:[[:space:]]*/ {
  pending_repo = $0
  sub(/.*(repository|imageRepository):[[:space:]]*/, "", pending_repo)
  pending_repo = trim(pending_repo)
  next
}
pending_repo != "" && (/tag:[[:space:]]*/ || /imageTag:[[:space:]]*/) {
  pending_tag = $0
  sub(/.*(tag|imageTag):[[:space:]]*/, "", pending_tag)
  pending_tag = trim(pending_tag)
  emit(pending_repo, pending_tag)
  pending_repo = ""
  next
}
END {
  for (image in seen) {
    print image
  }
}
' "${input_manifest}" | sort -u > "${tmp_images}"

if [[ ! -s "${tmp_images}" ]]; then
  cat > "${output_manifest}" <<'EOF'
apiVersion: v1
kind: List
items: []
EOF
  exit 0
fi

{
  echo "apiVersion: v1"
  echo "kind: Pod"
  echo "metadata:"
  echo "  name: nvca-release-supplemental-images"
  echo "spec:"
  echo "  restartPolicy: Never"
  echo "  containers:"
  index=0
  while IFS= read -r image; do
    [[ -z "${image}" ]] && continue
    printf "    - name: image-%02d\n" "${index}"
    printf "      image: %s\n" "${image}"
    index=$((index + 1))
  done < "${tmp_images}"
} > "${output_manifest}"
