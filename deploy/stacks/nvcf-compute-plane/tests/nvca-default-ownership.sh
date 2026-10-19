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

base_values="${stack_dir}/environments/base.yaml"
pinned_byoo_collector_tag="$(yq -r '.global.nvcaOperator.byooOtelCollector.imageTag' "${base_values}")"
test -n "${pinned_byoo_collector_tag}" && test "${pinned_byoo_collector_tag}" != "null" ||
  fail "environments/base.yaml does not pin global.nvcaOperator.byooOtelCollector.imageTag"
test "$(yq -r '.agent.byooOtelCollector.imageTag' "${default_values}")" = "${pinned_byoo_collector_tag}" ||
  fail "the stack BYOO collector tag pin was not forwarded to agent.byooOtelCollector.imageTag"
test "$(yq -r '.agent.byooOtelCollector.imageRepository // "omitted"' "${default_values}")" = "omitted" ||
  fail "stack set a BYOO collector repository without an explicit value"
byoo_collector_values="${work_dir}/byoo-collector-values.yaml"
render_values "${byoo_collector_values}" \
  --state-values-set-string global.nvcaOperator.byooOtelCollector.imageRepository=mirror.example.invalid/team/byoo-otel-collector \
  --state-values-set-string global.nvcaOperator.byooOtelCollector.imageTag=review-test
test "$(yq -r '.agent.byooOtelCollector.imageRepository' "${byoo_collector_values}")" = "mirror.example.invalid/team/byoo-otel-collector" ||
  fail "global.nvcaOperator.byooOtelCollector.imageRepository was not forwarded to the chart"
test "$(yq -r '.agent.byooOtelCollector.imageTag' "${byoo_collector_values}")" = "review-test" ||
  fail "global.nvcaOperator.byooOtelCollector.imageTag override was not forwarded to the chart"

# The stack default must match the image NVCA falls back to when no
# BYOO_FLUENTBIT_CONTAINER override is set. Docker Hub references without a
# registry resolve to docker.io.
nvca_otel_source="${stack_dir}/../../../src/libraries/go/lib/pkg/icms-translate/translate/common/otel.go"
nvca_fluent_bit_default="$(sed -n 's/^[[:space:]]*DefaultFluentbitImage[[:space:]]*=[[:space:]]*"\(.*\)"$/\1/p' "${nvca_otel_source}")"
test -n "${nvca_fluent_bit_default}" || fail "could not read DefaultFluentbitImage from ${nvca_otel_source}"
stack_fluent_bit_default="$(yq -r '.agent.byooFluentBit.imageRepository + ":" + .agent.byooFluentBit.imageTag' "${default_values}")"
test "${stack_fluent_bit_default}" = "docker.io/${nvca_fluent_bit_default}" ||
  fail "stack Fluent Bit default ${stack_fluent_bit_default} does not match the NVCA default ${nvca_fluent_bit_default}"
fluent_bit_values="${work_dir}/fluent-bit-values.yaml"
render_values "${fluent_bit_values}" \
  --state-values-set-string global.nvcaOperator.byooFluentBit.image.registry=mirror.example.invalid \
  --state-values-set-string global.nvcaOperator.byooFluentBit.image.repository=team/fluent-bit \
  --state-values-set-string global.nvcaOperator.byooFluentBit.image.tag=review-test
test "$(yq -r '.agent.byooFluentBit.imageRepository' "${fluent_bit_values}")" = "mirror.example.invalid/team/fluent-bit" ||
  fail "global.nvcaOperator.byooFluentBit.image registry and repository were not forwarded to the chart"
test "$(yq -r '.agent.byooFluentBit.imageTag' "${fluent_bit_values}")" = "review-test" ||
  fail "global.nvcaOperator.byooFluentBit.image.tag was not forwarded to the chart"
# helmfile ignores empty --state-values-set-string values, so empty fields are
# set through state values files.
fluent_bit_no_registry_state="${work_dir}/fluent-bit-no-registry-state.yaml"
printf '%s\n' \
  'global:' \
  '  nvcaOperator:' \
  '    byooFluentBit:' \
  '      image:' \
  '        registry: ""' \
  '        repository: mirror.example.invalid/fluent-bit' >"${fluent_bit_no_registry_state}"
fluent_bit_no_registry_values="${work_dir}/fluent-bit-no-registry-values.yaml"
render_values "${fluent_bit_no_registry_values}" --state-values-file "${fluent_bit_no_registry_state}"
test "$(yq -r '.agent.byooFluentBit.imageRepository' "${fluent_bit_no_registry_values}")" = "mirror.example.invalid/fluent-bit" ||
  fail "an empty Fluent Bit registry should forward the repository unchanged"
fluent_bit_missing_tag_state="${work_dir}/fluent-bit-missing-tag-state.yaml"
printf '%s\n' \
  'global:' \
  '  nvcaOperator:' \
  '    byooFluentBit:' \
  '      image:' \
  '        tag: ""' >"${fluent_bit_missing_tag_state}"
fluent_bit_error="${work_dir}/fluent-bit-error.log"
if render_values "${work_dir}/fluent-bit-missing-tag.yaml" \
  --state-values-file "${fluent_bit_missing_tag_state}" 2>"${fluent_bit_error}"; then
  fail "a Fluent Bit repository without a tag should fail to render"
fi
grep -Fq "global.nvcaOperator.byooFluentBit.image.tag is required" "${fluent_bit_error}" ||
  fail "missing Fluent Bit tag failed without the expected message"

has_gate() {
  yq -r ".selfManaged.featureGateValues | any_c(. == \"$2\")" "$1"
}
test "$(has_gate "${default_values}" BYOOFluentBit)" = "false" ||
  fail "BYOOFluentBit should stay off by default"
fluent_bit_gate_values="${work_dir}/fluent-bit-gate-values.yaml"
render_values "${fluent_bit_gate_values}" --state-values-set global.nvcaOperator.byooFluentBit.enabled=true
test "$(has_gate "${fluent_bit_gate_values}" BYOOFluentBit)" = "true" ||
  fail "global.nvcaOperator.byooFluentBit.enabled did not enable the BYOOFluentBit gate"
test "$(has_gate "${fluent_bit_gate_values}" BYOObservability)" = "true" ||
  fail "enabling Fluent Bit dropped the BYOObservability gate"
fluent_bit_gate_disabled_state="${work_dir}/fluent-bit-gate-disabled-state.yaml"
printf '%s\n' \
  'global:' \
  '  nvcaOperator:' \
  '    byooFluentBit:' \
  '      enabled: true' \
  '    selfManaged:' \
  '      featureGateValues:' \
  '        - -BYOOFluentBit' >"${fluent_bit_gate_disabled_state}"
fluent_bit_gate_disabled_values="${work_dir}/fluent-bit-gate-disabled-values.yaml"
render_values "${fluent_bit_gate_disabled_values}" --state-values-file "${fluent_bit_gate_disabled_state}"
test "$(has_gate "${fluent_bit_gate_disabled_values}" BYOOFluentBit)" = "false" ||
  fail "an explicit -BYOOFluentBit gate did not win over byooFluentBit.enabled"

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
