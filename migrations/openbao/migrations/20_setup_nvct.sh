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
SERVICE_ACCOUNT_NAME="nvct-api"

#-------------------------------------------
# Set defaults for secret paths and policies
#-------------------------------------------
VAULT_SECRET_BASE_PATH="services/${SERVICE_ACCOUNT_NAME}"
VAULT_POLICY_BASE_PATH="services-${SERVICE_ACCOUNT_NAME}"
# services-all-kv-ro grants read access to shared secrets:
#   - services/all/kv/encryption/keys/audit  (kv.audit-hmac-kid, kv.audit-hmac-keys)
#   - services/all/kv/encryption/keys/stored_data  (kv.jwks, kv.kid)
VAULT_JWT_AUTH_ROLE_POLICIES="services-all-kv-ro"

#-------------------------------------------
# Create KV2 secrets engine
#-------------------------------------------
enable_secrets_mount "${VAULT_SECRET_BASE_PATH}/kv" "kv-v2"

#-------------------------------------------
# Create default service paths and secrets
#-------------------------------------------
write_secrets_kv "${VAULT_SECRET_BASE_PATH}/kv" "cassandra/creds" "username=nvct_api_app_v0 password=${DEFAULT_CASSANDRA_PASSWORD}"
write_secrets_kv "${VAULT_SECRET_BASE_PATH}/kv" "sidecars/image-pull-secret" "secret=${NVCF_API_SIDECARS_IMAGE_PULL_SECRET:-replace}"

#-------------------------------------------
# Create policy for KV access
#-------------------------------------------
policy=$(generate_acl_policy "${VAULT_SECRET_BASE_PATH}/kv/*" "read,list")
write_policy "${VAULT_POLICY_BASE_PATH}-kv-ro" "${policy}"

# append policy to auth role list
VAULT_JWT_AUTH_ROLE_POLICIES="${VAULT_JWT_AUTH_ROLE_POLICIES},${VAULT_POLICY_BASE_PATH}-kv-ro"

#-------------------------------------------
# Add JWT sign access to NVCF API for nvct-api (notary + account_setup)
#-------------------------------------------

NVCF_API_SERVICE_ACCOUNT_NAMESPACE="nvcf"
NVCF_API_SERVICE_ACCOUNT_NAME="nvcf-api"
NVCF_API_SERVICE_NAME="api"
NVCF_API_SECRET_BASE_PATH="services/${NVCF_API_SERVICE_ACCOUNT_NAME}"
NVCF_API_SECRET_POLICY_PATH="services-${NVCF_API_SERVICE_ACCOUNT_NAME}"
NVCT_NVCF_API_ROLE_NOTARY="nvct-api-notary"
NVCT_NVCF_API_ROLE_API="nvct-api-nvcf"

#-------------------------------------------
# Create JWT Secret Role for Self to use with Notary (nvct-api)
#-------------------------------------------
scopes="notary-sign,make-assertion"
# Issuer: http://api.nvcf.svc.cluster.local
jwt_secret_role=$(generate_jwt_secret_role "${NVCF_API_SERVICE_ACCOUNT_NAMESPACE}" "${NVCF_API_SERVICE_NAME}" "${SERVICE_ACCOUNT_NAME}" "${scopes}")
create_secret_jwt_role "${NVCF_API_SECRET_BASE_PATH}/jwt" "${NVCT_NVCF_API_ROLE_NOTARY}" "${jwt_secret_role}"

#-------------------------------------------
# Create policy for JWT Secret Role
#-------------------------------------------
policy=$(generate_acl_policy "${NVCF_API_SECRET_BASE_PATH}/jwt/sign/${NVCT_NVCF_API_ROLE_NOTARY}" "create,update,read")
policy_name="${NVCF_API_SECRET_POLICY_PATH}-jwt-sign-${NVCT_NVCF_API_ROLE_NOTARY}-rw"
write_policy "${policy_name}" "${policy}"

# append policy to auth role list
VAULT_JWT_AUTH_ROLE_POLICIES="${VAULT_JWT_AUTH_ROLE_POLICIES},${policy_name}"


#-------------------------------------------
# Create JWT Secret Role for NVCF API account_setup (NvcfClient)
#-------------------------------------------
scopes="account_setup"
# Issuer: http://api.nvcf.svc.cluster.local
jwt_secret_role=$(generate_jwt_secret_role "${NVCF_API_SERVICE_ACCOUNT_NAMESPACE}" "${NVCF_API_SERVICE_NAME}" "${SERVICE_ACCOUNT_NAME}" "${scopes}")
create_secret_jwt_role "${NVCF_API_SECRET_BASE_PATH}/jwt" "${NVCT_NVCF_API_ROLE_API}" "${jwt_secret_role}"

#-------------------------------------------
# Create policy for JWT Secret Role
#-------------------------------------------
policy=$(generate_acl_policy "${NVCF_API_SECRET_BASE_PATH}/jwt/sign/${NVCT_NVCF_API_ROLE_API}" "create,update,read")
policy_name="${NVCF_API_SECRET_POLICY_PATH}-jwt-sign-${NVCT_NVCF_API_ROLE_API}-rw"
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
# API Keys service (NAK) does not enforce scopes, so none are issued
SCOPES=""

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
