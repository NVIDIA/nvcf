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

SERVICE_ACCOUNT_NAMESPACE="api-keys"
SERVICE_ACCOUNT_NAME="admin-issuer-proxy"
SERVICE_NAME="admin-issuer-proxy"

#-------------------------------------------
# Set defaults for secret paths and policies
#-------------------------------------------
# Use the existing NVCF API JWT mount (since we're issuing tokens for that issuer)
NVCF_API_JWT_MOUNT="services/nvcf-api/jwt"
VAULT_POLICY_BASE_PATH="services-${SERVICE_ACCOUNT_NAME}"
VAULT_JWT_AUTH_ROLE_POLICIES=""

#-------------------------------------------
# JWT Secret Engine already exists
#-------------------------------------------
# The JWT secrets engine at services/nvcf-api/jwt was created by 08_setup_nvcf-api.sh
# We're just adding a new role to it for the admin-issuer-proxy to use

#-------------------------------------------
# Create JWT Secret Role for Admin operations
#-------------------------------------------
# This role allows the proxy to sign JWTs with admin-level scopes.
# Issuer will be: http://api.nvcf.svc.cluster.local (same as NVCF API)
# Client will be: admin-issuer-proxy (the proxy's identity)

NVCF_API_NAMESPACE="nvcf"
NVCF_API_SERVICE_NAME="api"

# Full admin-level scopes for NVCF operations
SCOPES="register_function,list_functions,list_functions_details,deploy_function,update_function,update_secrets,delete_function,manage_telemetries,manage_registry_credentials,cluster-management"

# Use ADMIN_CLIENT_ID from environment (set via Helm values) as the client identity in the JWT
# This determines the sub, azp, and aud claims in the signed JWT
CLIENT_ID="${ADMIN_CLIENT_ID:-${SERVICE_ACCOUNT_NAME}}"

# Generate a JWT secret role with NVCF API as issuer, CLIENT_ID as client
jwt_secret_role=$(generate_jwt_secret_role "${NVCF_API_NAMESPACE}" "${NVCF_API_SERVICE_NAME}" "${CLIENT_ID}" "${SCOPES}")

# Register the role under a descriptive name that identifies the proxy
ROLE_NAME="admin-issuer-proxy"
create_secret_jwt_role "${NVCF_API_JWT_MOUNT}" "${ROLE_NAME}" "${jwt_secret_role}"

#-------------------------------------------
# Policy allowing Admin Issuer Proxy to sign tokens for the role
#-------------------------------------------
policy=$(generate_acl_policy "${NVCF_API_JWT_MOUNT}/sign/${ROLE_NAME}" "create,update,read")
policy_name="${VAULT_POLICY_BASE_PATH}-jwt-sign-${ROLE_NAME}-rw"
write_policy "${policy_name}" "${policy}"

# Set policy for auth role (proxy only needs signing permissions, no KV access)
VAULT_JWT_AUTH_ROLE_POLICIES="${policy_name}"

#-------------------------------------------
# Provision JWT Auth Role for the Admin Issuer Proxy service account
#-------------------------------------------
jwt_auth_role=$(generate_jwt_auth_role "${SERVICE_ACCOUNT_NAME}" "${SERVICE_ACCOUNT_NAMESPACE}" "$VAULT_JWT_AUTH_ROLE_POLICIES")
create_auth_jwt_role "${SERVICE_ACCOUNT_NAME}" "${jwt_auth_role}"
