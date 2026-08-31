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
repository="0651155215864979/ncp-dev"

manifest="${tmp_dir}/manifest.yaml"
manifest_without_sidecar_env="${tmp_dir}/manifest-without-sidecar-env.yaml"

helm template nvcf-api "${chart_dir}" \
  --namespace nvcf \
  --values "${chart_dir}/values.yaml" \
  --set-string api.image.registry=example.com \
  --set-string api.image.repository=foo/bar/strap \
  --set-string api.accountBootstrap.image.registry=example.com \
  --set-string api.accountBootstrap.image.repository=foo/bar/alpine-k8s \
  --set-string api.accountBootstrap.image.tag=0.0.0 \
  --set-string "api.env.NVCF_SIDECARS_HOSTNAME=${hostname}" \
  --set-string "api.env.NVCF_SIDECARS_REPOSITORY=${repository}" \
  --show-only templates/configmap-remote-config.yaml \
  > "${manifest}"

# Every sidecar entry carrying the ${nvcf.sidecars.*} placeholder must surface a
# concrete, placeholder-free release-artifact-*-image annotation resolved from
# NVCF_SIDECARS_HOSTNAME / NVCF_SIDECARS_REPOSITORY.
expected_annotations=(
  "release-artifact-init-container-image: \"${hostname}/${repository}/nvcf-worker-init-oss:1.2.0\""
  "release-artifact-utils-container-image-go-image: \"${hostname}/${repository}/nvcf-worker-utils-oss:1.0.4\""
  "release-artifact-niclls-container-image: \"${hostname}/${repository}/nvcf_worker_niclls:2.109.4\""
  "release-artifact-ess-agent-container-image: \"${hostname}/${repository}/ess-agent:1.4.0\""
  "release-artifact-llm-credential-manager-image: \"${hostname}/${repository}/nvcf-worker-llm-credentials-oss:1.1.1\""
  "release-artifact-llm-router-client-image: \"${hostname}/${repository}/pylon:0.14.1\""
)

for annotation in "${expected_annotations[@]}"; do
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
  --set-string api.accountBootstrap.image.registry=example.com \
  --set-string api.accountBootstrap.image.repository=foo/bar/alpine-k8s \
  --set-string api.accountBootstrap.image.tag=0.0.0 \
  --show-only templates/configmap-remote-config.yaml \
  > "${manifest_without_sidecar_env}"

if grep -Fq "release-artifact-" "${manifest_without_sidecar_env}"; then
  echo "expected no release-artifact annotations when NVCF_SIDECARS_* env is unset" >&2
  exit 1
fi

echo "remote-config ConfigMap surfaces resolved release-artifact image references for placeholder sidecars"
