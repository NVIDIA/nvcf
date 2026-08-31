#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Verify the values-schema contract around functionType: LLM.
#
# An openai model that sets functionType: LLM is served by the LLM Gateway, so
# the render must fail when config.llmGatewayEndpoint is empty. The CI values
# file cannot cover that: it only exercises renders that are expected to
# succeed. These are the negative directions.
#
#   bash tests/chart-render/verify-llm-gateway-routing.sh
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CHART="${ROOT_DIR}/helm-nvcf-vanity-gateway"
WORK_DIR="$(mktemp -d)"
trap 'rm -rf "${WORK_DIR}"' EXIT

# Emits a values file. $1 is the functionType line (empty for none), $2 the
# endpoint value.
write_values() {
  local function_type_line="$1" endpoint="$2" out="$3"
  cat > "${out}" <<EOF
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
        chatCompletions:
          a_model:
            modelName: acme/a-model
            functionID: 00000000-0000-0000-0000-000000000001
${function_type_line}
EOF
}

assert_render_fails() {
  local values="$1" needle="$2" label="$3"
  local output
  if output="$(helm template t "${CHART}" -f "${values}" 2>&1)"; then
    echo "FAILED: ${label}: render succeeded but should have been rejected" >&2
    exit 1
  fi
  if ! grep -F -q -- "${needle}" <<<"${output}"; then
    echo "FAILED: ${label}: expected '${needle}' in the error, got:" >&2
    echo "${output}" >&2
    exit 1
  fi
  echo "ok: ${label}"
}

assert_render_succeeds() {
  local values="$1" label="$2"
  if ! helm template t "${CHART}" -f "${values}" >/dev/null 2>&1; then
    echo "FAILED: ${label}: render was rejected but should have succeeded" >&2
    helm template t "${CHART}" -f "${values}" >&2 || true
    exit 1
  fi
  echo "ok: ${label}"
}

LLM_LINE='            functionType: LLM'

# An LLM-routed model with no endpoint must not render. Without this the values
# file passes validation and the container exits at startup instead.
write_values "${LLM_LINE}" "" "${WORK_DIR}/llm-empty.yaml"
assert_render_fails "${WORK_DIR}/llm-empty.yaml" \
  "/vanityGateway/config/llmGatewayEndpoint" \
  "functionType: LLM with an empty endpoint is rejected"

# The same model with an endpoint is the supported configuration.
write_values "${LLM_LINE}" "http://llm-api-gateway.nvcf.svc.cluster.local:8080" "${WORK_DIR}/llm-set.yaml"
assert_render_succeeds "${WORK_DIR}/llm-set.yaml" \
  "functionType: LLM with an endpoint renders"

# A model without the flag is served by the invocation service, so an empty
# endpoint stays valid. Requiring it unconditionally would break every existing
# values file.
write_values "" "" "${WORK_DIR}/default-empty.yaml"
assert_render_succeeds "${WORK_DIR}/default-empty.yaml" \
  "no functionType with an empty endpoint renders"

# functionType is constrained to a single value so a typo fails the render
# rather than silently routing to the invocation service.
write_values '            functionType: llmGateway' "http://llm.example" "${WORK_DIR}/bad-value.yaml"
assert_render_fails "${WORK_DIR}/bad-value.yaml" \
  "value must be 'LLM'" \
  "an unrecognized functionType is rejected"

# The gateway rejects each of these at startup. The schema must reject them at
# render time instead, so a bad values file never reaches a running container.

write_route() {
  local endpoint="$1" section="$2" extra="$3" out="$4"
  cat > "${out}" <<EOF
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

ENDPOINT="http://llm-api-gateway.nvcf.svc.cluster.local:8080"

# functionType is only meaningful on the three routes the LLM Gateway serves.
for section in completions imageGenerations imageEdits imageVariations; do
  write_route "${ENDPOINT}" "${section}" "            functionType: LLM" "${WORK_DIR}/s.yaml"
  assert_render_fails "${WORK_DIR}/s.yaml" "openai/${section}/a_model" \
    "functionType: LLM is rejected in ${section}"
done

# Invocation-only fields do not apply to a model the LLM Gateway serves.
write_route "${ENDPOINT}" chatCompletions "            functionType: LLM
            usePexec: true" "${WORK_DIR}/f.yaml"
assert_render_fails "${WORK_DIR}/f.yaml" "a_model/usePexec" "usePexec is rejected on an LLM model"

write_route "${ENDPOINT}" chatCompletions "            functionType: LLM
            outgoingPathOverride: /echo" "${WORK_DIR}/f.yaml"
assert_render_fails "${WORK_DIR}/f.yaml" "a_model/outgoingPathOverride" "outgoingPathOverride is rejected on an LLM model"

write_route "${ENDPOINT}" chatCompletions "            functionType: LLM
            sessionTimeout: 900" "${WORK_DIR}/f.yaml"
assert_render_fails "${WORK_DIR}/f.yaml" "a_model/sessionTimeout" "sessionTimeout is rejected on an LLM model"

# The LLM Gateway answers 400 for any request carrying X-Priority.
write_route "${ENDPOINT}" chatCompletions "            functionType: LLM
            customHeaders:
              X-Priority: \"5\"" "${WORK_DIR}/f.yaml"
assert_render_fails "${WORK_DIR}/f.yaml" "a_model/customHeaders" "an X-Priority custom header is rejected on an LLM model"

# Zero values are what an absent key produces, so the gateway accepts them.
write_route "${ENDPOINT}" chatCompletions "            functionType: LLM
            usePexec: false
            sessionTimeout: 0
            outgoingPathOverride: \"\"" "${WORK_DIR}/z.yaml"
assert_render_succeeds "${WORK_DIR}/z.yaml" "zero-valued invocation fields render on an LLM model"

# Shadow traffic is supported on LLM models.
write_route "${ENDPOINT}" chatCompletions "            functionType: LLM
            shadowModelNames:
              - acme/a-model-next
            shadowPercentage: 50" "${WORK_DIR}/sh.yaml"
assert_render_succeeds "${WORK_DIR}/sh.yaml" "shadow traffic renders on an LLM model"

# The endpoint is an origin. The proxy preserves the caller path and the
# transport only speaks http and https.
for bad in "ftp://llm-gateway:8080" "http://llm-gateway:8080/v1"; do
  write_route "${bad}" chatCompletions "            functionType: LLM" "${WORK_DIR}/e.yaml"
  assert_render_fails "${WORK_DIR}/e.yaml" "does not match pattern" \
    "endpoint ${bad} is rejected"
done

echo "All LLM Gateway routing render checks passed."
