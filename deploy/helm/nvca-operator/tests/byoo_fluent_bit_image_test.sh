#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
tmp_dir="$(mktemp -d)"
trap 'rm -rf "${tmp_dir}"' EXIT

fail() {
  echo "byoo-fluent-bit-image: $*" >&2
  exit 1
}

render() {
  local manifest="$1"
  shift
  helm template nvca-operator "${repo_root}/nvca-operator" \
    --set-string "ngcConfig.serviceKey=test-service-key" \
    --namespace nvca-operator \
    --set-string ngcConfig.clusterSource=self-managed \
    --values "${repo_root}/nvca-operator/values.yaml" \
    --values "${repo_root}/values.release-sbom.yaml" \
    --set-string selfManaged.icmsServiceURL=http://icms.example.invalid:8080 \
    --set-string selfManaged.revalServiceURL=http://reval.example.invalid:8080 \
    --set-string selfManaged.natsURL=nats://nats.example.invalid:4222 \
    "$@" \
    > "${manifest}"
}

# Prints the decoded JSON passed to the agent through the given env override
# flag, or {} when the flag is absent.
decoded_overrides() {
  local manifest="$1"
  local flag="$2"
  local encoded

  encoded="$(
    awk -v flag="${flag}" '
      $0 ~ "-[[:space:]]+" flag "$" { expect_value = 1; next }
      expect_value {
        value = $0
        sub(/^[[:space:]]*-[[:space:]]*"?/, "", value)
        sub(/"[[:space:]]*$/, "", value)
        print value
        exit
      }
    ' "${manifest}"
  )"
  if [[ -z "${encoded}" ]]; then
    echo '{}'
    return
  fi
  printf '%s' "${encoded}" | base64 --decode
}

# Prints the BYOO_FLUENTBIT_CONTAINER override for the given flag, or "unset".
fluent_bit_override() {
  decoded_overrides "$1" "$2" | yq -p json -r '.BYOO_FLUENTBIT_CONTAINER // "unset"'
}

assert_override() {
  local manifest="$1"
  local expected="$2"
  local message="$3"
  local flag actual

  for flag in --function-env-overrides-b64 --task-env-overrides-b64; do
    actual="$(fluent_bit_override "${manifest}" "${flag}")"
    if [[ "${actual}" != "${expected}" ]]; then
      fail "${message} (${flag}): expected ${expected}, got ${actual}"
    fi
  done
}

default_manifest="${tmp_dir}/default.yaml"
render "${default_manifest}"
assert_override "${default_manifest}" unset "chart default must leave the NVCA built-in Fluent Bit image in place"

mirror_repository="mirror.example.invalid/team/fluent-bit"
mirror_tag="review-test"
mirror_manifest="${tmp_dir}/mirror.yaml"
render "${mirror_manifest}" \
  --set-string "agent.byooFluentBit.imageRepository=${mirror_repository}" \
  --set-string "agent.byooFluentBit.imageTag=${mirror_tag}"
assert_override "${mirror_manifest}" "${mirror_repository}:${mirror_tag}" "agent.byooFluentBit was not passed to workloads"

explicit_image="explicit.example.invalid/fluent-bit:explicit"
explicit_manifest="${tmp_dir}/explicit.yaml"
render "${explicit_manifest}" \
  --set-string "agent.byooFluentBit.imageRepository=${mirror_repository}" \
  --set-string "agent.byooFluentBit.imageTag=${mirror_tag}" \
  --set-string "agent.functionEnvOverrides.BYOO_FLUENTBIT_CONTAINER=${explicit_image}" \
  --set-string "agent.taskEnvOverrides.BYOO_FLUENTBIT_CONTAINER=${explicit_image}"
assert_override "${explicit_manifest}" "${explicit_image}" "explicit env overrides must take precedence over agent.byooFluentBit"

collector_image="$(decoded_overrides "${mirror_manifest}" --function-env-overrides-b64 | yq -p json -r '.BYOO_OTEL_COLLECTOR_CONTAINER')"
default_collector_image="$(decoded_overrides "${default_manifest}" --function-env-overrides-b64 | yq -p json -r '.BYOO_OTEL_COLLECTOR_CONTAINER')"
if [[ "${collector_image}" != "${default_collector_image}" ]]; then
  fail "setting agent.byooFluentBit changed the BYOO collector image: ${collector_image}"
fi

error_log="${tmp_dir}/missing-tag.log"
if render "${tmp_dir}/missing-tag.yaml" \
  --set-string "agent.byooFluentBit.imageRepository=${mirror_repository}" 2>"${error_log}"; then
  fail "agent.byooFluentBit.imageRepository without imageTag should fail"
fi
grep -Fq "agent.byooFluentBit.imageTag is required" "${error_log}" ||
  fail "missing imageTag failed without the expected message: $(cat "${error_log}")"

echo "byoo-fluent-bit-image: all checks passed"
