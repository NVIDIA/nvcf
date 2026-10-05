#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
vendored_chart="${repo_root}/nvca-operator"
tmp_dir="$(mktemp -d)"
test_service_key="test-service-key"

cleanup() {
  rm -rf "${tmp_dir}"
}
trap cleanup EXIT

assert_render_fails() {
  local chart="$1"
  shift

  if helm template nvca-operator "${chart}" \
    --set-string ngcConfig.serviceKey="${test_service_key}" \
    "$@" > /dev/null 2>&1; then
    echo "expected PDB render to fail for ${chart}: $*" >&2
    exit 1
  fi
}

for chart in "${vendored_chart}"; do
  chart_name="vendored"
  default_manifest="${tmp_dir}/${chart_name}-default.yaml"
  reused_values_chart="${tmp_dir}/${chart_name}-reused-values"
  reused_values_manifest="${tmp_dir}/${chart_name}-reused-values.yaml"
  min_available_manifest="${tmp_dir}/${chart_name}-min-available.yaml"
  max_unavailable_manifest="${tmp_dir}/${chart_name}-max-unavailable.yaml"

  helm lint "${chart}" --set-string ngcConfig.serviceKey="${test_service_key}"

  helm template nvca-operator "${chart}" \
    --set-string ngcConfig.serviceKey="${test_service_key}" \
    > "${default_manifest}"
  if grep -Fq 'kind: PodDisruptionBudget' "${default_manifest}"; then
    echo "expected PDB to be disabled by default for ${chart}" >&2
    exit 1
  fi

  cp -R "${chart}" "${reused_values_chart}"
  yq -i 'del(.podDisruptionBudget)' "${reused_values_chart}/values.yaml"
  helm template nvca-operator "${reused_values_chart}" \
    --set-string ngcConfig.serviceKey="${test_service_key}" \
    > "${reused_values_manifest}"
  if grep -Fq 'kind: PodDisruptionBudget' "${reused_values_manifest}"; then
    echo "expected PDB to be disabled when reused values omit its configuration for ${chart}" >&2
    exit 1
  fi

  helm template nvca-operator "${chart}" \
    --set-string ngcConfig.serviceKey="${test_service_key}" \
    --set podDisruptionBudget.enabled=true \
    --set podDisruptionBudget.minAvailable=1 \
    > "${min_available_manifest}"
  if ! grep -Fq 'minAvailable: 1' "${min_available_manifest}"; then
    echo "expected minAvailable PDB render for ${chart}" >&2
    exit 1
  fi

  helm template nvca-operator "${chart}" \
    --set-string ngcConfig.serviceKey="${test_service_key}" \
    --set podDisruptionBudget.enabled=true \
    --set-string podDisruptionBudget.maxUnavailable=50% \
    > "${max_unavailable_manifest}"
  if ! grep -Fq 'maxUnavailable: 50%' "${max_unavailable_manifest}"; then
    echo "expected percentage maxUnavailable PDB render for ${chart}" >&2
    exit 1
  fi

  assert_render_fails "${chart}" --set podDisruptionBudget.enabled=true
  assert_render_fails "${chart}" \
    --set podDisruptionBudget.enabled=true \
    --set podDisruptionBudget.minAvailable=1 \
    --set podDisruptionBudget.maxUnavailable=1
  assert_render_fails "${chart}" \
    --set podDisruptionBudget.enabled=true \
    --set-string podDisruptionBudget.minAvailable=101%
done

echo "validated PodDisruptionBudget values for source and vendored charts"
