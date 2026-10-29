#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

chart_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work_dir="$(mktemp -d)"
trap 'rm -rf "${work_dir}"' EXIT

fail() {
  echo "resource-quantity-schema: $*" >&2
  exit 1
}

expected_schema_refs() {
  cat <<'EOF'
properties.agent.properties.resources.properties.limits.properties.cpu	#/definitions/resourceQuantity
properties.agent.properties.resources.properties.limits.properties.memory	#/definitions/resourceQuantity
properties.agent.properties.resources.properties.requests.properties.cpu	#/definitions/resourceQuantity
properties.agent.properties.resources.properties.requests.properties.memory	#/definitions/resourceQuantity
properties.byoo.properties.additionalResourceOverhead.additionalProperties	#/definitions/resourceQuantity
properties.byoo.properties.fluentbit.properties.resources.properties.limits.additionalProperties	#/definitions/resourceQuantity
properties.byoo.properties.fluentbit.properties.resources.properties.requests.additionalProperties	#/definitions/resourceQuantity
properties.byoo.properties.resources.properties.limits.additionalProperties	#/definitions/resourceQuantity
properties.byoo.properties.resources.properties.requests.additionalProperties	#/definitions/resourceQuantity
properties.clusterValidator.properties.resources.properties.limits.properties.cpu	#/definitions/resourceQuantity
properties.clusterValidator.properties.resources.properties.limits.properties.memory	#/definitions/resourceQuantity
properties.clusterValidator.properties.resources.properties.requests.properties.cpu	#/definitions/resourceQuantity
properties.clusterValidator.properties.resources.properties.requests.properties.memory	#/definitions/resourceQuantity
properties.otelCollector.properties.resources.properties.limits.properties.cpu	#/definitions/resourceQuantity
properties.otelCollector.properties.resources.properties.limits.properties.memory	#/definitions/resourceQuantity
properties.otelCollector.properties.resources.properties.requests.properties.cpu	#/definitions/resourceQuantity
properties.otelCollector.properties.resources.properties.requests.properties.memory	#/definitions/resourceQuantity
properties.resources.properties.limits.properties.cpu	#/definitions/resourceQuantity
properties.resources.properties.limits.properties.memory	#/definitions/resourceQuantity
properties.resources.properties.requests.properties.cpu	#/definitions/resourceQuantity
properties.resources.properties.requests.properties.memory	#/definitions/resourceQuantity
properties.storage.properties.internalPersistentStorage.properties.hardResourceQuota.additionalProperties	#/definitions/resourceQuantity
properties.storage.properties.sharedStorage.properties.server.properties.resources.properties.limits.additionalProperties	#/definitions/resourceQuantity
properties.storage.properties.sharedStorage.properties.server.properties.resources.properties.requests.additionalProperties	#/definitions/resourceQuantity
properties.storage.properties.sharedStorage.properties.taskData.properties.storageCapacity	#/definitions/optionalResourceQuantity
properties.utils.properties.resources.additionalProperties	#/definitions/resourceQuantity
properties.webhook.properties.resources.properties.limits.properties.cpu	#/definitions/resourceQuantity
properties.webhook.properties.resources.properties.limits.properties.memory	#/definitions/resourceQuantity
properties.webhook.properties.resources.properties.requests.properties.cpu	#/definitions/resourceQuantity
properties.webhook.properties.resources.properties.requests.properties.memory	#/definitions/resourceQuantity
EOF
}

assert_schema_topology() {
  local schema="$1"
  local actual_refs="${work_dir}/actual-refs"
  local expected_refs="${work_dir}/expected-refs"
  local refs_diff="${work_dir}/refs.diff"

  expected_schema_refs | LC_ALL=C sort >"${expected_refs}"
  yq -r \
    '.. | select(
      tag == "!!map" and
      (."$ref" == "#/definitions/resourceQuantity" or ."$ref" == "#/definitions/optionalResourceQuantity")
    ) | [path | join("."), ."$ref"] | @tsv' \
    "${schema}" | LC_ALL=C sort >"${actual_refs}"

  if ! diff -u "${expected_refs}" "${actual_refs}" >"${refs_diff}"; then
    cat "${refs_diff}" >&2
    fail "${schema} does not validate exactly the expected quantity fields"
  fi
}

render_with_quantity() {
  local chart="$1"
  local path="$2"
  local value="$3"
  local output="$4"
  local error_output="$5"
  local -a extra_args=()

  if [[ "${path}" == storage.internalPersistentStorage.hardResourceQuota.* ]]; then
    extra_args+=(--set-string storage.internalPersistentStorage.storageClassName=dummy)
  fi

  helm template nvca-operator "${chart}" \
    --namespace nvca-operator \
    --set generateImagePullSecret=false \
    --set imagePullSecretName=dummy \
    ${extra_args[@]+"${extra_args[@]}"} \
    --set-string "${path}=${value}" \
    >"${output}" 2>"${error_output}"
}

assert_accepts() {
  local chart="$1"
  local path="$2"
  local value="$3"

  if ! render_with_quantity "${chart}" "${path}" "${value}" \
    "${work_dir}/accepted.yaml" "${work_dir}/accepted.err"; then
    cat "${work_dir}/accepted.err" >&2
    fail "${chart} rejected valid quantity '${value}' for ${path}"
  fi
}

assert_rejects() {
  local chart="$1"
  local path="$2"
  local value="$3"

  if render_with_quantity "${chart}" "${path}" "${value}" \
    "${work_dir}/rejected.yaml" "${work_dir}/rejected.err"; then
    fail "${chart} accepted invalid quantity '${value}' for ${path}"
  fi
  grep -Fq "does not match pattern" "${work_dir}/rejected.err" || {
    cat "${work_dir}/rejected.err" >&2
    fail "${chart} did not report schema validation for ${path}=${value}"
  }
}

valid_quantities=(
  "0" "1" "01" "+1" "-1" "0.5" ".5" "1." "0.0001" "123.456789"
  "1n" "1u" "100m" "1k" "1M" "1G" "1T" "1P" "1E"
  "1Ki" "1Mi" "1Gi" "1.5Gi" "1Ti" "1Pi" "1Ei"
  "1e3" "1e+3" "1E+3" "1e-3"
)

invalid_quantities=(
  "notaquantity" "1K" "1ki" "1mi" "1MiB"
  "1e" "1E+" "1.2.3" "--1" "- 1" "1foo" "NaN" "Inf" " 1" "1 "
)

charts=("${chart_root}/nvca-operator")
for chart in "${charts[@]}"; do
  assert_schema_topology "${chart}/values.schema.json"

  for quantity in "${valid_quantities[@]}"; do
    assert_accepts "${chart}" byoo.resources.requests.cpu "${quantity}"
  done

  # Exercise each schema shape: explicit properties, additionalProperties,
  # and the optional quantity that deliberately accepts an empty string.
  assert_accepts "${chart}" resources.requests.cpu "250m"
  assert_accepts "${chart}" storage.internalPersistentStorage.hardResourceQuota.cpu "2"
  assert_accepts "${chart}" storage.sharedStorage.taskData.storageCapacity "10Gi"
  assert_accepts "${chart}" storage.sharedStorage.taskData.storageCapacity ""

  for quantity in "${invalid_quantities[@]}"; do
    assert_rejects "${chart}" byoo.resources.requests.cpu "${quantity}"
  done

  assert_rejects "${chart}" byoo.resources.requests.cpu ""
  assert_rejects "${chart}" resources.requests.cpu "notaquantity"
  assert_rejects "${chart}" storage.internalPersistentStorage.hardResourceQuota.cpu "notaquantity"
  assert_rejects "${chart}" storage.sharedStorage.taskData.storageCapacity "notaquantity"
done

echo "resource-quantity-schema: all checks passed"
