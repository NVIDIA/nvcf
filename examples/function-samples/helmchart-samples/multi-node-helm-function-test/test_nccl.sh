#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -e

# Load configuration from config.env
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CONFIG_FILE="${SCRIPT_DIR}/config.env"

if [ ! -f "$CONFIG_FILE" ]; then
    echo "Error: config.env not found!"
    echo "Please create config.env from config.env.sample:"
    echo "  cp config.env.sample config.env"
    echo "Then edit config.env with your API key and function ID."
    exit 1
fi

source "$CONFIG_FILE"

# Validate required variables
if [ -z "$KEY" ] || [ "$KEY" = "your-api-key-here" ]; then
    echo "Error: KEY not set in config.env"
    exit 1
fi

if [ -z "$FUNCTION_ID" ] || [ "$FUNCTION_ID" = "your-multi-node-function-id" ] || [ "$FUNCTION_ID" = "your-single-node-function-id" ]; then
    echo "Error: FUNCTION_ID not set in config.env"
    exit 1
fi

CLUSTER_TYPE="ncp-mlx5" # "aws-gb200" for AWS/EFA, "aws-gb300" for AWS GB300, "ncp-mlx5" for NCP/Mellanox
NUM_GPUS=72 # number of GPUs to test, must match at most the number of GPUs in the helm chart

RESPONSE=$(curl --silent --request POST \
--url https://${FUNCTION_ID}.invocation.api.nvcf.nvidia.com/nccl-test \
--header "Authorization: Bearer ${KEY}" \
--header "NVCF-POLL-SECONDS: 600" \
--header 'Content-Type: application/json' \
--data "{
  \"np\": ${NUM_GPUS},
  \"n\": \"20\",
  \"b\": \"1K\",
  \"e\": \"16G\",
  \"f\": \"2\",
  \"g\": \"1\",
  \"npernode\": 4,
  \"mnnvl\": true,
  \"debug\": false,
  \"cluster_type\": \"${CLUSTER_TYPE}\"
}")

echo "$RESPONSE"

echo ""
echo "======================================================================================================"
echo " NCCL Test Output"
echo "======================================================================================================"
echo "$RESPONSE" | jq -r '.command'
echo "$RESPONSE" | jq -r '.output'
echo ""
echo "======================================================================================================"

# save the formatted response to a file
echo "$RESPONSE" | jq '{command, output}' > nccl_test_response.json
