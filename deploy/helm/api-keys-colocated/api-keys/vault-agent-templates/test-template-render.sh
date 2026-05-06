#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     https://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.


# Always run from the script's directory
cd "$(dirname "$0")"

# Automated test script for template rendering
# Tests the template logic without requiring Vault integration

set -e

echo "Template Test Script"
echo "======================"

# Create expected output
cat > expected-output.json << 'EOF'
{
  "kv": {
    "registrations": {
      "services": "[{\"serviceId\":\"nvidia-cloud-functions-ncp-service-id-aketm\",\"serviceName\":\"test-service\",\"audienceServiceIds\":[\"nvidia-cloud-functions-ncp-service-id-aketm\"],\"maxApiKeysPerUser\":8,\"maxApiKeyTtlDays\":365,\"maxAuthzSizeChars\":2048,\"minAuthzUpdateIntervalSeconds\":3}]"
    }
  }
}
EOF

echo "1. Rendering template with consul-template..."
echo "============================================="

# Render the template
consul-template -template 'test-template-mock.tmpl:actual-output.json' -once

echo "✓ Template rendered successfully"
echo ""
echo "Full rendered output:"
cat actual-output.json

echo "2. Validating rendered output..."
echo "================================"

# Check if output file was created
if [ ! -f actual-output.json ]; then
    echo "✗ Error: actual-output.json was not created"
    exit 1
fi

# Validate JSON syntax
if ! jq empty actual-output.json 2>/dev/null; then
    echo "✗ Error: actual-output.json is not valid JSON"
    exit 1
fi

echo "✓ Output is valid JSON"
echo ""

echo "3. Comparing expected vs actual output..."
echo "========================================="

# Sort both files for comparison
jq --sort-keys . expected-output.json > expected-sorted.json
jq --sort-keys . actual-output.json > actual-sorted.json

# Compare the files
if diff -u expected-sorted.json actual-sorted.json; then
    echo ""
    echo "✅ TEST PASSED: Template output matches expected output"
    echo ""
    
    echo "4. Detailed output validation..."
    echo "================================"
    
    echo "Registrations value:"
    jq -r '.kv.registrations' actual-output.json | jq .
    echo ""
else
    echo ""
    echo "❌ TEST FAILED: Template output does not match expected output"
    echo ""
    echo "Expected output:"
    cat expected-output.json
    echo ""
    echo "Actual output:"
    cat actual-output.json
    echo ""
    exit 1
fi

echo "5. Cleanup..."
echo "============="

# Cleanup
rm -f actual-output.json expected-output.json expected-sorted.json actual-sorted.json

echo "✓ Test files cleaned up"
echo ""
echo "🎉 All tests completed successfully!" 
