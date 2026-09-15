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

curr_dir="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
if [ ! -f "${curr_dir}/utils/utils.sh" ]; then
    echo "Error: log.sh not found in ${curr_dir}/utils/utils.sh"
    return 1
else
  source "${curr_dir}/utils/utils.sh" 
  source "${curr_dir}/utils/functions.sh"
  source "${curr_dir}/utils/encryption_setup.sh"
fi


SERVICE_ACCOUNT_NAMESPACE="nvcf"
SERVICE_ACCOUNT_NAME="nvcf-notary"


#-------------------------------------------
# Set defaults for secret paths and policies
#-------------------------------------------
VAULT_SECRET_BASE_PATH="services/${SERVICE_ACCOUNT_NAME}"
VAULT_POLICY_BASE_PATH="services-${SERVICE_ACCOUNT_NAME}"
VAULT_JWT_AUTH_ROLE_POLICIES=""

#--------------------------
# Create KV2 secrets engine
#--------------------------
enable_secrets_mount "${VAULT_SECRET_BASE_PATH}/kv" "kv-v2"

#-------------------------------------------
# Create default service paths and secrets
#-------------------------------------------
SIGNING_KEY_KID=$(generate_kid)
SIGNING_KEY=$(generate_base64_encoded_json $(generate_asymmetric_signing_key $SIGNING_KEY_KID))

# Write signing key secrets to vault
write_secrets_kv "${VAULT_SECRET_BASE_PATH}/kv" "keys/signing-key" "keys=$SIGNING_KEY kid=$SIGNING_KEY_KID alg=ES256"

#-------------------------------------------
# Create policy for KV access
#-------------------------------------------
policy=$(generate_acl_policy "${VAULT_SECRET_BASE_PATH}/kv/*" "read,list")
write_policy "${VAULT_POLICY_BASE_PATH}-kv-ro" "${policy}"

# append policy to auth role list
VAULT_JWT_AUTH_ROLE_POLICIES="${VAULT_JWT_AUTH_ROLE_POLICIES},${VAULT_POLICY_BASE_PATH}-kv-ro"

#--------------------------
# Provision JWT Auth Role with policies
#--------------------------
jwt_auth_role=$(generate_jwt_auth_role "${SERVICE_ACCOUNT_NAME}" "${SERVICE_ACCOUNT_NAMESPACE}" "$VAULT_JWT_AUTH_ROLE_POLICIES")
create_auth_jwt_role "${SERVICE_ACCOUNT_NAME}" "${jwt_auth_role}"
