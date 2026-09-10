#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Verify that the values schema accepts and rejects shadow configuration the
# way the gateway does: the per-target shadows list, the legacy fields, their
# exclusivity, and the multipart image endpoints that support neither.
#
# Rejection needles are key names or route path fragments, because the Helm
# version decides the error message format (dotted paths on 3.18, JSON
# pointers on 3.21).
#
#   bash tests/chart-render/verify-shadow-schema.sh
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CHART="${ROOT_DIR}/helm-nvcf-vanity-gateway"
WORK_DIR="$(mktemp -d)"
trap 'rm -rf "${WORK_DIR}"' EXIT

ENDPOINT="http://llm-api-gateway.nvcf.svc.cluster.local:8080"
VALUES="${WORK_DIR}/values.yaml"

# write_values <section> <route body indented 12 spaces>
write_values() {
  local section="$1" extra="$2"
  cat > "${VALUES}" <<EOF
vanityGateway:
  image:
    registry: example.com
    repository: foo/bar
  config:
    llmGatewayEndpoint: "${ENDPOINT}"
  mappingConfig:
    v2config:
      openai:
        host: api.example.com
        ${section}:
          a_model:
            modelName: acme/a-model
            functionID: 00000000-0000-0000-0000-000000000001
${extra}
          a_model_next:
            modelName: acme/a-model-next
            functionID: 00000000-0000-0000-0000-000000000002
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

SHADOWS_FULL="            shadows:
              - modelName: acme/a-model-next
                percentage: 10
                samplingMethod: perBearerKey
                cancelOnClientDisconnect: true
              - modelName: acme/a-model-other
                percentage: 100
                samplingMethod: random
                cancelOnClientDisconnect: false"
SHADOWS_MINIMAL="            shadows:
              - modelName: acme/a-model-next"
SHADOWS_EMPTY="            shadows: []"
LEGACY_FULL="            shadowModelName: acme/a-model-next
            shadowModelNames:
              - acme/a-model-other
            shadowPercentage: 50
            shadowSamplingMethod: perBearerKey
            shadowCancelOnClientDisconnect: true"
LEGACY_ZERO="            shadowModelName: \"\"
            shadowModelNames: []
            shadowSamplingMethod: \"\"
            shadowCancelOnClientDisconnect: false"

# Both forms render on their own, in every non-multipart section.
for section in chatCompletions completions embeddings responses imageGenerations; do
  write_values "${section}" "${SHADOWS_FULL}"
  assert_renders "a shadows list renders in ${section}"

  write_values "${section}" "${LEGACY_FULL}"
  assert_renders "legacy shadow fields render in ${section}"
done

write_values chatCompletions "            functionType: LLM
${SHADOWS_MINIMAL}"
assert_renders "a shadows entry with only modelName renders on an LLM model"

write_values chatCompletions "${SHADOWS_EMPTY}"
assert_renders "an empty shadows list renders"

# Zero values are what absent legacy keys produce, so the gateway accepts them.
write_values chatCompletions "${LEGACY_ZERO}"
assert_renders "zero-valued legacy shadow fields render"

write_values chatCompletions "            shadowSamplingMethod: null"
assert_renders "a null shadowSamplingMethod renders"

# The forms are exclusive: shadows plus any legacy key, even zero-valued, fails.
LEGACY_KEYS=(
  "shadowModelName: acme/a-model-next"
  "shadowModelNames: []"
  "shadowPercentage: 50"
  "shadowSamplingMethod: \"\""
  "shadowCancelOnClientDisconnect: false"
)
for legacy in "${LEGACY_KEYS[@]}"; do
  write_values chatCompletions "${SHADOWS_EMPTY}
            ${legacy}"
  assert_rejected "a_model" "shadows combined with ${legacy%%:*} is rejected"
done

# The gateway reads shadow keys case-insensitively, so a variant spelling would
# slip past the exclusivity rule; the schema rejects every non-canonical form.
for variant in "Shadows: []" "shadowpercentage: 50" "ShadowModelNames: []" "shadowcancelonclientdisconnect: true"; do
  write_values chatCompletions "            ${variant}"
  assert_rejected "${variant%%:*}" "shadow key spelled ${variant%%:*} is rejected"
done

# Per-target entry validation.
write_values chatCompletions "            shadows:
              - percentage: 50"
assert_rejected "modelName" "a shadows entry without modelName is rejected"

write_values chatCompletions "            shadows:
              - modelname: acme/a-model-next"
assert_rejected "modelname" "a shadows entry with an unknown field is rejected"

for percentage in 0 101 50.5 '"50"'; do
  write_values chatCompletions "            shadows:
              - modelName: acme/a-model-next
                percentage: ${percentage}"
  assert_rejected "percentage" "a shadows percentage of ${percentage} is rejected"
done

write_values chatCompletions "            shadows:
              - modelName: acme/a-model-next
                samplingMethod: perUser"
assert_rejected "samplingMethod" "an unknown shadows samplingMethod is rejected"

write_values chatCompletions "            shadows:
              - modelName: acme/a-model-next
                samplingMethod: \"\""
assert_rejected "samplingMethod" "an empty shadows samplingMethod is rejected"

write_values chatCompletions "            shadows:
              - modelName: acme/a-model-next
                cancelOnClientDisconnect: \"true\""
assert_rejected "cancelOnClientDisconnect" "a string shadows cancelOnClientDisconnect is rejected"

write_values chatCompletions "            shadows:
              acme/a-model-next:
                percentage: 50"
assert_rejected "shadows" "a shadows mapping is rejected"

write_values chatCompletions "            shadowSamplingMethod: perUser"
assert_rejected "shadowSamplingMethod" "an unknown legacy shadowSamplingMethod is rejected"

# Multipart image endpoints support neither form. Zero values stay accepted
# because that is what absent keys produce.
MULTIPART_SHADOWS=(
  "${SHADOWS_MINIMAL}"
  "            shadowModelName: acme/a-model-next"
  "            shadowModelNames:
              - acme/a-model-next"
  "            shadowPercentage: 50"
  "            shadowSamplingMethod: random"
  "            shadowCancelOnClientDisconnect: true"
)
for section in imageEdits imageVariations; do
  for extra in "${MULTIPART_SHADOWS[@]}"; do
    write_values "${section}" "${extra}"
    key="$(sed -n '1s/^ *\([A-Za-z]*\):.*/\1/p' <<<"${extra}")"
    assert_rejected "a_model" "${key} is rejected in ${section}"
  done

  write_values "${section}" "${SHADOWS_EMPTY}
${LEGACY_ZERO}"
  assert_rejected "a_model" "empty shadows with zero-valued legacy fields is rejected in ${section}"

  write_values "${section}" "${SHADOWS_EMPTY}"
  assert_renders "an empty shadows list renders in ${section}"

  write_values "${section}" "${LEGACY_ZERO}"
  assert_renders "zero-valued legacy shadow fields render in ${section}"
done

echo "All shadow schema render checks passed."
