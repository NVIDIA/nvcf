#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0


SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/environment.sh"

echo -e "==================="
echo "Delete Function"
date
echo -e "==================="

read -r FUNCTION_ID FUNCTION_VERSION_ID < <(cat deploy_result.json | jq -r '[ .function.id, .function.versionId] | join(" ")')
echo "FUNCTION_ID=${FUNCTION_ID}, FUNCTION_VERSION_ID=${FUNCTION_VERSION_ID}"

ngc cf function remove \
    $FUNCTION_ID:$FUNCTION_VERSION_ID \
    --org ${NGC_CLI_ORG} --team ${NGC_CLI_TEAM}
