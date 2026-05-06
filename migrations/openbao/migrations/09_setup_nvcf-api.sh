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
SERVICE_ACCOUNT_NAME="nvcf-api"
SERVICE_NAME="api"

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
write_secrets_kv "${VAULT_SECRET_BASE_PATH}/kv" "cassandra/creds" "username=nvcf_api_app_v0 password=${DEFAULT_CASSANDRA_PASSWORD}"
write_secrets_kv "${VAULT_SECRET_BASE_PATH}/kv" "sidecars/image-pull-secret" "secret=${NVCF_API_SIDECARS_IMAGE_PULL_SECRET:-replace}"

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
# Create JWT Secret Role for Admin with extended scopes
#-------------------------------------------
NVCF_NCA_BOOTSTRAP_SERVICE_ACCOUNT_NAME="nvcf-api-account-bootstrap"
scopes="account_setup,admin:register_function,admin:list_functions,admin:list_functions_details,admin:deploy_function,admin:update_function,admin:update_secrets,admin:delete_function,admin:manage_telemetries,admin:manage_registry_credentials"
# Issuer: http://api.nvcf.svc.cluster.local
jwt_secret_role=$(generate_jwt_secret_role "${SERVICE_ACCOUNT_NAMESPACE}" "${SERVICE_NAME}" "${NVCF_NCA_BOOTSTRAP_SERVICE_ACCOUNT_NAME}" "${scopes}")
create_secret_jwt_role "${VAULT_SECRET_BASE_PATH}/jwt" "${SERVICE_ACCOUNT_NAME}-admin" "${jwt_secret_role}"

#-------------------------------------------
# Create JWT Secret Role for Self to use with Notary
#-------------------------------------------
scopes="notary-sign,make-assertion"
# Issuer: http://api.nvcf.svc.cluster.local
# The iss is the api - the notary service is configured to use this as the issuer for inbound tokens
# Notary uses the api JWKS for token validation
jwt_secret_role=$(generate_jwt_secret_role "${SERVICE_ACCOUNT_NAMESPACE}" "${SERVICE_NAME}" "${SERVICE_ACCOUNT_NAME}" "${scopes}")
create_secret_jwt_role "${VAULT_SECRET_BASE_PATH}/jwt" "${SERVICE_ACCOUNT_NAME}" "${jwt_secret_role}"

#-------------------------------------------
# Create policy for JWT Secret Role
#-------------------------------------------
policy=$(generate_acl_policy "${VAULT_SECRET_BASE_PATH}/jwt/sign/${SERVICE_ACCOUNT_NAME}" "create,update,read")
policy_name="${VAULT_POLICY_BASE_PATH}-jwt-sign-${SERVICE_ACCOUNT_NAME}-rw"
write_policy "${policy_name}" "${policy}"

# append policy to auth role list
VAULT_JWT_AUTH_ROLE_POLICIES="${VAULT_JWT_AUTH_ROLE_POLICIES},${policy_name}"




#-------------------------------------------
# Add Access to SIS API via JWT Secret Role
#-------------------------------------------

SIS_API_SERVICE_ACCOUNT_NAMESPACE="sis"
SIS_API_SERVICE_ACCOUNT_NAME="sis-api"
SIS_API_SERVICE_NAME="api"
SIS_API_SECRET_BASE_PATH="services/${SIS_API_SERVICE_ACCOUNT_NAME}"
SIS_API_SECRET_POLICY_PATH="services-${SIS_API_SERVICE_ACCOUNT_NAME}"
SCOPES="spot-request,cluster_listing,instance_types,cluster-management"

#-------------------------------------------
# Create JWT Secret Role for NVCF JWT Signer
#-------------------------------------------
# Issuer: http://api.sis.svc.cluster.local
jwt_secret_role=$(generate_jwt_secret_role "${SIS_API_SERVICE_ACCOUNT_NAMESPACE}" "${SIS_API_SERVICE_NAME}" "${SERVICE_ACCOUNT_NAME}" "${SCOPES}")
create_secret_jwt_role "${SIS_API_SECRET_BASE_PATH}/jwt" "${SERVICE_ACCOUNT_NAME}" "${jwt_secret_role}"

#-------------------------------------------
# Create policy for JWT Secret Role
#-------------------------------------------
policy=$(generate_acl_policy "${SIS_API_SECRET_BASE_PATH}/jwt/sign/${SERVICE_ACCOUNT_NAME}" "create,update,read")
policy_name="${SIS_API_SECRET_POLICY_PATH}-jwt-sign-${SERVICE_ACCOUNT_NAME}-rw"
write_policy "${policy_name}" "${policy}"

# append policy to auth role list
VAULT_JWT_AUTH_ROLE_POLICIES="${VAULT_JWT_AUTH_ROLE_POLICIES},${policy_name}"




#--------------------------------------------
# Add Access to API-KEYS API via JWT Secret Role
#--------------------------------------------

API_KEYS_API_SERVICE_ACCOUNT_NAMESPACE="api-keys"
API_KEYS_API_SERVICE_ACCOUNT_NAME="api-keys-api"
API_KEYS_API_SECRET_BASE_PATH="services/${API_KEYS_API_SERVICE_ACCOUNT_NAME}"
API_KEYS_API_SECRET_POLICY_PATH="services-${API_KEYS_API_SERVICE_ACCOUNT_NAME}"
SCOPES="pdp-evaluate"

#-------------------------------------------
# Create JWT Secret Role for NVCF JWT Signer
#-------------------------------------------
jwt_secret_role=$(generate_jwt_secret_role "${API_KEYS_API_SERVICE_ACCOUNT_NAMESPACE}" "${API_KEYS_API_SERVICE_ACCOUNT_NAME}" "${SERVICE_ACCOUNT_NAME}" "${SCOPES}")
create_secret_jwt_role "${API_KEYS_API_SECRET_BASE_PATH}/jwt" "${SERVICE_ACCOUNT_NAME}" "${jwt_secret_role}"

#-------------------------------------------
# Create policy for JWT Secret Role
#-------------------------------------------
policy=$(generate_acl_policy "${API_KEYS_API_SECRET_BASE_PATH}/jwt/sign/${SERVICE_ACCOUNT_NAME}" "create,update,read")
policy_name="${API_KEYS_API_SECRET_POLICY_PATH}-jwt-sign-${SERVICE_ACCOUNT_NAME}-rw"
write_policy "${policy_name}" "${policy}"

# append policy to auth role list
VAULT_JWT_AUTH_ROLE_POLICIES="${VAULT_JWT_AUTH_ROLE_POLICIES},${policy_name}"



#-------------------------------------------
# Add Access to ESS API via JWT Secret Role
#-------------------------------------------

ESS_API_SERVICE_ACCOUNT_NAMESPACE="ess"
ESS_API_SERVICE_ACCOUNT_NAME="ess-api"
ESS_API_SECRET_BASE_PATH="services/${ESS_API_SERVICE_ACCOUNT_NAME}"
ESS_API_SECRET_POLICY_PATH="services-${ESS_API_SERVICE_ACCOUNT_NAME}"
SCOPES="ess:secrets-admin,ess:entities-admin"

#-------------------------------------------
# Create JWT Secret Role for NVCF JWT Signer
#-------------------------------------------
jwt_secret_role=$(generate_jwt_secret_role "${ESS_API_SERVICE_ACCOUNT_NAMESPACE}" "${ESS_API_SERVICE_ACCOUNT_NAME}" "${SERVICE_ACCOUNT_NAME}" "${SCOPES}")
create_secret_jwt_role "${ESS_API_SECRET_BASE_PATH}/jwt" "${SERVICE_ACCOUNT_NAME}" "${jwt_secret_role}"

#-------------------------------------------
# Create policy for JWT Secret Role
#-------------------------------------------
policy=$(generate_acl_policy "${ESS_API_SECRET_BASE_PATH}/jwt/sign/${SERVICE_ACCOUNT_NAME}" "create,update,read")
policy_name="${ESS_API_SECRET_POLICY_PATH}-jwt-sign-${SERVICE_ACCOUNT_NAME}-rw"
write_policy "${policy_name}" "${policy}"

# append policy to auth role list
VAULT_JWT_AUTH_ROLE_POLICIES="${VAULT_JWT_AUTH_ROLE_POLICIES},${policy_name}"

#-------------------------------------------
# Add Access to API Keys API via JWT Secret Role
#-------------------------------------------

API_KEYS_API_SERVICE_ACCOUNT_NAMESPACE="api-keys"
API_KEYS_API_SERVICE_ACCOUNT_NAME="api-keys-api"
API_KEYS_API_SERVICE_NAME="api-keys"
API_KEYS_API_SECRET_BASE_PATH="services/${API_KEYS_API_SERVICE_ACCOUNT_NAME}"
API_KEYS_API_SECRET_POLICY_PATH="services-${API_KEYS_API_SERVICE_ACCOUNT_NAME}"
SCOPES="pdp-evaluate"

#-------------------------------------------
# Create JWT Secret Role for API Keys API JWT Signer
#-------------------------------------------
# Issuer: http://api-keys.api-keys.svc.cluster.local
jwt_secret_role=$(generate_jwt_secret_role "${API_KEYS_API_SERVICE_ACCOUNT_NAMESPACE}" "${API_KEYS_API_SERVICE_NAME}" "${SERVICE_ACCOUNT_NAME}" "${SCOPES}")
create_secret_jwt_role "${API_KEYS_API_SECRET_BASE_PATH}/jwt" "${SERVICE_ACCOUNT_NAME}" "${jwt_secret_role}"


#-------------------------------------------
# Create policy for JWT Secret Role
#-------------------------------------------
policy=$(generate_acl_policy "${API_KEYS_API_SECRET_BASE_PATH}/jwt/sign/${SERVICE_ACCOUNT_NAME}" "create,update,read")
policy_name="${API_KEYS_API_SECRET_POLICY_PATH}-jwt-sign-${SERVICE_ACCOUNT_NAME}-rw"
write_policy "${policy_name}" "${policy}"

# append policy to auth role list
VAULT_JWT_AUTH_ROLE_POLICIES="${VAULT_JWT_AUTH_ROLE_POLICIES},${policy_name}"


#-------------------------------------------
# Add Access to Reval API via JWT Secret Role
#-------------------------------------------

REVAL_SERVICE_ACCOUNT_NAMESPACE="nvcf"
REVAL_SERVICE_ACCOUNT_NAME="reval"
REVAL_SERVICE_NAME="reval"
REVAL_SECRET_BASE_PATH="services/${REVAL_SERVICE_ACCOUNT_NAME}"
REVAL_SECRET_POLICY_PATH="services-${REVAL_SERVICE_ACCOUNT_NAME}"
REVAL_SCOPES="helmreval:validate"

#-------------------------------------------
# Create JWT Secret Role for Reval JWT Signer
#-------------------------------------------
# Issuer: http://reval.nvcf.svc.cluster.local
jwt_secret_role=$(generate_jwt_secret_role "${REVAL_SERVICE_ACCOUNT_NAMESPACE}" "${REVAL_SERVICE_NAME}" "${SERVICE_ACCOUNT_NAME}" "${REVAL_SCOPES}")
create_secret_jwt_role "${REVAL_SECRET_BASE_PATH}/jwt" "${SERVICE_ACCOUNT_NAME}" "${jwt_secret_role}"

#-------------------------------------------
# Create policy for Reval JWT Secret Role
#-------------------------------------------
policy=$(generate_acl_policy "${REVAL_SECRET_BASE_PATH}/jwt/sign/${SERVICE_ACCOUNT_NAME}" "create,update,read")
policy_name="${REVAL_SECRET_POLICY_PATH}-jwt-sign-${SERVICE_ACCOUNT_NAME}-rw"
write_policy "${policy_name}" "${policy}"

# append policy to auth role list
VAULT_JWT_AUTH_ROLE_POLICIES="${VAULT_JWT_AUTH_ROLE_POLICIES},${policy_name}"


#-------------------------------------------
# Provision JWT Auth Role with policies
#-------------------------------------------
jwt_auth_role=$(generate_jwt_auth_role "${SERVICE_ACCOUNT_NAME}" "${SERVICE_ACCOUNT_NAMESPACE}" "$VAULT_JWT_AUTH_ROLE_POLICIES")
create_auth_jwt_role "${SERVICE_ACCOUNT_NAME}" "${jwt_auth_role}"

#-------------------------------------------
# Create policy for the ADMIN JWT Secret Role
#-------------------------------------------
policy=$(generate_acl_policy "${VAULT_SECRET_BASE_PATH}/jwt/sign/${SERVICE_ACCOUNT_NAME}-admin" "create,update,read")
policy_name="${VAULT_POLICY_BASE_PATH}-jwt-sign-${SERVICE_ACCOUNT_NAME}-admin-rw"
write_policy "${policy_name}" "${policy}"

#-------------------------------------------
# Provision JWT Auth Role for the BOOTSTRAP Service Account
#-------------------------------------------
bootstrap_auth_role_policies="${policy_name}"
bootstrap_auth_role=$(generate_jwt_auth_role "${NVCF_NCA_BOOTSTRAP_SERVICE_ACCOUNT_NAME}" "${SERVICE_ACCOUNT_NAMESPACE}" "${bootstrap_auth_role_policies}")
create_auth_jwt_role "${NVCF_NCA_BOOTSTRAP_SERVICE_ACCOUNT_NAME}" "${bootstrap_auth_role}"
