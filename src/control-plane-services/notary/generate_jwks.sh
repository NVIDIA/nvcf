#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
# http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
set -e

# Debug mode flag - default is disabled
DEBUG=false

# Function to generate a timestamp-based key ID in format "kid-yyyyMMdd-HHmm"
generate_kid() {
  echo "kid-$(date '+%Y%m%d-%H%M')"
}

# Function to base64url encode
base64url_encode() {
  base64 | tr '/+' '_-' | tr -d '=' | tr -d '\n'
}

# Generate a new signing key
generate_signing_key() {
  # Generate timestamp (seconds since epoch)
  TIMESTAMP=$(date +%s)
  
  # Generate a key ID with timestamp format
  KID=$(generate_kid)
  
  # Create a temporary directory
  TEMP_DIR=$(mktemp -d)
  
  # Set up cleanup to execute when the script exits
  # This ensures the temporary directory is removed regardless of how the script exits:
  # - Normal termination
  # - Error conditions that cause exit
  # - User interruption (like Ctrl+C)
  # - Explicit exit commands
  trap 'rm -rf "$TEMP_DIR"' EXIT
  
  # Redirect stderr based on debug mode
  if [ "$DEBUG" = true ]; then
    REDIRECT=""
  else
    REDIRECT="2>/dev/null"
  fi
  
  # Generate EC private key
  eval "openssl ecparam -name prime256v1 -genkey -noout -out \"$TEMP_DIR/private.pem\" $REDIRECT"
  
  # Extract public key
  eval "openssl ec -in \"$TEMP_DIR/private.pem\" -pubout -out \"$TEMP_DIR/public.pem\" $REDIRECT"
  
  # Extract private key in DER format
  eval "openssl ec -in \"$TEMP_DIR/private.pem\" -outform DER -out \"$TEMP_DIR/private.der\" $REDIRECT"
  
  # Extract parameters from the key
  # First, get the private key 'd' value (last 32 bytes of DER)
  D_VALUE=$(tail -c 32 "$TEMP_DIR/private.der" | base64url_encode)
  
  # Extract X and Y from public key
  # The output format from OpenSSL is: 04 + X-coordinate (64 hex chars) + Y-coordinate (64 hex chars)
  # where 04 indicates uncompressed point format for EC keys
  PUB_HEX=$(eval "openssl ec -in \"$TEMP_DIR/private.pem\" -text -noout $REDIRECT" | grep -A 3 "pub:" | tail -n 3 | tr -d ' \n:' | sed 's/^.*pub//g')
  # Skip the first 2 chars (04 prefix) and take the next 64 chars (32 bytes) for X coordinate
  X_HEX=${PUB_HEX:2:64}
  # Skip 2 chars for prefix + 64 chars for X (total 66) and take the next 64 chars for Y coordinate
  Y_HEX=${PUB_HEX:66:64}
  
  # Convert to base64url
  X_B64=$(echo -n "$X_HEX" | xxd -r -p | base64url_encode)
  Y_B64=$(echo -n "$Y_HEX" | xxd -r -p | base64url_encode)
  
  # Create JSON output
  cat <<EOF
{"kty":"EC","d":"$D_VALUE","use":"sig","crv":"P-256","kid":"$KID","x":"$X_B64","y":"$Y_B64","alg":"ES256","iat":$TIMESTAMP}
EOF
}

# Generate a key set
generate_key_set() {
  KEY=$(generate_signing_key)
  cat <<EOF
{"keys":[$KEY]}
EOF
}

# Generate an escaped key set for vault
generate_key_set_escaped() {
  KEY_SET=$(generate_key_set)
  # Replace each " with \\\" to match Java's escaping format (3 backslashes)
  ESCAPED_SET="${KEY_SET//\"/\\\\\\\"}"
  echo "$ESCAPED_SET"
}

# Display usage
usage() {
  echo "Usage: $0 [OPTION]"
  echo "Generate keys for Kaizen Notary Service"
  echo ""
  echo "Options:"
  echo "  -k, --key         Generate a single signing key"
  echo "  -s, --keyset      Generate a key set (default)"
  echo "  -e, --escaped     Generate an escaped key set for vault"
  echo "  -d, --debug       Enable debug output"
  echo "  -h, --help        Display this help message"
  exit 1
}

# Default action
ACTION="keyset"

# Parse arguments
while [[ $# -gt 0 ]]; do
  case $1 in
    -k|--key)
      ACTION="key"
      shift
      ;;
    -s|--keyset)
      ACTION="keyset"
      shift
      ;;
    -e|--escaped)
      ACTION="escaped"
      shift
      ;;
    -d|--debug)
      DEBUG=true
      shift
      ;;
    -h|--help)
      usage
      ;;
    *)
      echo "Unknown option: $1"
      usage
      ;;
  esac
done

# Execute the requested action
case $ACTION in
  "key")
    generate_signing_key
    ;;
  "keyset")
    generate_key_set
    ;;
  "escaped")
    generate_key_set_escaped
    ;;
esac 