#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Verify the nvcf-invocation-service chart version contract:
#   - values.yaml invocation.image.tag matches Chart.yaml appVersion,
#   - the default render pins the Deployment image to that version,
#   - an empty tag still resolves to appVersion through the chart helper.
#
# Assertions read the expected version from Chart.yaml rather than hardcoding
# it, so a routine image bump does not require editing this test.
set -euo pipefail

chart_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../nvcf-invocation-service" && pwd)"
registry="registry.example.test"
repository="nvcf-invocation-service"

app_version="$(yq -r '.appVersion' "${chart_root}/Chart.yaml")"
values_tag="$(yq -r '.invocation.image.tag' "${chart_root}/values.yaml")"

if [[ -z "${app_version}" || "${app_version}" == "null" ]]; then
  echo "FAILED: Chart.yaml appVersion is empty" >&2
  exit 1
fi

if [[ "${values_tag}" != "${app_version}" ]]; then
  echo "FAILED: values.yaml invocation.image.tag (${values_tag}) does not match Chart.yaml appVersion (${app_version})" >&2
  exit 1
fi

render() {
  helm template inv "${chart_root}" \
    --set-string invocation.image.registry="${registry}" \
    --set-string invocation.image.repository="${repository}" \
    "$@"
}

assert_image() {
  local manifest="$1" expected="$2" label="$3" count
  # awk, not grep -F: grep matches substrings, so a tag such as 0.12.0-rc1
  # would still match 0.12.0 and hide a regression.
  count="$(awk -v expected="\"${expected}\"" \
    '$1 == "image:" && $2 == expected { n++ } END { print n + 0 }' "${manifest}")"
  if [[ "${count}" -ne 1 ]]; then
    echo "FAILED: expected exactly 1 ${expected} image in the ${label} render, got ${count}" >&2
    exit 1
  fi
}

manifest="$(mktemp)"
fallback_manifest="$(mktemp)"
trap 'rm -f "${manifest}" "${fallback_manifest}"' EXIT

render > "${manifest}"
assert_image "${manifest}" "${registry}/${repository}:${app_version}" "default"

# helm upgrade --reuse-values carries the previous release's values forward but
# never its Chart.yaml. An empty tag must still resolve through appVersion.
render --set-string invocation.image.tag="" > "${fallback_manifest}"
assert_image "${fallback_manifest}" "${registry}/${repository}:${app_version}" "empty-tag fallback"

echo "image_tag_appversion_test.sh: chart image pins ${app_version} from Chart.yaml appVersion"
