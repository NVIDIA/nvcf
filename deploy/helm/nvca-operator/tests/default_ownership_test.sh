#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

chart_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
chart="${chart_root}/nvca-operator"
work_dir="$(mktemp -d)"
trap 'rm -rf "${work_dir}"' EXIT

fail() {
  echo "default-ownership: $*" >&2
  exit 1
}

render() {
  local manifest="$1"
  shift
  helm template nvca-operator "${chart}" \
    --namespace nvca-operator \
    --values "${chart}/values.yaml" \
    --set-string ngcConfig.clusterSource=self-managed \
    --set-string selfManaged.icmsServiceURL=http://icms.example.invalid:8080 \
    --set-string selfManaged.revalServiceURL=http://reval.example.invalid:8080 \
    --set-string selfManaged.natsURL=nats://nats.example.invalid:4222 \
    "$@" >"${manifest}"
}

agent_config() {
  yq ea -r \
    'select(.kind == "ConfigMap" and .metadata.name == "agent-config-merge") | .data."config.yaml"' \
    "$1"
}

backend_config() {
  yq ea -r \
    'select(.kind == "ConfigMap" and .metadata.name == "nvcfbackend-self-managed") | .data."cluster-dto.yaml"' \
    "$1"
}

default_manifest="${work_dir}/default.yaml"
render "${default_manifest}"
default_config="$(agent_config "${default_manifest}")"
default_policy="$(printf '%s' "${default_config}" | yq -r '.cluster.validationPolicy.name')"
test "${default_policy}" = "Unrestricted" ||
  fail "chart default validation policy is ${default_policy:-missing}, expected Unrestricted"
default_quic="$(printf '%s' "${default_config}" | yq -r '.workload.stargateQUICInsecure // "omitted"')"
test "${default_quic}" = "omitted" ||
  fail "chart serializes the runtime-default stargateQUICInsecure value"
default_region="$(backend_config "${default_manifest}" | yq -r '.region')"
test "${default_region}" = "us-west-1" ||
  fail "chart default self-managed region is ${default_region:-missing}, expected us-west-1"

override_values="${work_dir}/override.yaml"
printf '%s\n' \
  'agentConfig:' \
  '  mergeConfig: |' \
  '    cluster:' \
  '      validationPolicy:' \
  '        name: Default' \
  '    workload:' \
  '      stargateQUICInsecure: true' \
  'selfManaged:' \
  '  region: explicit-region' >"${override_values}"

override_manifest="${work_dir}/override.yaml.rendered"
render "${override_manifest}" --values "${override_values}"
override_config="$(agent_config "${override_manifest}")"
override_policy="$(printf '%s' "${override_config}" | yq -r '.cluster.validationPolicy.name')"
test "${override_policy}" = "Default" ||
  fail "explicit validation policy override was not preserved"
override_quic="$(printf '%s' "${override_config}" | yq -r '.workload.stargateQUICInsecure')"
test "${override_quic}" = "true" ||
  fail "explicit stargateQUICInsecure override was not preserved"
override_region="$(backend_config "${override_manifest}" | yq -r '.region')"
test "${override_region}" = "explicit-region" ||
  fail "explicit selfManaged.region override was not preserved"

echo "default-ownership: all checks passed"
