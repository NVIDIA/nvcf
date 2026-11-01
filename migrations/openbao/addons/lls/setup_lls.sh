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


set -euo pipefail

# Source utilities from the migrations directory
migrations_dir="$( cd "$( dirname "${BASH_SOURCE[0]}" )/../../migrations" && pwd )"
if [ ! -f "${migrations_dir}/utils/utils.sh" ]; then
    echo "Error: utils.sh not found in ${migrations_dir}/utils/utils.sh"
    return 1
else
  source "${migrations_dir}/utils/utils.sh"
  source "${migrations_dir}/utils/functions.sh"
  source "${migrations_dir}/utils/encryption_setup.sh"
fi

SERVICE_ACCOUNT_NAMESPACE="gdn-streaming"
SERVICE_ACCOUNT_NAME="turn"

#-------------------------------------------
# Set defaults for secret paths and policies
#-------------------------------------------
VAULT_SECRET_BASE_PATH="services/${SERVICE_ACCOUNT_NAME}"
VAULT_POLICY_BASE_PATH="services-${SERVICE_ACCOUNT_NAME}"

#--------------------------
# Create KV2 secrets engine
#--------------------------
log_section "Setting up LLS/TURN service"
enable_secrets_mount "${VAULT_SECRET_BASE_PATH}/kv" "kv-v2"

#-------------------------------------------
# Create HMAC key for TURN credential signing
#-------------------------------------------
log_step "Generating HMAC key for TURN credential signing"

HMAC_KEY_ID=$(generate_kid)
HMAC_KEY_TIMESTAMP=$(date -u +"%Y-%m-%dT%H:%M:%S+00:00")
HMAC_KEY_VALUE=$(generate_rand_key 32 hex)

write_secrets_kv "${VAULT_SECRET_BASE_PATH}/kv" "keys/hmac" \
  "id=${HMAC_KEY_ID} timestamp=${HMAC_KEY_TIMESTAMP} value=${HMAC_KEY_VALUE}"

#-------------------------------------------
# Configure version limit on HMAC secret
# Keep max 3 versions (current + 2 previous)
#-------------------------------------------
log_step "Configuring version limit on HMAC secret (max_versions=3)"
if ! output=$(bao kv metadata put -max-versions=3 "${VAULT_SECRET_BASE_PATH}/kv/keys/hmac" 2>&1); then
    log_error "Error setting max_versions on HMAC secret: $output"
else
    log_success "Version limit configured successfully"
fi

#-------------------------------------------
# Create public-dns-certificate secret path (placeholder)
# TURN uses kv.public-dns-certificate.current.value.cert (base64 PEM).
# Overwrite with real cert via: bao kv put services/turn/kv/keys/public-dns-certificate cert="<base64-pem>"
# write_secrets_kv uses overwrite=false by default, so re-run or existing cert is not overwritten.
#-------------------------------------------
log_step "Creating public-dns-certificate secret path (placeholder)"
# Placeholder: base64 of "# placeholder" so key exists; replace with real PEM via bao kv put
write_secrets_kv "${VAULT_SECRET_BASE_PATH}/kv" "keys/public-dns-certificate" "cert=$(echo -n '# placeholder' | base64)"

log_step "Configuring version limit on public-dns-certificate (max_versions=2)"
if ! output=$(bao kv metadata put -max-versions=2 "${VAULT_SECRET_BASE_PATH}/kv/keys/public-dns-certificate" 2>&1); then
    log_error "Error setting max_versions on public-dns-certificate: $output"
else
    log_success "Version limit configured successfully"
fi

#-------------------------------------------
# Create Redis credential secret path (placeholder)
# External TURN/LLS chart logic is expected to replace this with a real password.
#-------------------------------------------
log_step "Creating Redis credential secret path (placeholder)"
write_secrets_kv "${VAULT_SECRET_BASE_PATH}/kv" "redis/creds" "password=replace"

log_step "Configuring version limit on redis/creds (max_versions=2)"
if ! output=$(bao kv metadata put -max-versions=2 "${VAULT_SECRET_BASE_PATH}/kv/redis/creds" 2>&1); then
    log_error "Error setting max_versions on redis/creds: $output"
else
    log_success "Version limit configured successfully"
fi

#-------------------------------------------
# Create policy for KV read access
#-------------------------------------------
policy=$(generate_acl_policy "${VAULT_SECRET_BASE_PATH}/kv/*" "read,list")
write_policy "${VAULT_POLICY_BASE_PATH}-kv-ro" "${policy}"

VAULT_JWT_AUTH_ROLE_POLICIES="${VAULT_POLICY_BASE_PATH}-kv-ro"

#-------------------------------------------
# Create policy for KV metadata access (needed for version iteration)
#-------------------------------------------
metadata_policy=$(generate_acl_policy "${VAULT_SECRET_BASE_PATH}/kv/metadata/*" "read,list")
write_policy "${VAULT_POLICY_BASE_PATH}-kv-metadata-ro" "${metadata_policy}"

VAULT_JWT_AUTH_ROLE_POLICIES="${VAULT_JWT_AUTH_ROLE_POLICIES},${VAULT_POLICY_BASE_PATH}-kv-metadata-ro"

#--------------------------
# Provision JWT Auth Role with policies
#--------------------------
jwt_auth_role=$(generate_jwt_auth_role "${SERVICE_ACCOUNT_NAME}" "${SERVICE_ACCOUNT_NAMESPACE}" "$VAULT_JWT_AUTH_ROLE_POLICIES")
create_auth_jwt_role "${SERVICE_ACCOUNT_NAME}" "${jwt_auth_role}"

log_success "LLS/TURN service setup complete"
