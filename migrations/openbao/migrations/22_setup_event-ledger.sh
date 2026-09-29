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
fi

SERVICE_ACCOUNT_NAMESPACE="nvcf"
SERVICE_ACCOUNT_NAME="event-ledger"

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

write_secrets_kv "${VAULT_SECRET_BASE_PATH}/kv" "cassandra/creds" "username=event_ledger_app_v0 password=${DEFAULT_CASSANDRA_PASSWORD}"

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
# Add SIS as a caller via JWT Secret Role
# Issuer: http://event-ledger.nvcf.svc.cluster.local
#-------------------------------------------

SIS_SERVICE_ACCOUNT_NAMESPACE="sis"
SIS_SERVICE_ACCOUNT_NAME="sis-api"
SCOPES="fnds:createEvent,fnds:archiveEvents"

jwt_secret_role=$(generate_jwt_secret_role "${SERVICE_ACCOUNT_NAMESPACE}" "${SERVICE_ACCOUNT_NAME}" "${SIS_SERVICE_ACCOUNT_NAME}" "${SCOPES}")
create_secret_jwt_role "${VAULT_SECRET_BASE_PATH}/jwt" "${SIS_SERVICE_ACCOUNT_NAME}" "${jwt_secret_role}"

policy=$(generate_acl_policy "${VAULT_SECRET_BASE_PATH}/jwt/sign/${SIS_SERVICE_ACCOUNT_NAME}" "create,update,read")
policy_name="${VAULT_POLICY_BASE_PATH}-jwt-sign-${SIS_SERVICE_ACCOUNT_NAME}-rw"
write_policy "${policy_name}" "${policy}"

sis_merged_policies=$(merge_jwt_role_policies "${SIS_SERVICE_ACCOUNT_NAME}" "${policy_name}")
sis_jwt_auth_role=$(generate_jwt_auth_role "${SIS_SERVICE_ACCOUNT_NAME}" "${SIS_SERVICE_ACCOUNT_NAMESPACE}" "${sis_merged_policies}")
create_auth_jwt_role "${SIS_SERVICE_ACCOUNT_NAME}" "${sis_jwt_auth_role}"

#-------------------------------------------
# Add NVCA as a caller via JWT Secret Role
# Issuer: http://event-ledger.nvcf.svc.cluster.local
#-------------------------------------------

NVCA_SERVICE_ACCOUNT_NAMESPACE="nvca-system"
NVCA_SERVICE_ACCOUNT_NAME="nvca"

jwt_secret_role=$(generate_jwt_secret_role "${SERVICE_ACCOUNT_NAMESPACE}" "${SERVICE_ACCOUNT_NAME}" "${NVCA_SERVICE_ACCOUNT_NAME}" "${SCOPES}")
create_secret_jwt_role "${VAULT_SECRET_BASE_PATH}/jwt" "${NVCA_SERVICE_ACCOUNT_NAME}" "${jwt_secret_role}"

policy=$(generate_acl_policy "${VAULT_SECRET_BASE_PATH}/jwt/sign/${NVCA_SERVICE_ACCOUNT_NAME}" "create,update,read")
policy_name="${VAULT_POLICY_BASE_PATH}-jwt-sign-${NVCA_SERVICE_ACCOUNT_NAME}-rw"
write_policy "${policy_name}" "${policy}"

nvca_merged_policies=$(merge_jwt_role_policies "${NVCA_SERVICE_ACCOUNT_NAME}" "${policy_name}")
nvca_jwt_auth_role=$(generate_jwt_auth_role "${NVCA_SERVICE_ACCOUNT_NAME}" "${NVCA_SERVICE_ACCOUNT_NAMESPACE}" "${nvca_merged_policies}")
create_auth_jwt_role "${NVCA_SERVICE_ACCOUNT_NAME}" "${nvca_jwt_auth_role}"

#-------------------------------------------
# Provision JWT Auth Role with policies
#-------------------------------------------
jwt_auth_role=$(generate_jwt_auth_role "${SERVICE_ACCOUNT_NAME}" "${SERVICE_ACCOUNT_NAMESPACE}" "${VAULT_JWT_AUTH_ROLE_POLICIES}")
create_auth_jwt_role "${SERVICE_ACCOUNT_NAME}" "${jwt_auth_role}"
