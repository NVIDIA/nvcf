#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Test that global.nvcaOperator.agent.workerCallbackCaPem reaches function
# pods as NVCF_PROXY_CA_PEM, encoded as one quoted line, and that the default
# render carries no override.

set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work_dir="$(mktemp -d)"
cluster_name="callback-ca"
trap 'rm -rf "${work_dir}"' EXIT

fail() {
  echo "nvca-callback-ca: $*" >&2
  exit 1
}

printf '%s\n' \
  'clusterName: callback-ca' \
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

# generate_ca prints a fresh self-signed CA certificate from Helm's genCA.
generate_ca() {
  local chart_dir="${work_dir}/genca-$1"
  mkdir -p "${chart_dir}/templates"
  printf 'apiVersion: v2\nname: genca\nversion: 0.1.0\n' >"${chart_dir}/Chart.yaml"
  printf 'apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: ca\ndata:\n  cert: {{ (genCA "%s" 1).Cert | quote }}\n' "$1" \
    >"${chart_dir}/templates/ca.yaml"
  helm template genca "${chart_dir}" | yq -r '.data.cert' | sed '/^$/d'
}

pem="$(generate_ca nvcf-callback-ca-test-1)"
second_pem="$(generate_ca nvcf-callback-ca-test-2)"
cat >"${work_dir}/ca-values.yaml" <<EOF
global:
  nvcaOperator:
    agent:
      workerCallbackCaPem: |
$(printf '%s\n' "${pem}" | sed 's/^/        /')
EOF

ca_values="${work_dir}/ca.yaml"
render_values "${ca_values}" --state-values-file "${work_dir}/ca-values.yaml"
encoded="$(yq -r '.agent.functionEnvOverrides.NVCF_PROXY_CA_PEM' "${ca_values}")"
[[ "${encoded}" != *$'\n'* ]] || fail "NVCF_PROXY_CA_PEM must be a single line, got: ${encoded}"
[[ "${encoded}" == \"*\" ]] || fail "NVCF_PROXY_CA_PEM must be a quoted string, got: ${encoded}"
decoded="$(jq -r '.' <<<"${encoded}")"
test "${decoded}" = "${pem}" || fail "NVCF_PROXY_CA_PEM must decode to the full PEM, got: ${decoded}"

bundle="${pem}"$'\n'"${second_pem}"
cat >"${work_dir}/bundle-values.yaml" <<EOF
global:
  nvcaOperator:
    agent:
      workerCallbackCaPem: |
$(printf '%s\n' "${bundle}" | sed 's/^/        /')
EOF
render_values "${work_dir}/bundle.yaml" --state-values-file "${work_dir}/bundle-values.yaml"
decoded="$(yq -r '.agent.functionEnvOverrides.NVCF_PROXY_CA_PEM' "${work_dir}/bundle.yaml" | jq -r '.')"
test "${decoded}" = "${bundle}" || fail "a two-certificate bundle must pass through unchanged"

assert_rejected() {
  local name="$1" value="$2"
  printf 'global:\n  nvcaOperator:\n    agent:\n      workerCallbackCaPem: |\n%s\n' \
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
# PEM-shaped, but the body is not valid base64.
assert_rejected bad-base64 $'-----BEGIN CERTIFICATE-----\nMIIBfixtureLine1\nMIIBfixtureLine2\n-----END CERTIFICATE-----'
# Valid base64 that does not decode to DER ("hello world").
assert_rejected not-der $'-----BEGIN CERTIFICATE-----\naGVsbG8gd29ybGQ=\n-----END CERTIFICATE-----'
# One good certificate does not excuse a bad one in the same bundle.
assert_rejected bad-in-bundle "${pem}"$'\n-----BEGIN CERTIFICATE-----\naGVsbG8gd29ybGQ=\n-----END CERTIFICATE-----'

echo "nvca-callback-ca: all checks passed"
