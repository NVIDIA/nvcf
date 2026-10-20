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
test "$(yq -r '.selfManaged.eventLedgerServiceURL // "omitted"' "${default_values}")" = "omitted" ||
  fail "stack emitted an empty Event Ledger URL that would shadow the operator default"

event_ledger_values="${work_dir}/event-ledger-values.yaml"
render_values "${event_ledger_values}" \
  --state-values-set-string global.nvcaOperator.selfManaged.eventLedgerServiceURL=https://events.example.invalid
test "$(yq -r '.selfManaged.eventLedgerServiceURL' "${event_ledger_values}")" = "https://events.example.invalid" ||
  fail "global.nvcaOperator.selfManaged.eventLedgerServiceURL was not forwarded to the chart"

test "$(yq -r '.utils // "omitted"' "${default_values}")" = "omitted" ||
  fail "stack shadowed the chart utils reservation default without an explicit value"
utils_values="${work_dir}/utils-values.yaml"
render_values "${utils_values}" \
  --state-values-set-string global.nvcaOperator.utils.resources.cpu=1 \
  --state-values-set-string global.nvcaOperator.utils.resources.memory=2Gi
test "$(yq -r '.utils.resources.cpu | tag + " " + .' "${utils_values}")" = '!!str 1' ||
  fail "global.nvcaOperator.utils.resources.cpu was not forwarded to the chart as a string"
test "$(yq -r '.utils.resources.memory' "${utils_values}")" = "2Gi" ||
  fail "global.nvcaOperator.utils.resources.memory was not forwarded to the chart"

test "$(yq -r '.byoo // "omitted"' "${default_values}")" = "omitted" ||
  fail "stack shadowed the chart byoo defaults without an explicit value"
empty_byoo_state="${work_dir}/empty-byoo-state.yaml"
printf '%s\n' \
  'global:' \
  '  nvcaOperator:' \
  '    byoo: {}' >"${empty_byoo_state}"
empty_byoo_values="${work_dir}/empty-byoo-values.yaml"
render_values "${empty_byoo_values}" --state-values-file "${empty_byoo_state}"
test "$(yq -r '.byoo // "omitted"' "${empty_byoo_values}")" = "omitted" ||
  fail "stack emitted an empty byoo block"
byoo_state="${work_dir}/byoo-state.yaml"
printf '%s\n' \
  'global:' \
  '  nvcaOperator:' \
  '    byoo:' \
  '      resources:' \
  '        requests:' \
  '          cpu: 250m' \
  '          memory: 1Gi' \
  '      logChunking:' \
  '        maxPayloadBytes: 131072' \
  '        dryRun: true' \
  '      otelCollector:' \
  '        exporterHelper:' \
  '          timeout: 30s' \
  '      additionalResourceOverhead:' \
  '        cpu: "1"' \
  '      fluentbit:' \
  '        resources:' \
  '          limits:' \
  '            memory: 256Mi' >"${byoo_state}"
byoo_values="${work_dir}/byoo-values.yaml"
render_values "${byoo_values}" --state-values-file "${byoo_state}"
test "$(yq -r '.byoo.resources.requests.cpu' "${byoo_values}")" = "250m" ||
  fail "global.nvcaOperator.byoo.resources was not forwarded to the chart"
test "$(yq -r '.byoo.resources.requests.memory' "${byoo_values}")" = "1Gi" ||
  fail "global.nvcaOperator.byoo.resources.requests.memory was not forwarded to the chart"
test "$(yq -r '.byoo.logChunking.maxPayloadBytes' "${byoo_values}")" = "131072" ||
  fail "global.nvcaOperator.byoo.logChunking.maxPayloadBytes was not forwarded to the chart"
test "$(yq -r '.byoo.logChunking.dryRun' "${byoo_values}")" = "true" ||
  fail "global.nvcaOperator.byoo.logChunking.dryRun was not forwarded to the chart"
test "$(yq -r '.byoo.otelCollector.exporterHelper.timeout' "${byoo_values}")" = "30s" ||
  fail "global.nvcaOperator.byoo.otelCollector was not forwarded to the chart"
test "$(yq -r '.byoo.additionalResourceOverhead.cpu | tag + " " + .' "${byoo_values}")" = '!!str 1' ||
  fail "global.nvcaOperator.byoo.additionalResourceOverhead.cpu was not forwarded to the chart as a string"
test "$(yq -r '.byoo.fluentbit.resources.limits.memory' "${byoo_values}")" = "256Mi" ||
  fail "global.nvcaOperator.byoo.fluentbit.resources was not forwarded to the chart"

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
