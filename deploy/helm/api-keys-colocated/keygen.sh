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


# Exit on any error
set -e

# Function to generate a random kid (Key ID)
generate_kid() {
    date_str=$(date +"%Y%m%d%H%M%S")
    echo "kid-$date_str"
}

# Function to check if OpenSSL is available
check_openssl() {
    if ! command -v openssl &> /dev/null; then
        echo "Error: OpenSSL is required but not installed."
        exit 1
    fi
}

# Function to generate data domain key
generate_domain_key() {
    # Generate 2048 bits (256 bytes) random key and encode to base64url
    local raw_key=$(openssl rand -base64 256)
    echo -n "$raw_key" | tr '/+' '_-' | tr -d '=\n'
}

# Main function to generate JWK
generate_jwk() {
    # Generate 256-bit (32 byte) random key
    local raw_key=$(openssl rand -base64 32)
    
    # Convert to base64url encoding (replace / with _, + with -, remove =)
    local base64url_key=$(echo -n "$raw_key" | tr '/+' '_-' | tr -d '=\n')
    
    # Generate key ID
    local kid=$(generate_kid)
    
    # Create JWK JSON
    echo -n "{\"kty\":\"oct\",\"use\":\"enc\",\"kid\":\"$kid\",\"k\":\"$base64url_key\",\"alg\":\"A256GCM\"}"
}

# Function to generate JWKS
generate_jwks() {
    local jwk=$(generate_jwk)
    echo -n "{\"keys\":[$jwk]}"
}

# Check dependencies
check_openssl

# Generate and output both keys
echo "=== JWKS for A256GCM ==="
generate_jwks

echo -e "\n=== Data Domain Key ==="
echo "{\"value\":\"$(generate_domain_key)\"}" 