#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Verify the values schema for the per-target shadows list: the list and its
# entries are validated the way the gateway validates them, in any letter
# case; the two forms never mix; multipart image endpoints take no shadows.
# The legacy shadow fields are not under test here; their schema is unchanged.
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

# first_key <yaml block>: the key on the block's first line, for labels.
first_key() {
  local line="${1%%$'\n'*}"
  line="${line#"${line%%[! ]*}"}"
  echo "${line%%:*}"
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

for section in chatCompletions completions embeddings responses imageGenerations; do
  write_values "${section}" "${SHADOWS_FULL}"
  assert_renders "a shadows list renders in ${section}"
done

write_values chatCompletions "            functionType: LLM
${SHADOWS_MINIMAL}"
assert_renders "a shadows entry with only modelName renders on an LLM model"

write_values chatCompletions "${SHADOWS_EMPTY}"
assert_renders "an empty shadows list renders"

write_values chatCompletions "            shadows:
              - modelName: acme/a-model-next
                samplingMethod: \"\""
assert_renders "an empty shadows samplingMethod renders"

# The gateway reads the shadows key and entry keys in any letter case.
write_values chatCompletions "            Shadows:
              - modelname: acme/a-model-next
                Percentage: 10
                SAMPLINGMETHOD: random
                cancelonclientdisconnect: true"
assert_renders "shadows and entry keys render in any letter case"

write_values chatCompletions "            Shadows:
              - modelName: acme/a-model-next
                percentage: 500"
assert_rejected "Shadows" "a shadows list is validated in any letter case"

# The mixed-form rule only evaluates when a shadows key is present, so a
# legacy-only route is untouched by it, even one the gateway would reject.
write_values chatCompletions "            shadowpercentage: 500
            shadowSamplingMethod: Random"
assert_renders "a legacy-only route is not touched by the shadows rules"

# The forms are exclusive in any spelling: shadows plus any legacy key, even
# zero-valued, fails.
LEGACY_KEYS=(
  "shadowModelName: acme/a-model-next"
  "shadowModelNames: []"
  "shadowPercentage: 50"
  "shadowSamplingMethod: \"\""
  "shadowCancelOnClientDisconnect: false"
  "shadowpercentage: 50"
  "SHADOWMODELNAMES: []"
)
for legacy in "${LEGACY_KEYS[@]}"; do
  write_values chatCompletions "${SHADOWS_EMPTY}
            ${legacy}"
  assert_rejected "$(first_key "${legacy}")" "shadows combined with $(first_key "${legacy}") is rejected"
done

write_values chatCompletions "            Shadows: []
            shadowPercentage: 50"
assert_rejected "shadowPercentage" "Shadows combined with a legacy key is rejected"

# Per-target entry validation.
write_values chatCompletions "            shadows:
              - percentage: 50"
assert_rejected "shadows" "a shadows entry without modelName is rejected"

write_values chatCompletions "            shadows:
              - modelName: acme/a-model-next
                modelNamee: typo"
assert_rejected "modelNamee" "a shadows entry with an unknown key is rejected"

for percentage in 0 101 50.5 '"50"' null; do
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
                cancelOnClientDisconnect: \"true\""
assert_rejected "cancelOnClientDisconnect" "a string shadows cancelOnClientDisconnect is rejected"

write_values chatCompletions "            shadows:
              - modelName: \"\""
assert_rejected "modelName" "an empty shadows modelName is rejected"

write_values chatCompletions "            shadows:
              acme/a-model-next:
                percentage: 50"
assert_rejected "shadows" "a shadows mapping is rejected"

write_values chatCompletions "            shadows: null"
assert_rejected "shadows" "a null shadows value is rejected"

write_values chatCompletions "            shadows:
              - null"
assert_rejected "shadows" "a null shadows entry is rejected"

# Multipart image endpoints take no shadows; an empty list is what an absent
# key produces, so it stays accepted.
for section in imageEdits imageVariations; do
  write_values "${section}" "${SHADOWS_MINIMAL}"
  assert_rejected "shadows" "shadows is rejected in ${section}"

  write_values "${section}" "            Shadows:
              - modelName: acme/a-model-next"
  assert_rejected "Shadows" "Shadows is rejected in ${section}"

  write_values "${section}" "${SHADOWS_EMPTY}"
  assert_renders "an empty shadows list renders in ${section}"
done

echo "All shadow schema render checks passed."
