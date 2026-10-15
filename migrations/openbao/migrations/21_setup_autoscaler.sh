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
SERVICE_ACCOUNT_NAME="nvcf-autoscaler-service"

#-------------------------------------------
# Set defaults for secret paths and policies
#-------------------------------------------
VAULT_SECRET_BASE_PATH="services/${SERVICE_ACCOUNT_NAME}"
VAULT_POLICY_BASE_PATH="services-${SERVICE_ACCOUNT_NAME}"
VAULT_JWT_AUTH_ROLE_POLICIES="services-all-kv-ro"

#--------------------------
# Create KV2 secrets engine
#--------------------------
enable_secrets_mount "${VAULT_SECRET_BASE_PATH}/kv" "kv-v2"

#-------------------------------------------
# Create default service paths and secrets
#-------------------------------------------
write_secrets_kv "${VAULT_SECRET_BASE_PATH}/kv" "cassandra/creds" "username=nvcf_autoscaler_app_v0 password=${DEFAULT_CASSANDRA_PASSWORD}"

#-------------------------------------------
# Create policy for KV access
#-------------------------------------------
policy=$(generate_acl_policy "${VAULT_SECRET_BASE_PATH}/kv/*" "read,list")
write_policy "${VAULT_POLICY_BASE_PATH}-kv-ro" "${policy}"

# append policy to auth role list
VAULT_JWT_AUTH_ROLE_POLICIES="${VAULT_JWT_AUTH_ROLE_POLICIES},${VAULT_POLICY_BASE_PATH}-kv-ro"

#-------------------------------------------
# Add Access to NVCF API via JWT Secret Role
#
# The autoscaler signs tokens against the NVCF API issuer to call NVCF gRPC
# autoscaler APIs with the unified autoscaler:auth scope. autoscaler:fetch_config
# is deprecated and should not be requested by the function autoscaler.
#-------------------------------------------

NVCF_API_SERVICE_ACCOUNT_NAMESPACE="nvcf"
NVCF_API_SERVICE_ACCOUNT_NAME="nvcf-api"
NVCF_API_SERVICE_NAME="api"
NVCF_API_SECRET_BASE_PATH="services/${NVCF_API_SERVICE_ACCOUNT_NAME}"
NVCF_API_SECRET_POLICY_PATH="services-${NVCF_API_SERVICE_ACCOUNT_NAME}"
SCOPES="autoscaler:auth"

#-------------------------------------------
# Create JWT Secret Role for NVCF API JWT Signer
#-------------------------------------------
# Issuer: http://api.nvcf.svc.cluster.local
jwt_secret_role=$(generate_jwt_secret_role "${NVCF_API_SERVICE_ACCOUNT_NAMESPACE}" "${NVCF_API_SERVICE_NAME}" "${SERVICE_ACCOUNT_NAME}" "${SCOPES}")
create_secret_jwt_role "${NVCF_API_SECRET_BASE_PATH}/jwt" "${SERVICE_ACCOUNT_NAME}" "${jwt_secret_role}"

#-------------------------------------------
# Create policy for JWT Secret Role
#-------------------------------------------
policy=$(generate_acl_policy "${NVCF_API_SECRET_BASE_PATH}/jwt/sign/${SERVICE_ACCOUNT_NAME}" "create,update,read")
policy_name="${NVCF_API_SECRET_POLICY_PATH}-jwt-sign-${SERVICE_ACCOUNT_NAME}-rw"
write_policy "${policy_name}" "${policy}"

# append policy to auth role list
VAULT_JWT_AUTH_ROLE_POLICIES="${VAULT_JWT_AUTH_ROLE_POLICIES},${policy_name}"

#-------------------------------------------
# Provision JWT Auth Role with policies
#-------------------------------------------
jwt_auth_role=$(generate_jwt_auth_role "${SERVICE_ACCOUNT_NAME}" "${SERVICE_ACCOUNT_NAMESPACE}" "$VAULT_JWT_AUTH_ROLE_POLICIES")
create_auth_jwt_role "${SERVICE_ACCOUNT_NAME}" "${jwt_auth_role}"
