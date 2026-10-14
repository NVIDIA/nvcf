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
chart_dir="${chart_root}/nvct-api"
tmp_dir="$(mktemp -d)"

cleanup() {
  rm -rf "${tmp_dir}"
}
trap cleanup EXIT

hostname="nvcr.io"
repository="example-org/example-team"

manifest="${tmp_dir}/manifest.yaml"
manifest_without_sidecar_env="${tmp_dir}/manifest-without-sidecar-env.yaml"

render() {
  helm template nvct-api "${chart_dir}" \
    --namespace nvcf \
    --values "${chart_dir}/values.yaml" \
    --set-string nvctApi.image.registry=example.com \
    --set-string nvctApi.image.repository=foo/bar/nvct \
    "$@" \
    --show-only templates/configmap-remote-config.yaml
}

render \
  --set-string "nvctApi.env.NVCT_SIDECARS_HOSTNAME=${hostname}" \
  --set-string "nvctApi.env.NVCT_SIDECARS_REPOSITORY=${repository}" \
  > "${manifest}"

# Expected references come from the chart defaults so a pin bump does not break
# this test. Every sidecar carrying the ${nvct.sidecars.*} placeholder must
# surface a concrete release-artifact-*-image annotation.
placeholder_sidecars="$(yq -r \
  '.nvctApi.remoteConfig.configData.nvct.sidecars | to_entries[] | select(.value | test("\$\{nvct\.sidecars\.")) | .key + " " + .value' \
  "${chart_dir}/values.yaml")"

if [[ -z "${placeholder_sidecars}" ]]; then
  echo "expected the chart to default at least one placeholder sidecar" >&2
  exit 1
fi

# nvcf_worker_* is the managed worker line, which is never published to the
# public catalog; self-managed task workers must use the -oss builds.
if grep -E '/nvcf_worker_(init|task|utils):' <<< "${placeholder_sidecars}"; then
  echo "nvct-api sidecars must use the open-source worker images (nvcf-worker-*-oss)" >&2
  exit 1
fi

while read -r key value; do
  name="release-artifact-${key}"
  [[ "${name}" == *-image ]] || name="${name}-image"
  resolved="${value//\$\{nvct.sidecars.hostname\}/${hostname}}"
  resolved="${resolved//\$\{nvct.sidecars.repository\}/${repository}}"
  annotation="${name}: \"${resolved}\""
  if ! grep -Fq "${annotation}" "${manifest}"; then
    echo "expected rendered remote-config ConfigMap to include annotation: ${annotation}" >&2
    exit 1
  fi
done <<< "${placeholder_sidecars}"

# Fixed annotation names anchor the naming rule, so a shared mistake in the
# helper and the loop above cannot pass unnoticed.
for name in \
  "release-artifact-init-container-image" \
  "release-artifact-utils-container-image" \
  "release-artifact-ess-agent-container-image"; do
  if ! grep -Fq "${name}: \"${hostname}/${repository}/" "${manifest}"; then
    echo "expected rendered remote-config ConfigMap to include annotation: ${name}" >&2
    exit 1
  fi
done

# Sidecar values without the placeholder prefix are not worker images and must
# not be reported as release artifacts.
for absent in \
  "release-artifact-otel-container" \
  "release-artifact-otel-collector-container"; do
  if grep -Fq "${absent}" "${manifest}"; then
    echo "did not expect release-artifact annotation for non-placeholder sidecar: ${absent}" >&2
    exit 1
  fi
done

if grep -E 'release-artifact-.*\$\{' "${manifest}"; then
  echo "release-artifact annotations must not contain unresolved \${...} placeholders" >&2
  exit 1
fi

# Without the sidecar registry env vars the annotations must be omitted entirely
# rather than emitting partially-resolved (invalid) references.
render > "${manifest_without_sidecar_env}"

if grep -Fq "release-artifact-" "${manifest_without_sidecar_env}"; then
  echo "expected no release-artifact annotations when NVCT_SIDECARS_* env is unset" >&2
  exit 1
fi

echo "remote-config ConfigMap surfaces resolved release-artifact image references for placeholder sidecars"
