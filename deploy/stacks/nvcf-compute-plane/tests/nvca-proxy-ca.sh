#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Test that global.nvcaOperator.agent.workerCallbackProxyCaPem reaches function
# pods as NVCF_PROXY_CA_PEM, encoded as one quoted line, and that the default
# render carries no override.

set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work_dir="$(mktemp -d)"
cluster_name="proxy-ca"
trap 'rm -rf "${work_dir}"' EXIT

fail() {
  echo "nvca-proxy-ca: $*" >&2
  exit 1
}

printf '%s\n' \
  'clusterName: proxy-ca' \
  'clusterID: fixture-cluster-id' \
  'clusterGroupID: fixture-cluster-group-id' \
  'selfManaged:' \
  '  icmsServiceURL: http://icms.example.invalid:8080' \
  '  revalServiceURL: http://reval.example.invalid:8080' \
  '  natsURL: nats://nats.example.invalid:4222' >"${work_dir}/${cluster_name}-register-values.yaml"

render_values() {
  local output_file="$1"
  shift
  HELMFILE_ENV=base \
    CLUSTER_NAME="${cluster_name}" \
    NCA_ID=fixture-nca \
    OUTPUT_DIR="${work_dir}" \
    helmfile \
      --file "${stack_dir}/helmfile.d/02-nvca.yaml.gotmpl" \
      --environment default \
      "$@" \
      --selector name=nvca-operator \
      write-values \
      --output-file-template "${output_file}" >/dev/null
}

default_values="${work_dir}/default.yaml"
render_values "${default_values}"
test "$(yq -r '.agent.functionEnvOverrides // "omitted"' "${default_values}")" = "omitted" ||
  fail "default render must not set function env overrides"

# Fixture certificate body; not a real CA.
pem=$'-----BEGIN CERTIFICATE-----\nMIIBfixtureLine1\nMIIBfixtureLine2\n-----END CERTIFICATE-----'
cat >"${work_dir}/ca-values.yaml" <<EOF
global:
  nvcaOperator:
    agent:
      workerCallbackProxyCaPem: |
$(printf '%s\n' "${pem}" | sed 's/^/        /')
EOF

ca_values="${work_dir}/ca.yaml"
render_values "${ca_values}" --state-values-file "${work_dir}/ca-values.yaml"
encoded="$(yq -r '.agent.functionEnvOverrides.NVCF_PROXY_CA_PEM' "${ca_values}")"
[[ "${encoded}" != *$'\n'* ]] || fail "NVCF_PROXY_CA_PEM must be a single line, got: ${encoded}"
[[ "${encoded}" == \"*\" ]] || fail "NVCF_PROXY_CA_PEM must be a quoted string, got: ${encoded}"
decoded="$(jq -r '.' <<<"${encoded}")"
test "${decoded}" = "${pem}" || fail "NVCF_PROXY_CA_PEM must decode to the full PEM, got: ${decoded}"

assert_rejected() {
  local name="$1" value="$2"
  printf 'global:\n  nvcaOperator:\n    agent:\n      workerCallbackProxyCaPem: |\n%s\n' \
    "$(printf '%s\n' "${value}" | sed 's/^/        /')" >"${work_dir}/${name}-values.yaml"
  if render_values "${work_dir}/${name}.yaml" --state-values-file "${work_dir}/${name}-values.yaml" 2>"${work_dir}/${name}.err"; then
    fail "${name}: the render must fail"
  fi
  grep -Fq "must be a PEM certificate bundle" "${work_dir}/${name}.err" ||
    fail "${name}: unexpected error: $(tail -3 "${work_dir}/${name}.err")"
}

assert_rejected not-pem 'not-a-certificate'
assert_rejected begin-only '-----BEGIN CERTIFICATE-----'
assert_rejected truncated $'-----BEGIN CERTIFICATE-----\nMIIBfixtureLine1'
assert_rejected trailing-junk "${pem}"$'\nnot-a-certificate'

echo "nvca-proxy-ca: all checks passed"
