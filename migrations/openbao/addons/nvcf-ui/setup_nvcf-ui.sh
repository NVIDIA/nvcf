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
fi

log_section "Setting up nvcf-ui service"

SERVICE_ACCOUNT_NAMESPACE="nvcf-ui"
SERVICE_ACCOUNT_NAME="nvcf-ui"

#-------------------------------------------
# Add Access to Spot Instance Service API via JWT Secret Role
#-------------------------------------------

sis_namespace="sis"
sis_service="api"
sis_account="sis-api"
sis_secret_base="services/${sis_account}"
sis_policy_base="services-${sis_account}"
SCOPES="attributes_listing,cluster-management,clusters_listing,cluster_listing,gpu_listing,instance_types,regions_listing"

# Issuer: http://api.sis.svc.cluster.local
jwt_secret_role=$(generate_jwt_secret_role "${sis_namespace}" "${sis_service}" "${SERVICE_ACCOUNT_NAME}" "${SCOPES}" "${JWT_ISSUER_OVERRIDE:-}")
create_secret_jwt_role "${sis_secret_base}/jwt" "${SERVICE_ACCOUNT_NAME}" "${jwt_secret_role}"

policy=$(generate_acl_policy "${sis_secret_base}/jwt/sign/${SERVICE_ACCOUNT_NAME}" "create,update,read")
policy_name="${sis_policy_base}-jwt-sign-${SERVICE_ACCOUNT_NAME}-rw"
write_policy "${policy_name}" "${policy}"

VAULT_JWT_AUTH_ROLE_POLICIES="${policy_name}"


#-------------------------------------------
# Add Access to NVCF API via JWT Secret Role
#-------------------------------------------

NVCF_API_SERVICE_ACCOUNT_NAMESPACE="nvcf"
NVCF_API_SERVICE_NAME="api"
NVCF_API_SERVICE_ACCOUNT_NAME="nvcf-api"
NVCF_API_SECRET_BASE_PATH="services/${NVCF_API_SERVICE_ACCOUNT_NAME}"
NVCF_API_SECRET_POLICY_PATH="services-${NVCF_API_SERVICE_ACCOUNT_NAME}"
SCOPES="invoke_function,account_setup,admin:queue_details,admin:delete_function,admin:deploy_function,admin:list_cluster_groups,admin:list_functions,admin:update_function,admin:list_functions_details,admin:manage_registries,admin:manage_telemetries,admin:register_function,admin:update_secrets,admin:manage_registry_credentials"

# Issuer: http://api.nvcf.svc.cluster.local
jwt_secret_role=$(generate_jwt_secret_role "${NVCF_API_SERVICE_ACCOUNT_NAMESPACE}" "${NVCF_API_SERVICE_NAME}" "${SERVICE_ACCOUNT_NAME}" "${SCOPES}")
create_secret_jwt_role "${NVCF_API_SECRET_BASE_PATH}/jwt" "${SERVICE_ACCOUNT_NAME}" "${jwt_secret_role}"

policy=$(generate_acl_policy "${NVCF_API_SECRET_BASE_PATH}/jwt/sign/${SERVICE_ACCOUNT_NAME}" "create,update,read")
policy_name="${NVCF_API_SECRET_POLICY_PATH}-jwt-sign-${SERVICE_ACCOUNT_NAME}-rw"
write_policy "${policy_name}" "${policy}"

# append policy to auth role list
VAULT_JWT_AUTH_ROLE_POLICIES="${VAULT_JWT_AUTH_ROLE_POLICIES},${policy_name}"


#-------------------------------------------
# Add Access to NVCT API via JWT Secret Role
#
# The NVCT JWT secrets mount only exists to let the UI mint NVCT tokens, so the
# addon owns it. Enable it here (idempotent) instead of in core migration
# 20_setup_nvct.sh, keeping the NVCT signing path out of non-UI installs.
#-------------------------------------------

NVCT_API_SERVICE_ACCOUNT_NAMESPACE="nvcf"
NVCT_API_SERVICE_NAME="nvct-api"
NVCT_API_SERVICE_ACCOUNT_NAME="nvct-api"
NVCT_API_SECRET_BASE_PATH="services/${NVCT_API_SERVICE_ACCOUNT_NAME}"
NVCT_API_SECRET_POLICY_PATH="services-${NVCT_API_SERVICE_ACCOUNT_NAME}"
SCOPES="admin:cancel_task,admin:delete_task,admin:launch_task,admin:list_tasks,admin:task_details,admin:update_secrets,admin:list_events,admin:list_results"

# Create the NVCT JWT secret engine (mounted only when the UI addon runs)
enable_secrets_mount "${NVCT_API_SECRET_BASE_PATH}/jwt" "vault-plugin-secrets-jwt"

jwt_secret_mount_config=$(generate_jwt_secret_mount_config)
config_jwt_secret_mount_config "${NVCT_API_SECRET_BASE_PATH}/jwt" "${jwt_secret_mount_config}"

# Issuer: http://nvct-api.nvcf.svc.cluster.local
jwt_secret_role=$(generate_jwt_secret_role "${NVCT_API_SERVICE_ACCOUNT_NAMESPACE}" "${NVCT_API_SERVICE_NAME}" "${SERVICE_ACCOUNT_NAME}" "${SCOPES}")
create_secret_jwt_role "${NVCT_API_SECRET_BASE_PATH}/jwt" "${SERVICE_ACCOUNT_NAME}" "${jwt_secret_role}"

policy=$(generate_acl_policy "${NVCT_API_SECRET_BASE_PATH}/jwt/sign/${SERVICE_ACCOUNT_NAME}" "create,update,read")
policy_name="${NVCT_API_SECRET_POLICY_PATH}-jwt-sign-${SERVICE_ACCOUNT_NAME}-rw"
write_policy "${policy_name}" "${policy}"

# append policy to auth role list
VAULT_JWT_AUTH_ROLE_POLICIES="${VAULT_JWT_AUTH_ROLE_POLICIES},${policy_name}"


#-------------------------------------------
# Provision JWT Auth Role with policies
#-------------------------------------------
jwt_auth_role=$(generate_jwt_auth_role "${SERVICE_ACCOUNT_NAME}" "${SERVICE_ACCOUNT_NAMESPACE}" "$VAULT_JWT_AUTH_ROLE_POLICIES")
create_auth_jwt_role "${SERVICE_ACCOUNT_NAME}" "${jwt_auth_role}"

log_success "nvcf-ui service setup complete"
