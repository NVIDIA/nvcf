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

#-------------------------------------------
# Set defaults for secret paths and policies
#-------------------------------------------
VAULT_SECRET_BASE_PATH="services/all"
VAULT_POLICY_BASE_PATH="services-all"

#-------------------------------------------
# Create KV2 secrets engine 
#-------------------------------------------
enable_secrets_mount "${VAULT_SECRET_BASE_PATH}/kv" "kv-v2"

#-------------------------------------------
# Create policy for KV access
#-------------------------------------------
policy=$(generate_acl_policy "${VAULT_SECRET_BASE_PATH}/kv/*" "read,list")
write_policy "${VAULT_POLICY_BASE_PATH}-kv-ro" "${policy}"
