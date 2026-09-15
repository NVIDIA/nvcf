#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

# Multi-Token Authentication Test Script
# This script demonstrates the multi-token authentication feature

echo "NVIDIA Cloud Functions - Multi-Token Authentication Test"
echo "=========================================================="

# Set up different tokens for different operations
export NVCF_API_KEY="your-general-operations-token-here"
export NVCF_TOKEN="your-function-creation-token-here"

echo ""
echo "Configuration:"
echo "  NVCF_API_KEY: ${NVCF_API_KEY:0:20}..."
echo "  NVCF_TOKEN: ${NVCF_TOKEN:0:20}..."
echo ""

echo "Testing CREATE function (should use NVCF_TOKEN):"
echo "----------------------------------------------------------------"
./nvcf-cli create --debug \
  --name "multi-token-test" \
  --image "nginx:latest" \
  --inference-url "http://0.0.0.0:8000/predict" \
  --inference-port 8000 \
  --description "Testing multi-token authentication"

echo ""
echo "Expected debug output:"
echo "  DEBUG: Using FUNCTION TOKEN for POST /v2/nvcf/accounts/nvcf-default/functions"
echo ""

# The following commands would use NVCF_API_KEY:
# - deploy: ./nvcf-cli deploy function-id version-id  
# - invoke: ./nvcf-cli invoke function-id version-id '{"input": "test"}'
# - delete: ./nvcf-cli delete function-id version-id

echo "Multi-token authentication test complete!"
echo ""
echo "Key Features:"
echo "  Create operations use NVCF_TOKEN"
echo "  Other operations use NVCF_API_KEY"
echo "  Automatic token selection based on operation type"
echo "  Graceful fallback when only one token is available"
echo "  Full debug visibility into token selection"
