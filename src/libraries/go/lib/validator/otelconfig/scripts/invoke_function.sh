#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0


SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/environment.sh"

echo -e "==================="
echo "Invoke Function"
date
echo -e "==================="
RETRY_COUNT=0
MAX_RETRIES=3

read -r FUNCTION_ID FUNCTION_VERSION_ID < <(cat deploy_result.json | jq -r '[ .function.id, .function.versionId] | join(" ")')
echo "FUNCTION_ID=${FUNCTION_ID}, FUNCTION_VERSION_ID=${FUNCTION_VERSION_ID}"

TEMP_FILE=$(mktemp)
HTTP_STATUS=$(curl -sS -w "%{http_code}" -o "$TEMP_FILE" --location "$NVCF_API_URL/v2/nvcf/pexec/functions/$FUNCTION_ID/versions/$FUNCTION_VERSION_ID" \
     --header "Content-Type: application/json" \
     --header "Authorization: Bearer $NGC_CLI_API_KEY" \
     --retry 5 \
     --retry-delay 5 \
     --retry-max-time 30 \
     --data '{}')

cat "$TEMP_FILE"
rm -f "$TEMP_FILE"

if [[ $HTTP_STATUS -ge 300 ]]; then
     exit 1
fi
