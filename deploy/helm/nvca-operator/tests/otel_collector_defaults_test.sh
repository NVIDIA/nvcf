#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
source_chart="${repo_root}/../../../src/compute-plane-services/nvca/deployments/nvca-operator"
vendored_chart="${repo_root}/nvca-operator"
tmp_dir="$(mktemp -d)"
test_service_key="test-service-key"

cleanup() {
  rm -rf "${tmp_dir}"
}
trap cleanup EXIT

assert_equal() {
  local expected="$1"
  local actual="$2"
  local description="$3"

  if [[ "${actual}" != "${expected}" ]]; then
    echo "expected ${description} to be ${expected}, got ${actual}" >&2
    exit 1
  fi
}

render_helm_managed() {
  local chart="$1"
  local output="$2"
  shift 2

  helm template nvca-operator "${chart}" \
    --set-string ngcConfig.serviceKey="${test_service_key}" \
    --set-string ngcConfig.clusterSource=helm-managed \
    --set-string clusterID=test-cluster-id \
    --set-string clusterName=test-cluster \
    --set-string ncaID=test-nca \
    --set-string helmManaged.cloudProvider=aws \
    --set-string helmManaged.clusterRegion=us-west-2 \
    --set-string helmManaged.clusterGroupID=test-cluster-group-id \
    --set-string helmManaged.clusterGroupName=test-cluster-group \
    --set-string helmManaged.nvcaVersion=3.0.4 \
    "$@" > "${output}"
}

for chart in "${source_chart}" "${vendored_chart}"; do
  chart_name="$(basename "$(dirname "${chart}")")/$(basename "${chart}")"
  default_manifest="${tmp_dir}/$(basename "$(dirname "${chart}")")-default.yaml"
  disabled_manifest="${tmp_dir}/$(basename "$(dirname "${chart}")")-disabled.yaml"
  helm_managed_manifest="${tmp_dir}/$(basename "$(dirname "${chart}")")-helm-managed.yaml"
  helm_managed_disabled_manifest="${tmp_dir}/$(basename "$(dirname "${chart}")")-helm-managed-disabled.yaml"

  assert_equal true "$(yq -r '.otelCollector.enabled' "${chart}/values.yaml")" \
    "${chart_name} otelCollector.enabled value"
  assert_equal true "$(yq -r '.helmManaged.otelCollector.enabled' "${chart}/values.yaml")" \
    "${chart_name} helmManaged.otelCollector.enabled value"
  assert_equal false "$(yq -r '.selfManaged.otelCollector.enabled' "${chart}/values.yaml")" \
    "${chart_name} selfManaged.otelCollector.enabled value"

  assert_equal true "$(jq -r '.properties.otelCollector.properties.enabled.default' "${chart}/values.schema.json")" \
    "${chart_name} otelCollector.enabled schema default"
  assert_equal true "$(jq -r '.properties.helmManaged.properties.otelCollector.properties.enabled.default' "${chart}/values.schema.json")" \
    "${chart_name} helmManaged.otelCollector.enabled schema default"
  assert_equal false "$(jq -r '.properties.selfManaged.properties.otelCollector.properties.enabled.default' "${chart}/values.schema.json")" \
    "${chart_name} selfManaged.otelCollector.enabled schema default"

  helm template nvca-operator "${chart}" \
    --set-string ngcConfig.serviceKey="${test_service_key}" > "${default_manifest}"
  assert_equal true "$(
    yq -r 'select(.kind == "Deployment" and .metadata.name == "nvca-operator") |
      .spec.template.spec.containers[] | select(.name == "nvca-operator") |
      .env[] | select(.name == "OTEL_COLLECTOR_ENABLED") | .value' \
      "${default_manifest}"
  )" "${chart_name} rendered operator collector default"

  helm template nvca-operator "${chart}" \
    --set-string ngcConfig.serviceKey="${test_service_key}" \
    --set otelCollector.enabled=false > "${disabled_manifest}"
  assert_equal false "$(
    yq -r 'select(.kind == "Deployment" and .metadata.name == "nvca-operator") |
      .spec.template.spec.containers[] | select(.name == "nvca-operator") |
      .env[] | select(.name == "OTEL_COLLECTOR_ENABLED") | .value' \
      "${disabled_manifest}"
  )" "${chart_name} rendered operator collector override"

  render_helm_managed "${chart}" "${helm_managed_manifest}"
  helm_managed_config="$(
    yq -r 'select(.kind == "ConfigMap" and .metadata.name == "nvcfbackend-helm-managed") |
      .data."cluster-dto.yaml"' "${helm_managed_manifest}"
  )"
  assert_equal true "$(printf '%s\n' "${helm_managed_config}" | yq -r '.otelCollector.enabled')" \
    "${chart_name} rendered Helm-managed collector default"

  render_helm_managed "${chart}" "${helm_managed_disabled_manifest}" \
    --set helmManaged.otelCollector.enabled=false
  helm_managed_disabled_config="$(
    yq -r 'select(.kind == "ConfigMap" and .metadata.name == "nvcfbackend-helm-managed") |
      .data."cluster-dto.yaml"' "${helm_managed_disabled_manifest}"
  )"
  assert_equal false "$(printf '%s\n' "${helm_managed_disabled_config}" | yq -r '.otelCollector.enabled')" \
    "${chart_name} rendered Helm-managed collector override"
done

echo "NVCA OTel collector defaults: managed paths enabled, explicit disable honored, self-managed default unchanged"
