#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Verify that the values schema rejects what the gateway rejects at startup.
# The CI values file cannot cover this: it only exercises renders that succeed.
#
#   bash tests/chart-render/verify-llm-gateway-routing.sh
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CHART="${ROOT_DIR}/helm-nvcf-vanity-gateway"
WORK_DIR="$(mktemp -d)"
trap 'rm -rf "${WORK_DIR}"' EXIT

ENDPOINT="http://llm-api-gateway.nvcf.svc.cluster.local:8080"
VALUES="${WORK_DIR}/values.yaml"

write_values() {
  local endpoint="$1" section="$2" extra="$3"
  cat > "${VALUES}" <<EOF
vanityGateway:
  image:
    registry: example.com
    repository: foo/bar
  config:
    llmGatewayEndpoint: "${endpoint}"
  mappingConfig:
    v2config:
      openai:
        host: api.example.com
        ${section}:
          a_model:
            modelName: acme/a-model
            functionID: 00000000-0000-0000-0000-000000000001
${extra}
EOF
}

assert_rejected() {
  local needle="$1" label="$2" output
  if output="$(helm template t "${CHART}" -f "${VALUES}" 2>&1)"; then
    echo "FAILED: ${label}: rendered but should have been rejected" >&2
    exit 1
  fi
  if ! grep -F -q -- "${needle}" <<<"${output}"; then
    echo "FAILED: ${label}: expected '${needle}' in the error, got:" >&2
    echo "${output}" >&2
    exit 1
  fi
  echo "ok: ${label}"
}

assert_renders() {
  local label="$1"
  if ! helm template t "${CHART}" -f "${VALUES}" >/dev/null 2>&1; then
    echo "FAILED: ${label}: rejected but should have rendered" >&2
    helm template t "${CHART}" -f "${VALUES}" >&2 || true
    exit 1
  fi
  echo "ok: ${label}"
}

LLM="            functionType: LLM"

write_values "" chatCompletions "${LLM}"
assert_rejected "/vanityGateway/config/llmGatewayEndpoint" "an LLM model requires an endpoint"

write_values "${ENDPOINT}" chatCompletions "${LLM}"
assert_renders "an LLM model with an endpoint renders"

write_values "" chatCompletions ""
assert_renders "a model without functionType renders with an empty endpoint"

write_values "${ENDPOINT}" chatCompletions "            functionType: llmGateway"
assert_rejected "value must be 'LLM'" "an unrecognized functionType is rejected"

for section in responses embeddings; do
  write_values "${ENDPOINT}" "${section}" "${LLM}"
  assert_renders "an LLM model renders in ${section}"
done

for section in completions imageGenerations imageEdits imageVariations; do
  write_values "${ENDPOINT}" "${section}" "${LLM}"
  assert_rejected "openai/${section}/a_model" "functionType is rejected in ${section}"
done

write_values "${ENDPOINT}" chatCompletions "${LLM}
            usePexec: true"
assert_rejected "a_model/usePexec" "usePexec is rejected on an LLM model"

write_values "${ENDPOINT}" chatCompletions "${LLM}
            outgoingPathOverride: /echo"
assert_rejected "a_model/outgoingPathOverride" "outgoingPathOverride is rejected on an LLM model"

write_values "${ENDPOINT}" chatCompletions "${LLM}
            sessionTimeout: 900"
assert_rejected "a_model/sessionTimeout" "sessionTimeout is rejected on an LLM model"

# The LLM Gateway answers 400 for any request carrying X-Priority.
write_values "${ENDPOINT}" chatCompletions "${LLM}
            customHeaders:
              X-Priority: \"5\""
assert_rejected "a_model/customHeaders" "an X-Priority custom header is rejected on an LLM model"

# A zero value is what an absent key produces, so the gateway accepts it.
write_values "${ENDPOINT}" chatCompletions "${LLM}
            usePexec: false
            sessionTimeout: 0
            outgoingPathOverride: \"\""
assert_renders "zero-valued invocation fields render on an LLM model"

write_values "${ENDPOINT}" chatCompletions "${LLM}
            shadowModelNames:
              - acme/a-model-next
            shadowPercentage: 50"
assert_renders "shadow traffic renders on an LLM model"

# The proxy preserves the caller path and the transport only speaks http(s).
for bad in "ftp://llm-gateway:8080" "http://llm-gateway:8080/v1"; do
  write_values "${bad}" chatCompletions "${LLM}"
  assert_rejected "does not match pattern" "endpoint ${bad} is rejected"
done

echo "All LLM Gateway routing render checks passed."
