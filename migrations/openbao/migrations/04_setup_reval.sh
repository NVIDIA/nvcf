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
    echo "Error: utils.sh not found in ${curr_dir}/utils/utils.sh"
    return 1
else
  source "${curr_dir}/utils/utils.sh" 
  source "${curr_dir}/utils/functions.sh"
fi

SERVICE_ACCOUNT_NAMESPACE="nvcf"
SERVICE_ACCOUNT_NAME="reval"

#-------------------------------------------
# Set defaults for secret paths and policies
#-------------------------------------------
VAULT_SECRET_BASE_PATH="services/${SERVICE_ACCOUNT_NAME}"
VAULT_JWT_AUTH_ROLE_POLICIES="services-all-kv-ro"

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
