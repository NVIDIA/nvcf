#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work_dir="$(mktemp -d)"
cluster_name="default-ownership"
registration_values="${work_dir}/${cluster_name}-register-values.yaml"
trap 'rm -rf "${work_dir}"' EXIT

fail() {
  echo "nvca-default-ownership: $*" >&2
  exit 1
}

render_values() {
  local output_file="$1"
  shift
  HELMFILE_ENV=base \
    CLUSTER_NAME="${cluster_name}" \
    NCA_ID=authoritative-nca \
    OUTPUT_DIR="${work_dir}" \
    helmfile \
      --file "${stack_dir}/helmfile.d/02-nvca.yaml.gotmpl" \
      --environment default \
      "$@" \
      --selector name=nvca-operator \
      write-values \
      --output-file-template "${output_file}" >/dev/null
}

write_registration() {
  local merge_config="${1:-}"
  printf '%s\n' \
    'clusterName: default-ownership' \
    'clusterID: fixture-cluster-id' \
    'clusterGroupID: fixture-cluster-group-id' \
    'selfManaged:' \
    '  region: fixture-region' \
    '  icmsServiceURL: http://icms.example.invalid:8080' \
    '  revalServiceURL: http://reval.example.invalid:8080' \
    '  natsURL: nats://nats.example.invalid:4222' >"${registration_values}"
  if [[ -n "${merge_config}" ]]; then
    printf 'agentConfig:\n  mergeConfig: |\n%s\n' "${merge_config}" >>"${registration_values}"
  fi
}

write_registration
default_values="${work_dir}/default-values.yaml"
render_values "${default_values}"
test "$(yq -r '.ncaID' "${default_values}")" = "authoritative-nca" ||
  fail "NCA_ID was not forwarded with the chart-consumed ncaID spelling"
test "$(yq -r '.ncaId // "omitted"' "${default_values}")" = "omitted" ||
  fail "stack emitted the unused ncaId spelling"
test "$(yq -r '.selfManaged.region' "${default_values}")" = "fixture-region" ||
  fail "selfManaged.region was not preserved"
test "$(yq -r '.selfManaged.identitySource // "omitted"' "${default_values}")" = "omitted" ||
  fail "render-only registration values contain CLI lifecycle metadata"
test "$(yq -r '.agentConfig // "omitted"' "${default_values}")" = "omitted" ||
  fail "stack shadowed the chart agentConfig defaults without an explicit merge"
test "$(yq -r '.nodeSelector // "omitted"' "${default_values}")" = "omitted" ||
  fail "stack emitted a node selector while the singular global default is disabled"

write_registration '    workload:
      defaultOwnershipProbe: true'
extended_values="${work_dir}/extended-values.yaml"
render_values "${extended_values}" --state-values-set addons.kaiScheduler.enabled=true
extended_config="$(yq -r '.agentConfig.mergeConfig' "${extended_values}")"
test "$(printf '%s' "${extended_config}" | yq -r '.cluster.validationPolicy.name')" = "Unrestricted" ||
  fail "stack extension did not preserve the chart validation policy default"
test "$(printf '%s' "${extended_config}" | yq -r '.workload.defaultOwnershipProbe')" = "true" ||
  fail "stack extension dropped the explicit mergeConfig override"

write_registration '    cluster:
      validationPolicy:
        name: Default'
override_values="${work_dir}/override-values.yaml"
render_values "${override_values}" --state-values-set addons.kaiScheduler.enabled=true
override_config="$(yq -r '.agentConfig.mergeConfig' "${override_values}")"
test "$(printf '%s' "${override_config}" | yq -r '.cluster.validationPolicy.name')" = "Default" ||
  fail "stack replaced an explicit validation policy override"

echo "nvca-default-ownership: all checks passed"
