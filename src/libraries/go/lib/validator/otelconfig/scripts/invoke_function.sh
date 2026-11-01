#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0


set -euo pipefail

: "${NGC_CLI_API_KEY:?NGC_CLI_API_KEY environment variable is not set}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/environment.sh"

echo -e "==================="
echo "Invoke Function"
date
echo -e "==================="

if [[ ! -r deploy_result.json ]]; then
     echo "Error: deploy_result.json not found or not readable" >&2
     exit 1
fi
if ! read -r FUNCTION_ID FUNCTION_VERSION_ID < <(jq -r '[ .function.id, .function.versionId] | join(" ")' deploy_result.json); then
     echo "Error: Failed to parse deploy_result.json" >&2
     exit 1
fi
if [[ -z "$FUNCTION_ID" || "$FUNCTION_ID" == "null" || -z "$FUNCTION_VERSION_ID" || "$FUNCTION_VERSION_ID" == "null" ]]; then
     echo "Error: Failed to extract function ID and version ID from deploy_result.json" >&2
     exit 1
fi
echo "FUNCTION_ID=${FUNCTION_ID}, FUNCTION_VERSION_ID=${FUNCTION_VERSION_ID}"

TEMP_FILE=$(mktemp)
trap 'rm -f "$TEMP_FILE"' EXIT
if [[ -z "${NVCF_INVOKE_URL:-}" && -z "${NVCF_API_URL:-}" ]]; then
     echo "Error: Either NVCF_INVOKE_URL or NVCF_API_URL must be set" >&2
     exit 1
fi
if [[ -z "${FUNCTION_INFERENCE_PATH:-}" ]]; then
     echo "Error: FUNCTION_INFERENCE_PATH must be set" >&2
     exit 1
fi
if [[ "$FUNCTION_INFERENCE_PATH" != /* ]]; then
     echo "Error: FUNCTION_INFERENCE_PATH must start with /" >&2
     exit 1
fi
INVOKE_BASE_URL="${NVCF_INVOKE_URL:-$NVCF_API_URL}"
INVOKE_SCHEME="${INVOKE_BASE_URL%%://*}"
INVOKE_HOST="${INVOKE_BASE_URL#*://}"
if [[ "$INVOKE_SCHEME" == "$INVOKE_BASE_URL" ]]; then
     INVOKE_SCHEME="https"
     INVOKE_HOST="$INVOKE_BASE_URL"
fi
INVOKE_HOST="${INVOKE_HOST%%/*}"
if [[ "$INVOKE_HOST" == "$FUNCTION_ID."* ]]; then
     FUNCTION_INVOKE_HOST="$INVOKE_HOST"
elif [[ "$INVOKE_HOST" == invocation.* ]]; then
     FUNCTION_INVOKE_HOST="${FUNCTION_ID}.${INVOKE_HOST}"
elif [[ "$INVOKE_HOST" == api.* ]]; then
     FUNCTION_INVOKE_HOST="${FUNCTION_ID}.invocation.${INVOKE_HOST#api.}"
else
     FUNCTION_INVOKE_HOST="${FUNCTION_ID}.invocation.${INVOKE_HOST}"
fi
FUNCTION_INVOKE_URL="${INVOKE_SCHEME}://${FUNCTION_INVOKE_HOST}${FUNCTION_INFERENCE_PATH}"

CURL_HEADERS=(
     --header "Authorization: Bearer $NGC_CLI_API_KEY"
     --header "Content-Type: application/json"
)

if [[ -n "${FUNCTION_POLL_DURATION_SECONDS:-}" && "$FUNCTION_POLL_DURATION_SECONDS" != "0" ]]; then
     CURL_HEADERS+=(--header "NVCF-POLL-SECONDS: $FUNCTION_POLL_DURATION_SECONDS")
fi

HTTP_STATUS=$(curl -sS -w "%{http_code}" -o "$TEMP_FILE" --location "$FUNCTION_INVOKE_URL" \
     "${CURL_HEADERS[@]}" \
     --retry 5 \
     --retry-delay 5 \
     --retry-max-time 30 \
     --data '{}')

cat "$TEMP_FILE"

if [[ $HTTP_STATUS -ge 300 ]]; then
     echo "Error: Function invocation failed with HTTP status $HTTP_STATUS" >&2
     exit 1
fi
