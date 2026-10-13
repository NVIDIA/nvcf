#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# NVBug 6914489: a negative worker.timeouts interval (e.g. heartbeatInterval:
# -1s) used to be accepted by this chart's schema, then reach the operator and
# crash-loop it. Asserts the schema now rejects a negative duration string for
# every worker.timeouts field at `helm template` time, before the value ever
# reaches the operator.

set -euo pipefail

chart_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work_dir="$(mktemp -d)"
trap 'rm -rf "${work_dir}"' EXIT

fail() {
  echo "worker-timeouts-schema: $*" >&2
  exit 1
}

expected_schema_refs() {
  cat <<'EOF'
properties.worker.properties.timeouts.properties.credRenewInterval	#/definitions/optionalDuration
properties.worker.properties.timeouts.properties.heartbeatInterval	#/definitions/optionalDuration
properties.worker.properties.timeouts.properties.icmsRequestAckInterval	#/definitions/optionalDuration
properties.worker.properties.timeouts.properties.icmsRequestAckRetryTimeout	#/definitions/optionalDuration
properties.worker.properties.timeouts.properties.periodicInstanceStatusInterval	#/definitions/optionalDuration
properties.worker.properties.timeouts.properties.syncAcknowledgeRequestInterval	#/definitions/optionalDuration
properties.worker.properties.timeouts.properties.syncQueueInterval	#/definitions/optionalDuration
properties.worker.properties.timeouts.properties.syncRequestStatusInterval	#/definitions/optionalDuration
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
      tag == "!!map" and ."$ref" == "#/definitions/optionalDuration"
    ) | [path | join("."), ."$ref"] | @tsv' \
    "${schema}" | LC_ALL=C sort >"${actual_refs}"

  if ! diff -u "${expected_refs}" "${actual_refs}" >"${refs_diff}"; then
    cat "${refs_diff}" >&2
    fail "${schema} does not validate exactly the expected worker.timeouts fields"
  fi
}

render_with_duration() {
  local chart="$1"
  local path="$2"
  local value="$3"
  local output="$4"
  local error_output="$5"

  helm template nvca-operator "${chart}" \
    --namespace nvca-operator \
    --set generateImagePullSecret=false \
    --set imagePullSecretName=dummy \
    --set-string "${path}=${value}" \
    >"${output}" 2>"${error_output}"
}

assert_accepts() {
  local chart="$1"
  local path="$2"
  local value="$3"

  if ! render_with_duration "${chart}" "${path}" "${value}" \
    "${work_dir}/accepted.yaml" "${work_dir}/accepted.err"; then
    cat "${work_dir}/accepted.err" >&2
    fail "${chart} rejected valid duration '${value}' for ${path}"
  fi
}

assert_rejects() {
  local chart="$1"
  local path="$2"
  local value="$3"

  if render_with_duration "${chart}" "${path}" "${value}" \
    "${work_dir}/rejected.yaml" "${work_dir}/rejected.err"; then
    fail "${chart} accepted invalid duration '${value}' for ${path}"
  fi
  grep -Fq "does not match pattern" "${work_dir}/rejected.err" || {
    cat "${work_dir}/rejected.err" >&2
    fail "${chart} did not report schema validation for ${path}=${value}"
  }
}

valid_durations=(
  "" "0s" "30s" "500ms" "1m30s" "1h" "2.5s" "100ns" "1us" "5m0s"
)

invalid_durations=(
  # NVBug 6914489's exact repro value.
  "-1s" "-30s" "-1" "1" "30seconds" "1x" "notaduration" "1.2.3s" "- 1s" "1 s"
)

charts=("${chart_root}/nvca-operator")
for chart in "${charts[@]}"; do
  assert_schema_topology "${chart}/values.schema.json"

  for duration in "${valid_durations[@]}"; do
    assert_accepts "${chart}" worker.timeouts.heartbeatInterval "${duration}"
  done
  assert_accepts "${chart}" worker.timeouts.credRenewInterval "2ms"

  for duration in "${invalid_durations[@]}"; do
    assert_rejects "${chart}" worker.timeouts.heartbeatInterval "${duration}"
  done
  assert_rejects "${chart}" worker.timeouts.credRenewInterval "-1s"
done

echo "worker-timeouts-schema: all checks passed"
