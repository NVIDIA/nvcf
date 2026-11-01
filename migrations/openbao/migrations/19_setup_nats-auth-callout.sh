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

# Auth-callout pod's ServiceAccount lives in nats-system (with the rest of
# the NATS data plane), not in the standard `nvcf` ns the other per-service
# scripts use. The `services-all-kv-ro` policy is enough — auth-callout only
# reads its own KV path written by 03_setup_shared_secrets.sh.
SERVICE_ACCOUNT_NAMESPACE="nats-system"
SERVICE_ACCOUNT_NAME="nats-auth-callout"

VAULT_JWT_AUTH_ROLE_POLICIES="services-all-kv-ro"

#-------------------------------------------
# Provision JWT Auth Role bound to the auth-callout pod's ServiceAccount.
# The vault-agent sidecar in the colocated-deploy chart authenticates to
# OpenBao using the SA's projected token at this role.
#-------------------------------------------
jwt_auth_role=$(generate_jwt_auth_role "${SERVICE_ACCOUNT_NAME}" "${SERVICE_ACCOUNT_NAMESPACE}" "$VAULT_JWT_AUTH_ROLE_POLICIES")
create_auth_jwt_role "${SERVICE_ACCOUNT_NAME}" "${jwt_auth_role}"
