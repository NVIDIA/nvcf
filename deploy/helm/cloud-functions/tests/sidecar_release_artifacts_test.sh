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
values_file="${chart_dir}/values.yaml"
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
prefix='${nvcf.sidecars.hostname}/${nvcf.sidecars.repository}'

manifest="${tmp_dir}/manifest.yaml"
manifest_without_sidecar_env="${tmp_dir}/manifest-without-sidecar-env.yaml"

render() {
  helm template nvcf-api "${chart_dir}" \
    --namespace nvcf \
    --values "${values_file}" \
    --set-string api.image.registry=example.com \
    --set-string api.image.repository=foo/bar/strap \
    "$@" \
    --show-only templates/configmap-remote-config.yaml
}

render_with_sidecar_env() {
  render \
    --set-string "api.env.NVCF_SIDECARS_HOSTNAME=${hostname}" \
    --set-string "api.env.NVCF_SIDECARS_REPOSITORY=${repository}" \
    "$@"
}

# Read one value out of the rendered nvcf-api.yaml remote config.
remote_config() {
  yq -r '.data."nvcf-api.yaml"' "$1" | yq -r "$2"
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
  tag="$(yq -r ".api.remoteConfig.workerImageTags.$1 // \"\"" "${values_file}")"
  [[ -n "${tag}" ]] ||
    fail "the chart no longer defaults api.remoteConfig.workerImageTags.$1, update this test"
  printf '%s' "${tag}"
}

init_tag="$(worker_tag init)"
utils_tag="$(worker_tag utils)"
llm_credentials_tag="$(worker_tag llmCredentials)"

render_with_sidecar_env > "${manifest}"

# Worker references are composed from api.remoteConfig.workerImageTags and keep
# the Spring placeholders, which the API resolves at runtime.
assert_remote_config "${manifest}" '.nvcf.sidecars."init-container"' \
  "${prefix}/nvcf-worker-init-oss:${init_tag}" "composed init reference"
assert_remote_config "${manifest}" '.nvcf.sidecars."utils-container-image".go' \
  "${prefix}/nvcf-worker-utils-oss:${utils_tag}" "composed utils reference"
assert_remote_config "${manifest}" '.nvcf.sidecars."llm-credential-manager-image"' \
  "${prefix}/nvcf-worker-llm-credentials-oss:${llm_credentials_tag}" "composed llm-credentials reference"

# Every sidecar entry carrying the ${nvcf.sidecars.*} placeholder must surface a
# concrete, placeholder-free release-artifact-*-image annotation resolved from
# NVCF_SIDECARS_HOSTNAME / NVCF_SIDECARS_REPOSITORY.
expected_annotations=(
  "release-artifact-init-container-image: \"${hostname}/${repository}/nvcf-worker-init-oss:${init_tag}\""
  "release-artifact-utils-container-image-go-image: \"${hostname}/${repository}/nvcf-worker-utils-oss:${utils_tag}\""
  "release-artifact-llm-credential-manager-image: \"${hostname}/${repository}/nvcf-worker-llm-credentials-oss:${llm_credentials_tag}\""
)

# The other placeholder sidecars are listed in configData directly.
placeholder_sidecars="$(yq -r \
  '.api.remoteConfig.configData.nvcf.sidecars | to_entries[] | select((.value | type) == "!!str") | select(.value | test("\$\{nvcf\.sidecars\.")) | .key + " " + .value' \
  "${values_file}")"

while read -r key value; do
  [[ -n "${key}" ]] || continue
  name="release-artifact-${key}"
  [[ "${name}" == *-image ]] || name="${name}-image"
  resolved="${value//\$\{nvcf.sidecars.hostname\}/${hostname}}"
  resolved="${resolved//\$\{nvcf.sidecars.repository\}/${repository}}"
  expected_annotations+=("${name}: \"${resolved}\"")
done <<< "${placeholder_sidecars}"

for annotation in "${expected_annotations[@]}"; do
  if ! grep -Fq "${annotation}" "${manifest}"; then
    fail "expected rendered remote-config ConfigMap to include annotation: ${annotation}"
  fi
done

# Fixed annotation names anchor the naming rule, so a shared mistake in the
# helper and the loop above cannot pass unnoticed.
for name in \
  "release-artifact-niclls-container-image" \
  "release-artifact-ess-agent-container-image" \
  "release-artifact-llm-router-client-image"; do
  if ! grep -Fq "${name}: \"${hostname}/${repository}/" "${manifest}"; then
    fail "expected rendered remote-config ConfigMap to include annotation: ${name}"
  fi
done

# Sidecar values without the placeholder prefix are pinned by digest/tag elsewhere
# and must not be duplicated as release-artifact annotations.
for absent in \
  "release-artifact-inference-container" \
  "release-artifact-otel-container" \
  "release-artifact-otel-collector-container"; do
  if grep -Fq "${absent}" "${manifest}"; then
    fail "did not expect release-artifact annotation for non-placeholder sidecar: ${absent}"
  fi
done

# No annotation may leak an unresolved placeholder into the release artifacts.
if grep -E 'release-artifact-.*\$\{' "${manifest}"; then
  fail "release-artifact annotations must not contain unresolved \${...} placeholders"
fi

# A tag override moves only its own worker reference and its annotation.
tag_override_manifest="${tmp_dir}/manifest-tag-override.yaml"
render_with_sidecar_env \
  --set-string api.remoteConfig.workerImageTags.utils=0.0.1 \
  > "${tag_override_manifest}"
assert_remote_config "${tag_override_manifest}" '.nvcf.sidecars."utils-container-image".go' \
  "${prefix}/nvcf-worker-utils-oss:0.0.1" "utils tag override"
assert_remote_config "${tag_override_manifest}" '.nvcf.sidecars."init-container"' \
  "${prefix}/nvcf-worker-init-oss:${init_tag}" "utils tag override leaves init"
grep -Fq "release-artifact-utils-container-image-go-image: \"${hostname}/${repository}/nvcf-worker-utils-oss:0.0.1\"" \
  "${tag_override_manifest}" ||
  fail "the utils release-artifact annotation did not follow the tag override"

# An explicit configData entry still replaces a whole worker reference, and a
# sibling entry under the same parent keeps the composed default.
reference_override_manifest="${tmp_dir}/manifest-reference-override.yaml"
render_with_sidecar_env \
  --set-string 'api.remoteConfig.configData.nvcf.sidecars.init-container=mirror.example.test/team/init:9.9.9' \
  --set-string 'api.remoteConfig.configData.nvcf.sidecars.utils-container-image.python=mirror.example.test/team/utils-python:9.9.9' \
  > "${reference_override_manifest}"
assert_remote_config "${reference_override_manifest}" '.nvcf.sidecars."init-container"' \
  "mirror.example.test/team/init:9.9.9" "configData init override"
assert_remote_config "${reference_override_manifest}" '.nvcf.sidecars."utils-container-image".python' \
  "mirror.example.test/team/utils-python:9.9.9" "configData utils sibling"
assert_remote_config "${reference_override_manifest}" '.nvcf.sidecars."utils-container-image".go' \
  "${prefix}/nvcf-worker-utils-oss:${utils_tag}" "configData utils sibling keeps the composed reference"
if grep -Fq "release-artifact-init-container-image" "${reference_override_manifest}"; then
  fail "a placeholder-free init-container override must not be reported as a release artifact"
fi

# An empty worker tag would render an invalid reference, so the render fails.
if render --set-string api.remoteConfig.workerImageTags.init= \
  > "${tmp_dir}/empty-tag.yaml" 2> "${tmp_dir}/empty-tag.log"; then
  fail "an empty api.remoteConfig.workerImageTags.init was accepted"
fi
grep -Fq "api.remoteConfig.workerImageTags.init is required" "${tmp_dir}/empty-tag.log" ||
  fail "an empty worker tag did not name the missing key"

# Without the sidecar registry env vars the annotations must be omitted entirely
# rather than emitting partially-resolved (invalid) references.
render > "${manifest_without_sidecar_env}"

if grep -Fq "release-artifact-" "${manifest_without_sidecar_env}"; then
  fail "expected no release-artifact annotations when NVCF_SIDECARS_* env is unset"
fi

echo "remote-config ConfigMap composes worker references and surfaces resolved release-artifact image references for placeholder sidecars"
