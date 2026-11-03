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

fail() {
  echo "$*" >&2
  exit 1
}

hostname="nvcr.io"
repository="example-org/example-team"
prefix='${nvct.sidecars.hostname}/${nvct.sidecars.repository}'

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

render_with_sidecar_env() {
  render \
    --set-string "nvctApi.env.NVCT_SIDECARS_HOSTNAME=${hostname}" \
    --set-string "nvctApi.env.NVCT_SIDECARS_REPOSITORY=${repository}" \
    "$@"
}

# Read one value out of the rendered nvct-api.yaml remote config.
remote_config() {
  yq -r '.data."nvct-api.yaml"' "$1" | yq -r "$2"
}

assert_remote_config() {
  local manifest_file="$1" expression="$2" want="$3" label="$4" got
  got="$(remote_config "${manifest_file}" "${expression}")"
  [[ "${got}" == "${want}" ]] ||
    fail "${label}: expected ${expression} to be ${want}, got ${got}"
}

# Expected references come from the chart defaults so a pin bump does not break
# this test.
worker_tag() {
  local tag
  tag="$(yq -r ".nvctApi.remoteConfig.workerImageTags.$1 // \"\"" "${chart_dir}/values.yaml")"
  [[ -n "${tag}" ]] ||
    fail "the chart no longer defaults nvctApi.remoteConfig.workerImageTags.$1, update this test"
  printf '%s' "${tag}"
}

init_tag="$(worker_tag init)"
task_tag="$(worker_tag task)"

render_with_sidecar_env > "${manifest}"

# Worker references are composed from nvctApi.remoteConfig.workerImageTags and
# keep the Spring placeholders, which the service resolves at runtime.
assert_remote_config "${manifest}" '.nvct.sidecars."init-container"' \
  "${prefix}/nvcf-worker-init-oss:${init_tag}" "composed init reference"
assert_remote_config "${manifest}" '.nvct.sidecars."utils-container"' \
  "${prefix}/nvcf-worker-task-oss:${task_tag}" "composed task reference"

# Every sidecar in the rendered remote config that carries the
# ${nvct.sidecars.*} placeholder must surface a concrete
# release-artifact-*-image annotation.
placeholder_sidecars="$(remote_config "${manifest}" \
  '.nvct.sidecars | to_entries[] | select((.value | type) == "!!str") | select(.value | test("\$\{nvct\.sidecars\.")) | .key + " " + .value')"

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

# A tag override moves only its own worker reference and its annotation.
tag_override_manifest="${tmp_dir}/manifest-tag-override.yaml"
render_with_sidecar_env \
  --set-string nvctApi.remoteConfig.workerImageTags.task=0.0.1 \
  > "${tag_override_manifest}"
assert_remote_config "${tag_override_manifest}" '.nvct.sidecars."utils-container"' \
  "${prefix}/nvcf-worker-task-oss:0.0.1" "task tag override"
assert_remote_config "${tag_override_manifest}" '.nvct.sidecars."init-container"' \
  "${prefix}/nvcf-worker-init-oss:${init_tag}" "task tag override leaves init"
grep -Fq "release-artifact-utils-container-image: \"${hostname}/${repository}/nvcf-worker-task-oss:0.0.1\"" \
  "${tag_override_manifest}" ||
  fail "the task release-artifact annotation did not follow the tag override"

# An explicit configData entry still replaces a whole worker reference.
reference_override_manifest="${tmp_dir}/manifest-reference-override.yaml"
render_with_sidecar_env \
  --set-string 'nvctApi.remoteConfig.configData.nvct.sidecars.utils-container=mirror.example.test/team/task:9.9.9' \
  > "${reference_override_manifest}"
assert_remote_config "${reference_override_manifest}" '.nvct.sidecars."utils-container"' \
  "mirror.example.test/team/task:9.9.9" "configData task override"
assert_remote_config "${reference_override_manifest}" '.nvct.sidecars."init-container"' \
  "${prefix}/nvcf-worker-init-oss:${init_tag}" "configData task override leaves init"
if grep -Fq "release-artifact-utils-container-image" "${reference_override_manifest}"; then
  fail "a placeholder-free utils-container override must not be reported as a release artifact"
fi

# An empty worker tag would render an invalid reference, so the render fails.
if render --set-string nvctApi.remoteConfig.workerImageTags.task= \
  > "${tmp_dir}/empty-tag.yaml" 2> "${tmp_dir}/empty-tag.log"; then
  fail "an empty nvctApi.remoteConfig.workerImageTags.task was accepted"
fi
grep -Fq "nvctApi.remoteConfig.workerImageTags.task is required" "${tmp_dir}/empty-tag.log" ||
  fail "an empty worker tag did not name the missing key"

# helm upgrade --reuse-values renders these templates against the values stored
# with the previous release. Values from before workerImageTags existed have no
# tags but carry every worker reference in configData, so they must still
# render and keep those references. The null drops the chart default tags.
stored_values="${tmp_dir}/stored-values.yaml"
cat > "${stored_values}" <<EOF
nvctApi:
  remoteConfig:
    workerImageTags: null
    configData:
      nvct:
        sidecars:
          init-container: "${prefix}/nvcf-worker-init-oss:0.9.1"
          utils-container: "${prefix}/nvcf-worker-task-oss:0.9.2"
EOF
stored_manifest="${tmp_dir}/manifest-stored-values.yaml"
render_with_sidecar_env --values "${stored_values}" > "${stored_manifest}" ||
  fail "values stored before workerImageTags existed no longer render"
assert_remote_config "${stored_manifest}" '.nvct.sidecars."init-container"' \
  "${prefix}/nvcf-worker-init-oss:0.9.1" "stored init reference"
assert_remote_config "${stored_manifest}" '.nvct.sidecars."utils-container"' \
  "${prefix}/nvcf-worker-task-oss:0.9.2" "stored task reference"
grep -Fq "release-artifact-utils-container-image: \"${hostname}/${repository}/nvcf-worker-task-oss:0.9.2\"" \
  "${stored_manifest}" ||
  fail "the task release-artifact annotation did not follow the stored reference"

# A tag stays required for a worker whose reference configData does not set.
if render --set nvctApi.remoteConfig.workerImageTags=null \
  > "${tmp_dir}/no-tags.yaml" 2> "${tmp_dir}/no-tags.log"; then
  fail "missing worker tags were accepted without stored worker references"
fi
grep -Fq "nvctApi.remoteConfig.workerImageTags.init is required" "${tmp_dir}/no-tags.log" ||
  fail "missing worker tags did not name the missing key"

# Without the sidecar registry env vars the annotations must be omitted entirely
# rather than emitting partially-resolved (invalid) references.
render > "${manifest_without_sidecar_env}"

if grep -Fq "release-artifact-" "${manifest_without_sidecar_env}"; then
  echo "expected no release-artifact annotations when NVCT_SIDECARS_* env is unset" >&2
  exit 1
fi

echo "remote-config ConfigMap composes worker references and surfaces resolved release-artifact image references for placeholder sidecars"
