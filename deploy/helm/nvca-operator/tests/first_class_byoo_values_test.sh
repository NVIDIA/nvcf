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
    --set-string "ngcConfig.serviceKey=test-service-key" \
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

assert_absent() {
  local manifest="$1"
  local expression="$2"
  local message="$3"

  if agent_config_value "${manifest}" "${expression}" >/dev/null 2>&1; then
    echo "${message}: expected field to be absent, but it was present" >&2
    exit 1
  fi
}

# byoo.resources/otelCollector/additionalResourceOverhead/fluentbit.resources default to {}
# so a no-op chart upgrade does not change the agent's built-in resource defaults; only
# logChunking and utils.resources ship non-empty chart defaults.
render "${default_manifest}"
assert_absent "${default_manifest}" '.agent.BYOOResources' "BYOOResources should be unset by default"
assert_absent "${default_manifest}" '.agent.byooOtelCollector' "byooOtelCollector should be unset by default"
assert_absent "${default_manifest}" '.agent.additionalResourceOverhead' "additionalResourceOverhead should be unset by default"
assert_absent "${default_manifest}" '.agent.BYOOFluentBitResources' "BYOOFluentBitResources should be unset by default"
assert_equal "0" "$(agent_config_value "${default_manifest}" '.agent.byooLogChunking.maxPayloadBytes')" "unexpected default BYOO log chunk payload size"
assert_equal "false" "$(agent_config_value "${default_manifest}" '.agent.byooLogChunking.dryRun')" "unexpected default BYOO log chunk dry-run"
assert_equal "4" "$(agent_config_value "${default_manifest}" '.agent.UtilsResources.cpu')" "unexpected utils CPU sizing"
assert_equal "4Gi" "$(agent_config_value "${default_manifest}" '.agent.UtilsResources.memory')" "unexpected utils memory sizing"

explicit_values="${tmp_dir}/explicit-values.yaml"
cat > "${explicit_values}" <<'EOF'
byoo:
  resources:
    limits:
      cpu: 1000m
      memory: 4Gi
    requests:
      cpu: 1000m
      memory: 4Gi
  logChunking:
    maxPayloadBytes: 262144
    dryRun: false
  additionalResourceOverhead:
    cpu: "8"
  fluentbit:
    resources:
      limits:
        cpu: 500m
        memory: 512Mi
      requests:
        cpu: 100m
        memory: 128Mi
EOF
explicit_manifest="${tmp_dir}/explicit-manifest.yaml"
render "${explicit_manifest}" --values "${explicit_values}"
assert_equal "1000m" "$(agent_config_value "${explicit_manifest}" '.agent.BYOOResources.limits.cpu')" "unexpected explicit BYOO CPU limit"
assert_equal "4Gi" "$(agent_config_value "${explicit_manifest}" '.agent.BYOOResources.requests.memory')" "unexpected explicit BYOO memory request"
assert_equal "262144" "$(agent_config_value "${explicit_manifest}" '.agent.byooLogChunking.maxPayloadBytes')" "unexpected explicit BYOO log chunk payload size"
assert_equal "8" "$(agent_config_value "${explicit_manifest}" '.agent.additionalResourceOverhead.cpu')" "unexpected explicit BYOO capacity reservation"
assert_equal "500m" "$(agent_config_value "${explicit_manifest}" '.agent.BYOOFluentBitResources.limits.cpu')" "unexpected explicit BYOO FluentBit CPU limit"
assert_equal "128Mi" "$(agent_config_value "${explicit_manifest}" '.agent.BYOOFluentBitResources.requests.memory')" "unexpected explicit BYOO FluentBit memory request"

yq eval '
  .agentConfig.mergeConfig = "agent:\n  BYOOResources:\n    limits:\n      cpu: 1500m\n  byooLogChunking:\n    maxPayloadBytes: 131072\n  UtilsResources:\n    cpu: \"6\"\ncluster:\n  validationPolicy:\n    name: Unrestricted\n    allowedExtraKubernetesTypes:\n      - group: nvidia.com\n        kind: DynamoGraphDeployment\n        resource: dynamographdeployments\n        version: v1alpha1" |
  .agentConfig.mergeConfig style="literal"
' "${repo_root}/nvca-operator/values.yaml" > "${legacy_values}"

helm template nvca-operator "${repo_root}/nvca-operator" \
  --set-string "ngcConfig.serviceKey=test-service-key" \
  --namespace nvca-operator \
  --values "${legacy_values}" \
  --values "${repo_root}/values.release-sbom.yaml" \
  --set-string selfManaged.icmsServiceURL=http://icms.example.invalid:8080 \
  --set-string selfManaged.revalServiceURL=http://reval.example.invalid:8080 \
  --set-string selfManaged.natsURL=nats://nats.example.invalid:4222 \
  > "${legacy_manifest}"

assert_equal "1500m" "$(agent_config_value "${legacy_manifest}" '.agent.BYOOResources.limits.cpu')" "legacy BYOO resources did not override the chart default"
assert_equal "131072" "$(agent_config_value "${legacy_manifest}" '.agent.byooLogChunking.maxPayloadBytes')" "legacy BYOO chunking did not override the chart default"
assert_equal "6" "$(agent_config_value "${legacy_manifest}" '.agent.UtilsResources.cpu')" "legacy utils resources did not override the chart default"
assert_equal "dynamographdeployments" "$(agent_config_value "${legacy_manifest}" '.cluster.validationPolicy.allowedExtraKubernetesTypes[0].resource')" "legacy validation policy was dropped"
assert_equal "true" "$(yq -er 'select(.kind == "ConfigMap" and .metadata.name == "agent-config-merge") | .metadata.annotations."nvcf.nvidia.com/legacy-first-class-config"' "${legacy_manifest}")" "legacy BYOO config was not annotated"

# NVCA reads config keys case-insensitively. A legacy mergeConfig key that
# differs from a chart-generated key only by case must replace the chart value
# under the chart spelling instead of rendering a second spelling.
assert_render_fails() {
  local expected="$1"
  shift
  local output="${tmp_dir}/render-error.txt"

  if render "${tmp_dir}/unused-manifest.yaml" "$@" 2> "${output}"; then
    echo "expected render to fail with: ${expected}" >&2
    exit 1
  fi
  if ! grep -qF -- "${expected}" "${output}"; then
    echo "render failed without the expected message: ${expected}" >&2
    cat "${output}" >&2
    exit 1
  fi
}

agent_key_spellings() {
  local manifest="$1"
  local folded_key="$2"

  agent_config_value "${manifest}" ".agent | keys | map(select(downcase == \"${folded_key}\")) | join(\",\")"
}

legacy_case_values="${tmp_dir}/legacy-case-values.yaml"
cat > "${legacy_case_values}" <<'EOF_VALUES'
agentConfig:
  mergeConfig: |
    agent:
      BYOOLogChunking:
        dryRun: false
        exporterBatchMaxSizeBytes: 1000000
        maxBodyBytes: 262144
EOF_VALUES
legacy_case_manifest="${tmp_dir}/legacy-case-manifest.yaml"
render "${legacy_case_manifest}" --values "${legacy_case_values}"
assert_equal "byooLogChunking" "$(agent_key_spellings "${legacy_case_manifest}" "byoologchunking")" "legacy BYOOLogChunking should render once under the chart spelling"
assert_equal "262144" "$(agent_config_value "${legacy_case_manifest}" '.agent.byooLogChunking.maxBodyBytes')" "legacy BYOO log chunk size was dropped"
assert_equal "0" "$(agent_config_value "${legacy_case_manifest}" '.agent.byooLogChunking.maxPayloadBytes')" "chart default BYOO log chunk payload size was dropped"
assert_equal "1000000" "$(agent_config_value "${legacy_case_manifest}" '.agent.byooLogChunking.exporterBatchMaxSizeBytes')" "legacy BYOO log chunk field was dropped"

nested_case_values="${tmp_dir}/nested-case-values.yaml"
cat > "${nested_case_values}" <<'EOF_VALUES'
agentConfig:
  mergeConfig: |
    agent:
      BYOOLogChunking:
        MaxPayloadBytes: 131072
      byooResources:
        limits:
          cpu: 1500m
EOF_VALUES
nested_case_manifest="${tmp_dir}/nested-case-manifest.yaml"
render "${nested_case_manifest}" --values "${explicit_values}" --values "${nested_case_values}"
assert_equal "byooLogChunking" "$(agent_key_spellings "${nested_case_manifest}" "byoologchunking")" "legacy BYOOLogChunking should render once under the chart spelling"
assert_equal "131072" "$(agent_config_value "${nested_case_manifest}" '.agent.byooLogChunking.maxPayloadBytes')" "nested legacy spelling did not override the chart value"
assert_absent "${nested_case_manifest}" '.agent.byooLogChunking.MaxPayloadBytes' "nested legacy spelling should take the chart spelling"
assert_equal "BYOOResources" "$(agent_key_spellings "${nested_case_manifest}" "byooresources")" "legacy byooResources should render once under the chart spelling"
assert_equal "1500m" "$(agent_config_value "${nested_case_manifest}" '.agent.BYOOResources.limits.cpu')" "legacy byooResources did not override the chart value"
assert_equal "4Gi" "$(agent_config_value "${nested_case_manifest}" '.agent.BYOOResources.requests.memory')" "chart BYOO resources were not merged with the legacy override"

both_spellings_values="${tmp_dir}/both-spellings-values.yaml"
cat > "${both_spellings_values}" <<'EOF_VALUES'
agentConfig:
  mergeConfig: |
    agent:
      BYOOLogChunking:
        maxBodyBytes: 262144
      byooLogChunking:
        maxPayloadBytes: 0
EOF_VALUES
assert_render_fails 'agentConfig.mergeConfig: keys "BYOOLogChunking" and "byooLogChunking" under "agent" differ only by case; keep one spelling' \
  --values "${both_spellings_values}"

list_item_values="${tmp_dir}/list-item-values.yaml"
cat > "${list_item_values}" <<'EOF_VALUES'
agentConfig:
  mergeConfig: |
    cluster:
      validationPolicy:
        allowedExtraKubernetesTypes:
          - group: nvidia.com
            kind: DynamoGraphDeployment
            Kind: DynamoGraphDeployment
EOF_VALUES
assert_render_fails 'agentConfig.mergeConfig: keys "Kind" and "kind" under "cluster.validationPolicy.allowedExtraKubernetesTypes[0]" differ only by case' \
  --values "${list_item_values}"

first_class_values="${tmp_dir}/first-class-case-values.yaml"
cat > "${first_class_values}" <<'EOF_VALUES'
byoo:
  logChunking:
    maxPayloadBytes: 1
    MaxPayloadBytes: 2
EOF_VALUES
assert_render_fails 'effective agent configuration: keys "MaxPayloadBytes" and "maxPayloadBytes" under "agent.byooLogChunking" differ only by case' \
  --values "${first_class_values}"
