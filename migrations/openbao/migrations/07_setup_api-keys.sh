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

SERVICE_ACCOUNT_NAMESPACE="api-keys"
SERVICE_ACCOUNT_NAME="api-keys-api"

# 43-char service id for NVCT registration. Must match the value stored
# under services/nvct-api/kv/identity/service-id (see 20_setup_nvct.sh)
# and the NVCT_SERVICE_ID env var consumed by NAK and NVCT.
NVCT_SERVICE_ID="nvidia-cloud-tasks-ncp-service-id-nvcttasks"

EVENT_LEDGER_SERVICE_ID="nvidia-cloud-events-ncp-service-id-ckozoh6f"

#-------------------------------------------
# Set defaults for secret paths and policies
#-------------------------------------------
VAULT_SECRET_BASE_PATH="services/${SERVICE_ACCOUNT_NAME}"
VAULT_POLICY_BASE_PATH="services-${SERVICE_ACCOUNT_NAME}"
VAULT_JWT_AUTH_ROLE_POLICIES="services-all-kv-ro"

#-------------------------------------------
# Create KV2 secrets engine
#-------------------------------------------
enable_secrets_mount "${VAULT_SECRET_BASE_PATH}/kv" "kv-v2"

#-------------------------------------------
# Create policy for KV access
#-------------------------------------------
policy=$(generate_acl_policy "${VAULT_SECRET_BASE_PATH}/kv/*" "read,list")
write_policy "${VAULT_POLICY_BASE_PATH}-kv-ro" "${policy}"

# append policy to auth role list
VAULT_JWT_AUTH_ROLE_POLICIES="${VAULT_JWT_AUTH_ROLE_POLICIES},${VAULT_POLICY_BASE_PATH}-kv-ro"


#--------------------------
# Create JWT Secret Engine
#--------------------------
enable_secrets_mount "${VAULT_SECRET_BASE_PATH}/jwt" "vault-plugin-secrets-jwt"

jwt_secret_mount_config=$(generate_jwt_secret_mount_config)
config_jwt_secret_mount_config "${VAULT_SECRET_BASE_PATH}/jwt" "${jwt_secret_mount_config}"


#-------------------------------------------
# Provision JWT Auth Role with policies
#-------------------------------------------
jwt_auth_role=$(generate_jwt_auth_role "${SERVICE_ACCOUNT_NAME}" "${SERVICE_ACCOUNT_NAMESPACE}" "$VAULT_JWT_AUTH_ROLE_POLICIES")
create_auth_jwt_role "${SERVICE_ACCOUNT_NAME}" "${jwt_auth_role}"


#-------------------------------------------
# Create default service paths and secrets
#-------------------------------------------
write_secrets_kv "${VAULT_SECRET_BASE_PATH}/kv" "cassandra/creds" "username=api_keys_api_app_v0 password=${DEFAULT_CASSANDRA_PASSWORD}"

# API Keys requires exactly 136 bytes for the key
# Generate 136 bytes and Base64URLEncode
DATA_DOMAIN_KEY=$(generate_rand_key 136 base64)
write_secrets_kv "${VAULT_SECRET_BASE_PATH}/kv" "keys/data-domain-key" "key=$DATA_DOMAIN_KEY"

SVC_VALUE=$(base64_encode '[
  {"serviceId":"nvidia-cloud-functions-ncp-service-id-aketm","serviceName":"nvcf-api","audienceServiceIds":["nvidia-cloud-functions-ncp-service-id-aketm"],"maxApiKeysPerUser":8,"maxApiKeyTtlDays":365,"maxAuthzSizeChars":2048,"minAuthzUpdateIntervalSeconds":3},
  {"serviceId":"'"${NVCT_SERVICE_ID}"'","serviceName":"nvct-api","audienceServiceIds":["'"${NVCT_SERVICE_ID}"'"],"maxApiKeysPerUser":8,"maxApiKeyTtlDays":365,"maxAuthzSizeChars":2048,"minAuthzUpdateIntervalSeconds":3},
  {"serviceId":"'"${EVENT_LEDGER_SERVICE_ID}"'","serviceName":"event-ledger","audienceServiceIds":["'"${EVENT_LEDGER_SERVICE_ID}"'"],"maxApiKeysPerUser":8,"maxApiKeyTtlDays":365,"maxAuthzSizeChars":2048,"minAuthzUpdateIntervalSeconds":3}
]')
write_secrets_kv "${VAULT_SECRET_BASE_PATH}/kv" "registrations/services" "services=$SVC_VALUE" true
