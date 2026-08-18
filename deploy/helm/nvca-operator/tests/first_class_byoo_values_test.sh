#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
tmp_dir="$(mktemp -d)"

cleanup() {
  rm -rf "${tmp_dir}"
}
trap cleanup EXIT

default_manifest="${tmp_dir}/default-manifest.yaml"
legacy_manifest="${tmp_dir}/legacy-manifest.yaml"
legacy_values="${tmp_dir}/legacy-values.yaml"

render() {
  local manifest="$1"
  shift

  helm template nvca-operator "${repo_root}/nvca-operator" \
    --namespace nvca-operator \
    --values "${repo_root}/nvca-operator/values.yaml" \
    --values "${repo_root}/values.release-sbom.yaml" \
    --set-string selfManaged.icmsServiceURL=http://icms.example.invalid:8080 \
    --set-string selfManaged.revalServiceURL=http://reval.example.invalid:8080 \
    --set-string selfManaged.natsURL=nats://nats.example.invalid:4222 \
    "$@" \
    > "${manifest}"
}

agent_config_value() {
  local manifest="$1"
  local expression="$2"

  yq -er "select(.kind == \"ConfigMap\" and .metadata.name == \"agent-config-merge\") | .data.\"config.yaml\" | from_yaml | ${expression}" "${manifest}"
}

assert_equal() {
  local expected="$1"
  local actual="$2"
  local message="$3"

  if [[ "${actual}" != "${expected}" ]]; then
    echo "${message}: expected ${expected}, got ${actual}" >&2
    exit 1
  fi
}

render "${default_manifest}"
assert_equal "1000m" "$(agent_config_value "${default_manifest}" '.agent.BYOOResources.limits.cpu')" "unexpected BYOO CPU limit"
assert_equal "4Gi" "$(agent_config_value "${default_manifest}" '.agent.BYOOResources.requests.memory')" "unexpected BYOO memory request"
assert_equal "262144" "$(agent_config_value "${default_manifest}" '.agent.byooLogChunking.maxPayloadBytes')" "unexpected BYOO log chunk payload size"
assert_equal "1000000" "$(agent_config_value "${default_manifest}" '.agent.byooOtelCollector.exporterHelper.sendingQueue.batch.maxSize')" "unexpected BYOO exporter batch limit"
assert_equal "8" "$(agent_config_value "${default_manifest}" '.agent.additionalResourceOverhead.cpu')" "unexpected BYOO capacity reservation"

yq eval '
  .agentConfig.mergeConfig = "agent:\n  BYOOResources:\n    limits:\n      cpu: 1500m\n  byooLogChunking:\n    maxPayloadBytes: 131072\ncluster:\n  validationPolicy:\n    name: Unrestricted\n    allowedExtraKubernetesTypes:\n      - group: nvidia.com\n        kind: DynamoGraphDeployment\n        resource: dynamographdeployments\n        version: v1alpha1" |
  .agentConfig.mergeConfig style="literal"
' "${repo_root}/nvca-operator/values.yaml" > "${legacy_values}"

helm template nvca-operator "${repo_root}/nvca-operator" \
  --namespace nvca-operator \
  --values "${legacy_values}" \
  --values "${repo_root}/values.release-sbom.yaml" \
  --set-string selfManaged.icmsServiceURL=http://icms.example.invalid:8080 \
  --set-string selfManaged.revalServiceURL=http://reval.example.invalid:8080 \
  --set-string selfManaged.natsURL=nats://nats.example.invalid:4222 \
  > "${legacy_manifest}"

assert_equal "1500m" "$(agent_config_value "${legacy_manifest}" '.agent.BYOOResources.limits.cpu')" "legacy BYOO resources did not override the chart default"
assert_equal "131072" "$(agent_config_value "${legacy_manifest}" '.agent.byooLogChunking.maxPayloadBytes')" "legacy BYOO chunking did not override the chart default"
assert_equal "dynamographdeployments" "$(agent_config_value "${legacy_manifest}" '.cluster.validationPolicy.allowedExtraKubernetesTypes[0].resource')" "legacy validation policy was dropped"
assert_equal "true" "$(yq -er 'select(.kind == "ConfigMap" and .metadata.name == "agent-config-merge") | .metadata.annotations."nvcf.nvidia.com/legacy-byoo-config"' "${legacy_manifest}")" "legacy BYOO config was not annotated"
