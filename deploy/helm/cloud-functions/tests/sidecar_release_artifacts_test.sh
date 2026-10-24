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

chart_root="$(cd "$(dirname "$0")/.." && pwd)"
chart_dir="${chart_root}/nvcf-api"
tmp_dir="$(mktemp -d)"

cleanup() {
  rm -rf "${tmp_dir}"
}
trap cleanup EXIT

hostname="nvcr.io"
repository="example-org/example-team"

manifest="${tmp_dir}/manifest.yaml"
manifest_without_sidecar_env="${tmp_dir}/manifest-without-sidecar-env.yaml"

helm template nvcf-api "${chart_dir}" \
  --namespace nvcf \
  --values "${chart_dir}/values.yaml" \
  --set-string api.image.registry=example.com \
  --set-string api.image.repository=foo/bar/strap \
  --set-string "api.env.NVCF_SIDECARS_HOSTNAME=${hostname}" \
  --set-string "api.env.NVCF_SIDECARS_REPOSITORY=${repository}" \
  --show-only templates/configmap-remote-config.yaml \
  > "${manifest}"

# Every sidecar entry carrying the ${nvcf.sidecars.*} placeholder must surface a
# concrete, placeholder-free release-artifact-*-image annotation resolved from
# NVCF_SIDECARS_HOSTNAME / NVCF_SIDECARS_REPOSITORY.
sidecar_paths=(
  'init-container-image|."init-container"'
  'utils-container-image-go-image|."utils-container-image".go'
  'niclls-container-image|."niclls-container"'
  'ess-agent-container-image|."ess-agent-container"'
  'llm-credential-manager-image|."llm-credential-manager-image"'
  'llm-router-client-image|."llm-router-client-image"'
)

# Read expected image versions from chart values, keeping annotation names fixed
# so unrelated image pin changes do not weaken the reference-resolution check.
for sidecar_path in "${sidecar_paths[@]}"; do
  name="${sidecar_path%%|*}"
  expression="${sidecar_path#*|}"
  value="$(yq -r ".api.remoteConfig.configData.nvcf.sidecars${expression}" "${chart_dir}/values.yaml")"
  resolved="${value//\$\{nvcf.sidecars.hostname\}/${hostname}}"
  resolved="${resolved//\$\{nvcf.sidecars.repository\}/${repository}}"
  annotation="release-artifact-${name}: \"${resolved}\""
  if ! grep -Fq "${annotation}" "${manifest}"; then
    echo "expected rendered remote-config ConfigMap to include annotation: ${annotation}" >&2
    exit 1
  fi
done

# Sidecar values without the placeholder prefix are pinned by digest/tag elsewhere
# and must not be duplicated as release-artifact annotations.
for absent in \
  "release-artifact-inference-container" \
  "release-artifact-otel-container" \
  "release-artifact-otel-collector-container"; do
  if grep -Fq "${absent}" "${manifest}"; then
    echo "did not expect release-artifact annotation for non-placeholder sidecar: ${absent}" >&2
    exit 1
  fi
done

# No annotation may leak an unresolved placeholder into the release artifacts.
if grep -E 'release-artifact-.*\$\{' "${manifest}"; then
  echo "release-artifact annotations must not contain unresolved \${...} placeholders" >&2
  exit 1
fi

# Without the sidecar registry env vars the annotations must be omitted entirely
# rather than emitting partially-resolved (invalid) references.
helm template nvcf-api "${chart_dir}" \
  --namespace nvcf \
  --values "${chart_dir}/values.yaml" \
  --set-string api.image.registry=example.com \
  --set-string api.image.repository=foo/bar/strap \
  --show-only templates/configmap-remote-config.yaml \
  > "${manifest_without_sidecar_env}"

if grep -Fq "release-artifact-" "${manifest_without_sidecar_env}"; then
  echo "expected no release-artifact annotations when NVCF_SIDECARS_* env is unset" >&2
  exit 1
fi

echo "remote-config ConfigMap surfaces resolved release-artifact image references for placeholder sidecars"
