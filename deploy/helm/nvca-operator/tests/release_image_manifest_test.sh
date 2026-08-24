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

rendered_manifest="${tmp_dir}/rendered.yaml"
supplemental_manifest="${tmp_dir}/supplemental.yaml"
image_manifest="${tmp_dir}/artifacts.txt"

cat > "${rendered_manifest}" <<'EOF'
apiVersion: apps/v1
kind: Deployment
metadata:
  name: nvca-operator
spec:
  template:
    spec:
      containers:
        - name: operator
          image: nvcr.io/nvidia/nvcf-byoc/nvca-operator:2.52.0-rc.5
          env:
            - name: NVCA_IMAGE_REPO
              value: "nvcr.io/nvidia/nvcf-byoc/nvca"
          args:
            - --function-env-overrides-b64
            - "eyJCWU9PX09URUxfQ09MTEVDVE9SX0NPTlRBSU5FUiI6Im52Y3IuaW8vbnZpZGlhL252Y2YtYnlvYy9ieW9vLW90ZWwtY29sbGVjdG9yOjAuMTU3LjAtbnYtMC4xLjEifQ=="
            - --task-env-overrides-b64
            - "eyJCWU9PX09URUxfQ09MTEVDVE9SX0NPTlRBSU5FUiI6Im52Y3IuaW8vbnZpZGlhL252Y2YtYnlvYy9ieW9vLW90ZWwtY29sbGVjdG9yOjAuMTU3LjAtbnYtMC4xLjEifQ=="
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: nvcfbackend-self-managed
data:
  cluster-dto.yaml: |
    nvcaVersion: "2.52.0-rc.5"
    imageCredentialHelper:
      imageConfig:
        repository: "nvcr.io/nvidia/nvcf-byoc/nvcf-image-credential-helper"
        tag: "0.5.0"
    sharedStorage:
      imageRepository: "nvcr.io/nvidia/nvcf-byoc/samba"
      imageTag: "1.0.5"
    otelCollector:
      enabled: false
      imageConfig:
        repository: "nvcr.io/nvidia/nvcf-byoc/nvcf-otel-collector"
        tag: "0.157.9"
    placeholder:
      imageRepository: ""
      imageTag: "ignored"
    duplicate:
      imageConfig:
        repository: "nvcr.io/nvidia/nvcf-byoc/nvcf-image-credential-helper"
        tag: "0.5.0"
EOF

"${repo_root}/scripts/render_release_supplemental_images.sh" \
  "${rendered_manifest}" \
  "${supplemental_manifest}"

"${repo_root}/scripts/build_release_image_manifest.sh" \
  "${rendered_manifest}" \
  "${supplemental_manifest}" \
  "${image_manifest}"

expected_images=(
  "nvcr.io/nvidia/nvcf-byoc/nvca-operator:2.52.0-rc.5"
  "nvcr.io/nvidia/nvcf-byoc/nvca:2.52.0-rc.5"
  "nvcr.io/nvidia/nvcf-byoc/nvcf-image-credential-helper:0.5.0"
  "nvcr.io/nvidia/nvcf-byoc/nvcf-otel-collector:0.157.9"
  "nvcr.io/nvidia/nvcf-byoc/samba:1.0.5"
  "nvcr.io/nvidia/nvcf-byoc/byoo-otel-collector:0.157.0-nv-0.1.1"
)

for image in "${expected_images[@]}"; do
  if ! grep -Fxq "${image}" "${image_manifest}"; then
    echo "expected image manifest to include ${image}" >&2
    exit 1
  fi
done

line_count="$(wc -l < "${image_manifest}" | tr -d '[:space:]')"
if [[ "${line_count}" != "6" ]]; then
  echo "expected exactly 6 unique images, got ${line_count}" >&2
  cat "${image_manifest}" >&2
  exit 1
fi

if ! grep -Fq "nvca-release-supplemental-images" "${supplemental_manifest}"; then
  echo "expected supplemental manifest to contain the synthetic Pod" >&2
  exit 1
fi

invalid_manifest="${tmp_dir}/invalid-rendered.yaml"
invalid_supplemental_manifest="${tmp_dir}/invalid-supplemental.yaml"
invalid_output="${tmp_dir}/invalid-output.log"

cat > "${invalid_manifest}" <<'EOF'
apiVersion: apps/v1
kind: Deployment
metadata:
  name: nvca-operator
spec:
  template:
    spec:
      containers:
        - name: operator
          args:
            - --function-env-overrides-b64
            - "eyJCWU9PX09URUxfQ09MTEVDVE9SX0NPTlRBSU5FUiI6InJlcG8vaW1hZ2U6In0="
EOF

if "${repo_root}/scripts/render_release_supplemental_images.sh" \
  "${invalid_manifest}" \
  "${invalid_supplemental_manifest}" \
  >"${invalid_output}" 2>&1; then
  echo "expected invalid BYOO collector image reference to fail" >&2
  exit 1
fi

if ! grep -Fq "invalid BYOO_OTEL_COLLECTOR_CONTAINER image reference" "${invalid_output}"; then
  echo "expected invalid image reference error" >&2
  cat "${invalid_output}" >&2
  exit 1
fi

echo "validated image manifest:"
cat "${image_manifest}"

echo "release image manifest generation captures direct and split image references"
